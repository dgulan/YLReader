import unittest
from scripts.prototypes.reader_soak_result import soak_checks, valid_resource_samples


class SoakGateTest(unittest.TestCase):
    def valid(self):
        return dict(mode='reader_soak', requested_seconds=1800, elapsed_seconds=1801.2, completed=True, auth_injection=False,
                    cycles=120, offline_rounds=3, desktop_checks=5, appearance_changes=10, viewport_changes=5,
                    mobile_refreshes=1, desktop_refreshes=1, final_resume=True,
                    page_errors=[], unhandled_rejections=[], csp_violations=[])

    def test_complete_long_run(self):
        self.assertTrue(all(soak_checks(self.valid(), 1800).values()))

    def test_exit_success_and_completed_cannot_replace_elapsed_time(self):
        for value in [60, True, float('inf'), float('nan')]:
            report = self.valid(); report['elapsed_seconds'] = value
            self.assertFalse(all(soak_checks(report, 1800).values()))

    def test_missing_exercises_and_renewal_fail(self):
        for field in ['cycles', 'offline_rounds', 'desktop_checks', 'appearance_changes', 'viewport_changes', 'mobile_refreshes', 'desktop_refreshes']:
            report = self.valid(); report[field] = 0
            self.assertFalse(all(soak_checks(report, 1800).values()), field)

    def test_errors_and_incomplete_resume_fail(self):
        for field in ['page_errors', 'unhandled_rejections', 'csp_violations']:
            report = self.valid(); report[field] = ['synthetic']
            self.assertFalse(all(soak_checks(report, 1800).values()))
        report = self.valid(); report['final_resume'] = False
        self.assertFalse(all(soak_checks(report, 1800).values()))

    def test_probe_cannot_be_reported_as_long_reading(self):
        report = self.valid(); report.update(mode='reader_soak_probe', requested_seconds=60, elapsed_seconds=61, mobile_refreshes=0, auth_injection=True)
        self.assertTrue(all(soak_checks(report, 60, probe=True).values()))
        self.assertFalse(all(soak_checks(report, 1800).values()))

    def test_missing_or_malformed_report_fails(self):
        for report in [None, [], {}, {'completed': True}]:
            self.assertFalse(all(soak_checks(report, 1800).values()))

    def test_short_duration_cannot_be_renamed_as_long_mode(self):
        report = self.valid(); report.update(requested_seconds=60, elapsed_seconds=61)
        self.assertFalse(all(soak_checks(report, 60).values()))

    def test_manual_access_damage_cannot_prove_real_expiry(self):
        report = self.valid(); report['auth_injection'] = True
        self.assertFalse(all(soak_checks(report, 1800).values()))

    def sample(self, elapsed=0):
        return {'elapsed_seconds': elapsed, 'containers': [
            {'Name': name, 'MemUsage': '10MiB / 512MiB', 'CPUPerc': '1%', 'PIDs': '8'} for name in ['app', 'browser']]}

    def test_unavailable_or_incomplete_resource_rows_do_not_count(self):
        full = self.sample()
        empty = {'elapsed_seconds': 60, 'containers': []}
        missing = self.sample(120); missing['containers'].pop()
        self.assertEqual(valid_resource_samples([full, empty, missing], ['app', 'browser']), 1)

    def test_duplicate_or_malformed_resource_evidence_rejected(self):
        duplicate = self.sample(60); duplicate['containers'][1]['Name'] = 'app'
        invalid = self.sample(120); invalid['containers'][0]['MemUsage'] = None
        self.assertEqual(valid_resource_samples([self.sample(), self.sample(), duplicate, invalid], ['app', 'browser']), 1)

    def test_complete_monotonic_samples_count(self):
        self.assertEqual(valid_resource_samples([self.sample(0), self.sample(60), self.sample(120)], ['app', 'browser']), 3)
