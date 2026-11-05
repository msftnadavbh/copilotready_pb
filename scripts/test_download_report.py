import contextlib
import datetime as dt
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location('report', Path(__file__).with_name('download_report.py'))
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)


class ReportTests(unittest.TestCase):
    def run_report(self, *args):
        output = io.StringIO()
        with patch('sys.argv', ['download_report.py', *map(str, args)]), contextlib.redirect_stdout(output):
            report.main()
        return output.getvalue()

    def test_logs_are_required_without_cloud_or_file_access(self):
        with patch('subprocess.check_output', side_effect=AssertionError('no cloud commands')), \
             patch('urllib.request.urlopen', side_effect=AssertionError('no network')), \
             patch.object(Path, 'read_bytes', side_effect=AssertionError('no implicit logs')), \
             contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as exit:
            self.run_report()
        self.assertEqual(exit.exception.code, 2)

    def test_smoke_ids_are_explicit_and_missing_files_fail(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            logs, smoke = root / 'logs.zip', root / 'checks.json'
            event = {'event': 'installer_download', 'id': 'synthetic-check',
                     'timestamp': dt.datetime.now(dt.timezone.utc).isoformat(),
                     'platform': 'windows', 'release': 'fixture', 'outcome': 'completed'}
            with zipfile.ZipFile(logs, 'w') as archive:
                archive.writestr('a.log', json.dumps(event, separators=(',', ':')))
                archive.writestr('b.log', json.dumps(event, separators=(',', ':')))
            smoke.write_text(json.dumps([event['id']]), encoding='utf-8')
            with patch('subprocess.check_output', side_effect=AssertionError('no cloud commands')), \
                 patch('urllib.request.urlopen', side_effect=AssertionError('no network')):
                self.assertIn('downloads | 1', self.run_report('--logs', logs))
                self.assertIn('deployment check | 1', self.run_report('--logs', logs, '--smoke-ids', smoke))
                with self.assertRaises(FileNotFoundError):
                    self.run_report('--logs', logs, '--smoke-ids', root / 'missing.json')


if __name__ == '__main__':
    unittest.main()
