"""Validation failure scenarios without changing the running platform."""
import copy
import importlib.util
import json
from pathlib import Path
import time
import unittest
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location('validator', Path(__file__).with_name('validate-platform.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def row(value, **labels):
    return {'metric': labels, 'value': [time.time(), str(value)]}


class ValidatorTests(unittest.TestCase):
    def setUp(self):
        self.validator = object.__new__(module.Validator)
        self.validator.timeout = 1
        self.validator.deadline = time.monotonic() + 1
        self.dashboard = json.loads((Path(__file__).resolve().parents[1] / 'config/grafana/dashboard.json').read_text())['dashboard']

    def test_targets_reject_replacement_even_with_six_healthy_targets(self):
        targets = [{'labels': {'job': j, 'instance': i}, 'health': 'up'}
                   for j, i in sorted(module.EXPECTED_TARGETS)]
        targets[0]['labels'] = {'job': 'unrelated', 'instance': 'other:1234'}
        self.validator.prometheus = Mock(return_value={'activeTargets': targets})
        with self.assertRaisesRegex(module.ValidationError, 'target set mismatch'):
            self.validator.check_targets()

    def metric_query(self, expression):
        value = time.time() if expression.startswith('timestamp(') else 1
        if 'container_' in expression:
            return [row(value, **{module.SERVICE_LABEL: s}) for s in module.SERVICES]
        return [row(value)]

    def test_stale_raw_samples_rejected(self):
        self.validator.query = Mock(return_value=[row(time.time() - 40)])
        with self.assertRaisesRegex(module.ValidationError, 'no older than 30'):
            self.validator.check_metrics()

    def test_database_unavailable_even_when_scrape_is_healthy(self):
        self.validator.query = Mock(side_effect=lambda e: [row(0)] if e == 'mysql_up{job="mariadb"}' else self.metric_query(e))
        with self.assertRaisesRegex(module.ValidationError, 'service unavailable.*mysql_up'):
            self.validator.check_metrics()

    def test_missing_one_container_rejected(self):
        self.validator.query = Mock(side_effect=lambda e: [row(time.time(), **{module.SERVICE_LABEL: 'mariadb'})]
                                    if 'container_cpu_' in e else self.metric_query(e))
        with self.assertRaisesRegex(module.ValidationError, 'missing container series: uptime-kuma'):
            self.validator.check_metrics()

    def test_all_saved_panel_queries_and_sources_checked(self):
        self.validator.get = Mock(return_value={'dashboard': self.dashboard})
        self.validator.query = Mock(side_effect=self.metric_query)
        self.validator.check_panels()
        self.assertEqual(self.validator.query.call_count, 13)
        expressions = [c.args[0] for c in self.validator.query.call_args_list]
        self.assertTrue(any('[1m]' in e for e in expressions))
        self.assertTrue(all('$' not in e for e in expressions))

    def test_wrong_panel_source_rejected(self):
        dashboard = copy.deepcopy(self.dashboard)
        dashboard['panels'][0]['targets'][0]['datasource']['uid'] = 'wrong'
        self.validator.get = Mock(return_value={'dashboard': dashboard})
        with self.assertRaisesRegex(module.ValidationError, 'unexpected data source'):
            self.validator.check_panels()

    def test_saved_panel_query_error_identifies_panel(self):
        self.validator.get = Mock(return_value={'dashboard': self.dashboard})
        self.validator.query = Mock(side_effect=module.ValidationError('no metric series'))
        with self.assertRaisesRegex(module.ValidationError, 'panel 1.*no metric series'):
            self.validator.check_panels()

    def test_empty_and_nonfinite_results_rejected(self):
        for rows in ([], [row('NaN')], [row('Inf')]):
            with self.subTest(rows=rows):
                self.validator.prometheus = Mock(return_value={'resultType': 'vector', 'result': rows})
                with self.assertRaises(module.ValidationError):
                    self.validator.query('some_metric')

    def test_global_deadline_interrupts_slow_check(self):
        self.validator.check = lambda: time.sleep(5)
        started = time.monotonic()
        with self.assertRaisesRegex(module.ValidationError, 'timed out after 1s'):
            self.validator.run()
        self.assertLess(time.monotonic() - started, 2)


if __name__ == '__main__':
    unittest.main()
