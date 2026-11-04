#!/usr/bin/env python3
"""Bounded stop/recovery and restart-policy checks against the local Docker host."""
import argparse
import base64
import datetime
import http.client
import json
import os
from pathlib import Path
import signal
import socket
import sys
import time
import urllib.parse
import urllib.request


class UnixConnection(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect('/var/run/docker.sock')


def docker(method, path):
    connection = UnixConnection('localhost', timeout=20)
    try:
        connection.request(method, path)
        response = connection.getresponse()
        data = response.read()
        if response.status not in (200, 204, 304):
            raise RuntimeError(f'Docker {method} {path}: HTTP {response.status}: {data.decode()[:300]}')
        return json.loads(data) if data else None
    finally:
        connection.close()


class ChaosTest:
    def __init__(self, target, timeout):
        self.target, self.timeout = target, timeout
        password = Path('.secrets/monitoring/grafana_admin_password').read_text().strip()
        self.auth = 'Basic ' + base64.b64encode(f'admin:{password}'.encode()).decode()
        filters = json.dumps({'label': ['com.docker.compose.project=safeboard-app',
                                      f'com.docker.compose.service={target}']})
        containers = docker('GET', '/containers/json?all=true&' + urllib.parse.urlencode({'filters': filters}))
        if len(containers) != 1:
            raise RuntimeError(f'Expected exactly one safeboard-app/{target} container; run deploy-all first')
        self.container_id = containers[0]['Id']
        self.path = f'/containers/{self.container_id}'
        self.metric = 'mysql_up{job="mariadb"}' if target == 'mariadb' else 'probe_success{job="kuma-http"}'
        self.scrape = 'up{job="mariadb"}' if target == 'mariadb' else 'up{job="kuma-http"}'
        self.modified = False

    def grafana(self, path):
        req = urllib.request.Request('http://127.0.0.1:3000' + path, headers={'Authorization': self.auth})
        with urllib.request.urlopen(req, timeout=10) as response:
            return json.load(response)

    def query(self, expression):
        result = self.grafana('/api/datasources/proxy/uid/safeboard-prometheus/api/v1/query?' +
                              urllib.parse.urlencode({'query': expression}))
        rows = result.get('data', {}).get('result', [])
        if result.get('status') != 'success' or len(rows) != 1:
            raise RuntimeError(f'Expected one metric series: {expression}')
        return float(rows[0]['value'][1])

    def inspect(self):
        return docker('GET', self.path + '/json')

    def healthy(self):
        state = self.inspect()['State']
        return state['Running'] and state.get('Health', {}).get('Status') == 'healthy'

    def wait(self, label, condition):
        deadline = time.monotonic() + self.timeout
        last_error = 'condition not yet satisfied'
        while time.monotonic() < deadline:
            try:
                if condition():
                    print(f'PASS: {label}', flush=True)
                    return
            except (RuntimeError, OSError, ValueError) as error:
                last_error = str(error)
            time.sleep(2)
        raise RuntimeError(f'{label}: timed out after {self.timeout}s ({last_error})')

    def observed(self, value, since):
        # A successful scrape and a new raw sample prevent a monitoring outage
        # or an old sample from masquerading as the induced target failure.
        return (self.query(self.scrape) == 1 and self.query(self.metric) == value
                and self.query(f'timestamp({self.metric})') >= since
                and self.query(self.panel_expression) == value)

    def recover(self):
        info = self.inspect()
        if not info['State']['Running'] and not info['State'].get('Restarting'):
            docker('POST', self.path + '/start')
        self.wait(f'{self.target}: container healthy', self.healthy)

    def run(self):
        info = self.inspect()
        if not self.healthy():
            raise RuntimeError('Target must already be healthy; run deploy-all first')
        if info['HostConfig']['RestartPolicy']['Name'] not in ('always', 'unless-stopped'):
            raise RuntimeError('Target requires restart policy always or unless-stopped')
        dashboard = self.grafana('/api/dashboards/uid/safeboard-health')['dashboard']
        panel_id = 1 if self.target == 'mariadb' else 2
        panel = next(p for p in dashboard['panels'] if p['id'] == panel_id)
        self.panel_expression = panel['targets'][0]['expr']
        # Check the saved dashboard actually represents the selected target.
        if self.metric not in self.panel_expression or self.scrape not in self.panel_expression:
            raise RuntimeError('Saved Grafana status panel does not match the target metrics')
        self.wait('baseline dashboard status = 1', lambda: self.observed(1, time.time() - 45))
        print(f'Target: {self.target}; dashboard panel: {panel["title"]}', flush=True)
        try:
            self.modified = True
            stopped_at = time.time()
            print('STOP: Docker stop API (equivalent to docker stop)', flush=True)
            docker('POST', self.path + '/stop?t=10')
            self.wait('dashboard status = 0 with a fresh scrape', lambda: self.observed(0, stopped_at))
            # Keep the outage for two separate samples so a 15s-refresh dashboard
            # has time to show it, instead of merely observing a momentary zero.
            first_zero = self.query(f'timestamp({self.metric})')
            self.wait('dashboard status remains 0 on the next scrape',
                      lambda: self.observed(0, first_zero + 1))
            print('START: explicit start required after a manual stop', flush=True)
            restarted_at = time.time()
            docker('POST', self.path + '/start')
            self.recover()
            self.wait('dashboard status restored to 1', lambda: self.observed(1, restarted_at))

            # Killing through Docker's stop/kill APIs marks a manual stop.
            # Signal the host PID directly to test an actual process failure.
            def ready_for_crash():
                current = self.inspect()
                started = datetime.datetime.fromisoformat(current['State']['StartedAt'].replace('Z', '+00:00')).timestamp()
                return self.healthy() and time.time() - started >= 10
            self.wait('restart-policy activation window (10s)', ready_for_crash)
            before = self.inspect()
            pid = before['State']['Pid']
            if pid <= 1 or pid == os.getpid():
                raise RuntimeError('Unsafe target PID; refusing crash injection')
            crash_at = time.time()
            print(f'CRASH: SIGKILL to selected container main process; restart count before={before["RestartCount"]}', flush=True)
            os.kill(pid, signal.SIGKILL)
            self.wait('restart-policy restarted the container without docker start',
                      lambda: self.inspect()['RestartCount'] > before['RestartCount'] and self.healthy())
            self.wait('dashboard status = 1 after automatic restart', lambda: self.observed(1, crash_at))
            self.modified = False
            print(f'PASS: chaos-test {self.target}: manual-stop outage/recovery and automatic restart verified', flush=True)
        finally:
            if self.modified:
                print('CLEANUP: restoring target after an interrupted/failed test', flush=True)
                # Ignore further termination signals while attempting bounded cleanup.
                signal.signal(signal.SIGTERM, signal.SIG_IGN)
                signal.signal(signal.SIGINT, signal.SIG_IGN)
                original_error = sys.exception()
                try:
                    self.recover()
                except Exception as cleanup_error:
                    raise RuntimeError(f'{original_error}; cleanup also failed: {cleanup_error}') from cleanup_error


def interrupted(signum, frame):
    raise RuntimeError(f'Test interrupted by signal {signum}')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', required=True, choices=['mariadb', 'uptime-kuma'])
    parser.add_argument('--timeout', type=int, default=120)
    args = parser.parse_args()
    if not 30 <= args.timeout <= 600:
        parser.error('--timeout must be between 30 and 600')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        ChaosTest(args.target, args.timeout).run()
    except Exception as error:
        print(f'FAIL: chaos-test {args.target}: {error}', file=sys.stderr)
        sys.exit(1)
