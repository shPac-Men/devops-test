#!/usr/bin/env python3
"""Validate the deployed monitoring chain through Grafana, without changing services."""
import argparse
import base64
import json
import math
from pathlib import Path
import signal
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


DATASOURCE = 'safeboard-prometheus'
EXPECTED_TARGETS = {
    ('prometheus', 'prometheus:9090'),
    ('node', 'node-exporter:9100'),
    ('cadvisor', 'cadvisor:8080'),
    ('mariadb', 'mysqld-exporter:9104'),
    ('mariadb', 'mysqld-exporter-2:9104'),
    ('mariadb', 'mysqld-exporter-3:9104'),
    ('haproxy', 'http://haproxy:8404/health'),
    ('blackbox', 'blackbox-exporter:9115'),
    ('kuma-http', 'http://uptime-kuma:3001/'),
}
SERVICES = {'mariadb', 'mariadb-2', 'mariadb-3', 'haproxy', 'uptime-kuma'}
SERVICE_LABEL = 'container_label_com_docker_compose_service'
APP = 'job="cadvisor",container_label_com_docker_compose_project="safeboard-app"'
# Check raw sample timestamps: computed PromQL values have evaluation timestamps,
# which alone cannot prove that the underlying samples are fresh.
RAW_METRICS = [
    ('scrape status', 'up', False),
    ('MariaDB status', 'mysql_up{job="mariadb"}', False),
    ('Kuma HTTP status', 'probe_success{job="kuma-http"}', False),
    ('host CPU', 'node_cpu_seconds_total{job="node",mode="idle"}', False),
    ('host available RAM', 'node_memory_MemAvailable_bytes{job="node"}', False),
    ('host total RAM', 'node_memory_MemTotal_bytes{job="node"}', False),
    ('root available space', 'node_filesystem_avail_bytes{job="node",mountpoint="/"}', False),
    ('root total space', 'node_filesystem_size_bytes{job="node",mountpoint="/"}', False),
    ('container CPU', 'container_cpu_usage_seconds_total{' + APP + '}', True),
    ('container RAM', 'container_memory_working_set_bytes{' + APP + '}', True),
    ('MariaDB connections', 'mysql_global_status_threads_connected{job="mariadb"}', False),
    ('Kuma HTTP duration', 'probe_duration_seconds{job="kuma-http"}', False),
]


class ValidationError(RuntimeError):
    pass


class DeadlineExpired(RuntimeError):
    pass


def require_services(rows, label):
    found = {row['metric'].get(SERVICE_LABEL) for row in rows}
    missing = SERVICES - found
    if missing:
        raise ValidationError(f'{label}: missing container series: {", ".join(sorted(missing))}')


