#!/usr/bin/env python3
"""Regenerate the synthetic SQLite/WAL pair used by the session-index tests."""
import hashlib
from pathlib import Path
import sqlite3
import tempfile


def generate(destination):
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as folder:
        path = Path(folder) / 'session-store.db'
        db = sqlite3.connect(path)
        try:
            db.execute('PRAGMA page_size=4096')
            assert db.execute('PRAGMA journal_mode=WAL').fetchone() == ('wal',)
            db.execute('PRAGMA wal_autocheckpoint=0')
            db.execute('CREATE TABLE sessions (id TEXT PRIMARY KEY, summary TEXT)')
            main = {f'main-{i:04d}' for i in range(200)}
            db.executemany('INSERT INTO sessions VALUES (?, ?)',
                           [(id, 'Synthetic checkpointed session ' + id) for id in sorted(main)])
            db.commit()
            assert db.execute('PRAGMA wal_checkpoint(TRUNCATE)').fetchone() == (0, 0, 0)
            assert db.execute('PRAGMA page_size').fetchone() == (4096,)
            assert {row[0] for row in db.execute('SELECT id FROM sessions')} == main
            checkpointed = path.read_bytes()

            wal_ids = {f'wal-{i:04d}' for i in range(150)} | {'wal-big'}
            db.executemany('INSERT INTO sessions VALUES (?, ?)',
                           [(id, 'Synthetic WAL session ' + id) for id in sorted(wal_ids - {'wal-big'})])
            db.execute('INSERT INTO sessions VALUES (?, ?)', ('wal-big', 'Synthetic overflow summary. ' * 1000))
            db.commit()
            # Keep the deletion in a final transaction so a torn tail preserves earlier inserts.
            db.execute('DELETE FROM sessions WHERE id = ?', ('main-0007',))
            db.commit()
            expected = (main | wal_ids) - {'main-0007'}
            assert len(expected) == 350
            assert {row[0] for row in db.execute('SELECT id FROM sessions')} == expected
            assert db.execute('PRAGMA integrity_check').fetchone() == ('ok',)
            assert path.read_bytes() == checkpointed
            # Copy while the connection is quiescent but open: closing may checkpoint/remove WAL.
            (destination / path.name).write_bytes(checkpointed)
            (destination / (path.name + '-wal')).write_bytes(Path(str(path) + '-wal').read_bytes())
        finally:
            db.close()

        # Verify only disposable copies, never open/checkpoint the exported pair.
        for with_wal in (False, True):
            copy = Path(folder) / ('with-wal.db' if with_wal else 'without-wal.db')
            copy.write_bytes((destination / path.name).read_bytes())
            if with_wal:
                Path(str(copy) + '-wal').write_bytes((destination / (path.name + '-wal')).read_bytes())
            check = sqlite3.connect(copy)
            try:
                assert {row[0] for row in check.execute('SELECT id FROM sessions')} == (expected if with_wal else main)
            finally:
                check.close()


if __name__ == '__main__':
    target = Path(__file__).resolve().parents[1] / 'internal/sessions/testdata'
    generate(target)
    for name in ('session-store.db', 'session-store.db-wal'):
        print(name + ' sha256=' + hashlib.sha256((target / name).read_bytes()).hexdigest())
