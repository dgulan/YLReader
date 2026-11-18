"""Fail closed on incomplete soak evidence, independently of test stdout."""
import math


def valid_resource_samples(samples, expected_names):
    if not isinstance(samples, list) or not expected_names:
        return 0
    count, previous = 0, -1
    for sample in samples:
        if not isinstance(sample, dict):
            continue
        elapsed, rows = sample.get('elapsed_seconds'), sample.get('containers')
        if type(elapsed) not in (int, float) or not math.isfinite(elapsed) or elapsed <= previous:
            continue
        if not isinstance(rows, list) or len(rows) != len(expected_names) or not all(isinstance(row, dict) for row in rows):
            continue
        if {row.get('Name') for row in rows} != set(expected_names):
            continue
        if not all(all(isinstance(row.get(key), str) and row[key] for key in ['MemUsage', 'CPUPerc', 'PIDs']) for row in rows):
            continue
        count += 1; previous = elapsed
    return count


def soak_checks(report, seconds, probe=False):
    if not isinstance(report, dict):
        return {'soak_report_object': False}

    def count(key, minimum):
        value = report.get(key)
        return type(value) is int and value >= minimum

    elapsed = report.get('elapsed_seconds')
    return {
        'soak_duration_policy': type(seconds) is int and (seconds == 60 if probe else 1800 <= seconds <= 3600),
        'soak_auth_method': report.get('auth_injection') is probe,
        'soak_mode': report.get('mode') == ('reader_soak_probe' if probe else 'reader_soak'),
        'soak_requested_duration': report.get('requested_seconds') == seconds,
        'soak_completed': report.get('completed') is True,
        'soak_elapsed': type(elapsed) in (int, float) and math.isfinite(elapsed) and elapsed >= seconds,
        'soak_navigation': count('cycles', 3 if probe else 60),
        'soak_offline': count('offline_rounds', 3),
        'soak_observer': count('desktop_checks', 1),
        'soak_appearance': count('appearance_changes', 1),
        'soak_viewport': count('viewport_changes', 1),
        'soak_mobile_refresh': probe or count('mobile_refreshes', 1),
        'soak_desktop_refresh': count('desktop_refreshes', 1),
        'soak_final_resume': report.get('final_resume') is True,
        'soak_no_errors': all(report.get(key) == [] for key in ['page_errors', 'unhandled_rejections', 'csp_violations']),
    }
