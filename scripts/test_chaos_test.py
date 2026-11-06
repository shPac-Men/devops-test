"""Safety checks for failure cleanup; these tests do not access Docker."""
import importlib.util
from pathlib import Path
import time
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('chaos', Path(__file__).with_name('chaos-test.py'))
chaos = importlib.util.module_from_spec(spec)
spec.loader.exec_module(chaos)


class ChaosSafetyTests(unittest.TestCase):
    def runner(self):
        runner = object.__new__(chaos.ChaosTest)
        runner.target = 'mariadb'
        runner.timeout = 30
        runner.path = '/containers/test'
        runner.modified = False
        runner.metric = 'mysql_up{job="mariadb"}'
        runner.scrape = 'up{job="mariadb"}'
        runner.healthy = Mock(return_value=True)
        runner.inspect = Mock(return_value={'HostConfig': {'RestartPolicy': {'Name': 'unless-stopped'}}})
        runner.grafana = Mock(return_value={'dashboard': {'panels': [{
            'id': 1, 'title': 'MariaDB', 'targets': [{'expr': runner.metric + ' * ' + runner.scrape}]
        }]}})
        runner.wait = Mock(side_effect=[None, RuntimeError('injected observation timeout')])
        runner.recover = Mock()
        return runner

    @patch.object(chaos, 'docker')
    def test_failed_observation_restores_target_and_preserves_failure(self, docker):
        runner = self.runner()
        with patch.object(chaos.signal, 'signal'):
            with self.assertRaisesRegex(RuntimeError, 'injected observation timeout'):
                runner.run()
        docker.assert_called_once_with('POST', '/containers/test/stop?t=10')
        runner.recover.assert_called_once_with()

    @patch.object(chaos, 'docker')
    def test_cleanup_failure_reports_both_causes(self, docker):
        runner = self.runner()
        runner.recover.side_effect = RuntimeError('injected recovery failure')
        with patch.object(chaos.signal, 'signal'):
            with self.assertRaisesRegex(RuntimeError, 'observation timeout; cleanup also failed: injected recovery failure'):
                runner.run()

    @patch.object(chaos, 'docker')
    def test_unhealthy_baseline_never_stops_target(self, docker):
        runner = self.runner()
        runner.healthy.return_value = False
        with self.assertRaisesRegex(RuntimeError, 'must already be healthy'):
            runner.run()
        docker.assert_not_called()
        runner.recover.assert_not_called()

    def test_old_zero_sample_cannot_confirm_new_outage(self):
        runner = self.runner()
        runner.query = Mock(side_effect=[1, 0, 99])
        self.assertFalse(runner.observed(0, since=100))

    @patch.object(chaos, 'docker')
    def test_signal_during_observation_immediately_restores_target(self, docker):
        for signum in (chaos.signal.SIGTERM, chaos.signal.SIGINT):
            with self.subTest(signum=signum):
                runner = self.runner()
                # Use the real wait loop: baseline succeeds, then a signal
                # arrives while observing the manually stopped container.
                runner.wait = chaos.ChaosTest.wait.__get__(runner)

                def observe(value, since):
                    if value == 0:
                        chaos.interrupted(signum, None)
                    return True

                runner.observed = Mock(side_effect=observe)
                docker.reset_mock()
                with patch.object(chaos.signal, 'signal'), \
                        patch.object(chaos.time, 'sleep', side_effect=AssertionError('Signal was retried')) as sleep:
                    with self.assertRaisesRegex(Exception, f'Test interrupted by signal {signum}'):
                        runner.run()
                sleep.assert_not_called()
                docker.assert_called_once_with('POST', '/containers/test/stop?t=10')
                runner.recover.assert_called_once_with()

    def test_temporary_observation_error_is_still_retried(self):
        runner = self.runner()
        condition = Mock(side_effect=[RuntimeError('temporary observation error'), True])
        with patch.object(chaos.time, 'sleep') as sleep:
            chaos.ChaosTest.wait(runner, 'observation recovers', condition)
        self.assertEqual(condition.call_count, 2)
        sleep.assert_called_once_with(2)

    def test_success_after_deadline_is_rejected(self):
        runner = self.runner()
        with patch.object(chaos.time, 'monotonic', side_effect=[0, 0, 31]), \
                patch('builtins.print') as output:
            with self.assertRaisesRegex(RuntimeError, 'timed out after 30s'):
                chaos.ChaosTest.wait(runner, 'late success', lambda: True)
        output.assert_not_called()

    def test_deadline_interrupts_blocking_condition(self):
        runner = self.runner()
        runner.timeout = 0.05
        started = time.monotonic()
        with self.assertRaisesRegex(RuntimeError, 'timed out'):
            chaos.ChaosTest.wait(runner, 'blocked response', lambda: time.sleep(5))
        self.assertLess(time.monotonic() - started, 1)

    def test_retry_sleep_does_not_exceed_remaining_time(self):
        runner = self.runner()
        with patch.object(chaos.time, 'monotonic', side_effect=[0, 0, 29.5, 30]), \
                patch.object(chaos.time, 'sleep') as sleep:
            with self.assertRaisesRegex(RuntimeError, 'timed out after 30s'):
                chaos.ChaosTest.wait(runner, 'not ready', lambda: False)
        sleep.assert_called_once_with(0.5)

    def test_requests_use_remaining_phase_time(self):
        runner = self.runner()
        runner.auth = 'test auth'
        runner._phase_deadline = 30
        with patch.object(chaos.time, 'monotonic', return_value=29), \
                patch.object(chaos.urllib.request, 'urlopen') as urlopen, \
                patch.object(chaos.json, 'load', return_value={}), \
                patch.object(chaos, 'docker') as docker:
            chaos.ChaosTest.grafana(runner, '/api/health')
            chaos.ChaosTest.inspect(runner)
        self.assertEqual(urlopen.call_args.kwargs['timeout'], 1)
        docker.assert_called_once_with('GET', '/containers/test/json', timeout=1)


if __name__ == '__main__':
    unittest.main()
