"""Focused local check; only disposable files and localhost requests."""
import collections
import http.client
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request
import urllib.error
import zipfile

source = Path(__file__).resolve().parents[1]
with tempfile.TemporaryDirectory() as folder:
    root = Path(folder)
    shutil.copyfile(source / 'server.cjs', root / 'server.cjs')
    (root / 'downloads').mkdir()
    (root / 'downloads/manifest.json').write_text('{"releaseVersion":"fixture"}')
    (root / '404.html').write_text('Not found')
    windows = root / 'downloads/Install-Copilot-Windows.zip'
    mac = root / 'downloads/Install-Copilot-macOS.zip'
    windows.write_bytes(b'fixture')
    (root / 'downloads/Install-Copilot-Windows-ARM64.zip').write_bytes(b'fixture')
    mac.write_bytes(b'fixture')
    (root / 'downloads/helper.exe').write_bytes(b'helper')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    process = subprocess.Popen(['node', str(root / 'server.cjs')], env={**os.environ, 'PORT': str(port)}, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        base = f'http://127.0.0.1:{port}'
        for _ in range(100):
            try:
                with urllib.request.urlopen(base + '/downloads/manifest.json', timeout=1): pass
                break
            except (OSError, urllib.error.URLError): time.sleep(.05)
        for name in ['Install-Copilot-Windows.zip', 'Install-Copilot-Windows-ARM64.zip', 'Install-Copilot-macOS.zip']:
            with urllib.request.urlopen(base + '/downloads/' + name) as r:
                assert r.read() == b'fixture'
                assert r.headers['X-Download-Event-ID']
        with urllib.request.urlopen(urllib.request.Request(base + '/downloads/Install-Copilot-Windows.zip', method='HEAD')) as r:
            assert not r.headers.get('X-Download-Event-ID')
        connection = http.client.HTTPConnection('127.0.0.1', port)
        connection.request('GET', '/downloads/Install-Copilot.cmd')
        retired = connection.getresponse()
        assert retired.status == 302 and retired.getheader('Location') == '/downloads/Install-Copilot-Windows.zip'
        assert not retired.getheader('X-Download-Event-ID')
        connection.close()
        with urllib.request.urlopen(base + '/downloads/helper.exe') as r:
            assert not r.headers.get('X-Download-Event-ID')
        windows.unlink()
        try: urllib.request.urlopen(base + '/downloads/Install-Copilot-Windows.zip')
        except urllib.error.HTTPError as e: assert e.code == 404
        with mac.open('wb') as f: f.truncate(128 * 1024 * 1024)
        with socket.create_connection(('127.0.0.1', port)) as sock:
            sock.sendall(b'GET /downloads/Install-Copilot-macOS.zip HTTP/1.1\r\nHost: localhost\r\n\r\n')
            assert b'200 OK' in sock.recv(1024)
        time.sleep(.3)
        process.terminate()
        stdout, stderr = process.communicate(timeout=10)
        events = [json.loads(line) for line in stdout.decode().splitlines() if 'installer_download' in line]
        assert len(events) == 5, events
        assert len({e['id'] for e in events}) == 5
        assert collections.Counter(e['outcome'] for e in events) == {'completed': 3, 'failed': 1, 'interrupted': 1}, events
        assert all(e['release'] == 'fixture' for e in events)
        spec = importlib.util.spec_from_file_location('report', source / 'scripts/download_report.py')
        report = importlib.util.module_from_spec(spec); spec.loader.exec_module(report)
        zipped = io.BytesIO()
        with zipfile.ZipFile(zipped, 'w') as z:
            z.writestr('a.log', stdout); z.writestr('b.log', stdout)
        assert len([e for e in report.events_from_zip(zipped.getvalue()) if e['event'] == 'installer_download']) == 5
        # A failed logging sink must not prevent a successful response.
        broken = root / 'broken-stdout.cjs'
        broken.write_text("process.stdout.write = () => { throw Error('fixture logging failure'); };")
        process = subprocess.Popen(['node', '--require', str(broken), str(root / 'server.cjs')], env={**os.environ, 'PORT': str(port)}, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        mac.write_bytes(b'fixture')
        for _ in range(100):
            try:
                with urllib.request.urlopen(base + '/downloads/Install-Copilot-macOS.zip', timeout=1) as r:
                    assert r.read() == b'fixture'
                break
            except (OSError, urllib.error.URLError): time.sleep(.05)
        else: raise AssertionError('Logging failure blocked download')
        print('PASS: both platforms, both Windows processors, retired .cmd redirect, HEAD/helper exclusion, failure, interruption, one event per request, report deduplication, logging failure isolation.')
    finally:
        if process.poll() is None:
            process.terminate(); process.communicate(timeout=10)
