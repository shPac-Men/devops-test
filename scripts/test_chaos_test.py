"""Safety checks for failure cleanup; these tests do not access Docker."""
import importlib.util
from pathlib import Path
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


if __name__ == '__main__':
    unittest.main()