class Validator:
    def __init__(self, timeout, base='http://grafana:3000'):
        password = Path('.secrets/monitoring/grafana_admin_password').read_text().strip()
        self.auth = 'Basic ' + base64.b64encode(f'admin:{password}'.encode()).decode()
        self.base = base
        self.timeout = timeout
        self.deadline = time.monotonic() + timeout

    def remaining(self):
        left = self.deadline - time.monotonic()
        if left <= 0:
            raise DeadlineExpired('validation deadline reached')
        return left

    def get(self, path):
        request = urllib.request.Request(self.base + path, headers={'Authorization': self.auth})
        try:
            with urllib.request.urlopen(request, timeout=min(10, self.remaining())) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            # Do not print response bodies or credentials.
            raise ValidationError(f'Grafana {path.split("?")[0]}: HTTP {error.code}') from None
        except (OSError, ValueError) as error:
            raise ValidationError(f'Grafana request failed: {type(error).__name__}') from None

    def prometheus(self, path):
        result = self.get(f'/api/datasources/proxy/uid/{DATASOURCE}/api/v1/' + path)
        if result.get('status') != 'success':
            raise ValidationError('Prometheus API did not return success')
        return result['data']

    def query(self, expression):
        data = self.prometheus('query?' + urllib.parse.urlencode({'query': expression}))
        rows = data.get('result', [])
        if data.get('resultType') != 'vector' or not rows:
            raise ValidationError(f'no metric series: {expression}')
        if any(not math.isfinite(float(row['value'][1])) for row in rows):
            raise ValidationError(f'non-finite metric value: {expression}')
        return rows

    def check_targets(self):
        targets = self.prometheus('targets?state=active')['activeTargets']
        found = [(t['labels'].get('job'), t['labels'].get('instance')) for t in targets]
        if len(found) != len(EXPECTED_TARGETS) or set(found) != EXPECTED_TARGETS:
            missing = sorted(EXPECTED_TARGETS - set(found))
            extra = sorted(set(found) - EXPECTED_TARGETS, key=str)
            raise ValidationError(f'Prometheus target set mismatch: missing={missing}, unexpected={extra}, count={len(found)}')
        for target, key in zip(targets, found):
            if target['health'] != 'up':
                raise ValidationError(f'Prometheus target {key[0]}/{key[1]} is not UP')
        rows = self.query('up')
        keys = {(r['metric'].get('job'), r['metric'].get('instance')) for r in rows}
        if keys != EXPECTED_TARGETS or len(rows) != len(EXPECTED_TARGETS) or any(float(r['value'][1]) != 1 for r in rows):
            raise ValidationError('expected nine scraped up=1 series')

    def check_metrics(self):
        for label, expression, containers in RAW_METRICS:
            stamp_query = f'timestamp({expression})'
            if containers:
                # cAdvisor retains stopped container incarnations briefly.
                # Require a fresh sample for every service using its newest incarnation.
                stamp_query = f'max by ({SERVICE_LABEL}) ({stamp_query})'
            rows = self.query(stamp_query)
            now = time.time()
            if any(not 0 <= now - float(row['value'][1]) <= 30 for row in rows):
                raise ValidationError(f'{label}: raw samples must be no older than 30 seconds')
            if containers:
                require_services(rows, label)
        for metric, expected in [('wsrep_cluster_size', 3), ('wsrep_local_state', 4), ('wsrep_connected', 1), ('wsrep_ready', 1)]:
            rows = self.query('mysql_global_status_' + metric + '{job="mariadb"}')
            stamps = self.query('timestamp(mysql_global_status_' + metric + '{job="mariadb"})')
            if len(rows) != 3 or any(float(r['value'][1]) != expected for r in rows) or len(stamps) != 3 or any(not 0 <= time.time() - float(r['value'][1]) <= 30 for r in stamps):
                raise ValidationError(f'Galera {metric}: expected three fresh values = {expected}')
        for expression in ('mysql_up{job="mariadb"}', 'probe_success{job="kuma-http"}', 'probe_success{job="haproxy"}'):
            rows = self.query(expression)
            if len(rows) != (3 if 'mysql_up' in expression else 1) or any(float(r['value'][1]) != 1 for r in rows):
                raise ValidationError(f'service unavailable: expected {expression}=1')

    def check_panels(self):
        dashboard = self.get('/api/dashboards/uid/safeboard-health')['dashboard']
        panels = dashboard['panels']
        ids = [p['id'] for p in panels]
        if len(ids) != 17 or set(ids) != set(range(1, 18)):
            raise ValidationError('saved dashboard must contain the 17 expected panels')
        for panel in panels:
            label = f'panel {panel["id"]} ({panel["title"]})'
            targets = [t for t in panel.get('targets', []) if not t.get('hide')]
            if not targets:
                raise ValidationError(f'{label}: no visible queries')
            for target in targets:
                source = target.get('datasource', panel.get('datasource', {}))
                if source.get('uid') != DATASOURCE or source.get('type') != 'prometheus':
                    raise ValidationError(f'{label}: unexpected data source')
                # The saved dashboard uses a 15s scrape interval; 1m supplies
                # the four samples recommended for its CPU rate expressions.
                expression = target['expr'].replace('$__rate_interval', '1m').replace('$__interval', '15s')
                if '$' in expression:
                    raise ValidationError(f'{label}: unsupported query variable')
                try:
                    rows = self.query(expression)
                    if panel['id'] in (1, 2, 3) and (len(rows) != 1 or float(rows[0]['value'][1]) != 1):
                        raise ValidationError('expected status=1')
                    if panel['id'] in (8, 9):
                        require_services(rows, label)
                except ValidationError as error:
                    raise ValidationError(f'{label}: {error}') from None

    def check(self):
        self.check_targets()
        self.check_metrics()
        self.check_panels()

    def run(self):
        last_error = 'no completed check'

        def expired(signum, frame):
            raise DeadlineExpired('validation deadline reached')

        # A socket timeout is per operation, not an overall deadline. The timer
        # also bounds slow response reads and the complete sequence of requests.
        previous_handler = signal.signal(signal.SIGALRM, expired)
        signal.setitimer(signal.ITIMER_REAL, self.remaining())
        try:
            while True:
                try:
                    self.check()
                    self.remaining()
                    print('PASS: nine expected targets UP; fresh service/resource metrics; all 17 saved Grafana panels have valid data', flush=True)
                    return
                except ValidationError as error:
                    reason = str(error)
                    if reason != last_error:
                        print(f'WAIT: {reason}', flush=True)
                    last_error = reason
                time.sleep(min(2, self.remaining()))
        except DeadlineExpired:
            raise ValidationError(f'validation timed out after {self.timeout}s; last failure: {last_error}') from None
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous_handler)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--timeout', type=int, default=120)
    args = parser.parse_args()
    if not 1 <= args.timeout <= 600:
        parser.error('--timeout must be between 1 and 600 seconds')
    try:
        Validator(args.timeout).run()
    except (ValidationError, OSError, ValueError, KeyError, TypeError) as error:
        print(f'FAIL: validate-platform: {error}', file=sys.stderr)
        sys.exit(1)
