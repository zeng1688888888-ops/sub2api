import unittest
import subprocess
import sys
import tempfile
import json
from pathlib import Path
from check_pnpm_audit_exceptions import validate_audit_result

class AuditValidityTests(unittest.TestCase):
    def test_accepts_clean_and_populated_reports(self):
        for report in ({"advisories": {}}, {"vulnerabilities": {}}, {"advisories": {"1": {"severity": "high"}}}):
            validate_audit_result(report)

    def test_network_errors_and_unknown_shapes_fail_closed(self):
        for report in ({"error": {"code": "ECONNRESET"}}, {}, [], {"advisories": []}, {"error": "failed", "advisories": {}}):
            with self.subTest(report=report), self.assertRaises(ValueError):
                validate_audit_result(report)

    def test_cli_rejects_error_report_before_loading_exceptions(self):
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / "audit.json"
            report.write_text(json.dumps({"error": {"code": "ECONNRESET"}}), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(Path(__file__).with_name("check_pnpm_audit_exceptions.py")),
                 "--audit", str(report), "--exceptions", str(Path(directory) / "missing.yml")],
                capture_output=True, text=True,
            )
            self.assertEqual(result.returncode, 1)
            self.assertIn("valid report", result.stderr)
            self.assertNotIn("Traceback", result.stderr)

if __name__ == '__main__':
    unittest.main()
