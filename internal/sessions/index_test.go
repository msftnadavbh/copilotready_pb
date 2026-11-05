package sessions

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scripts/generate_session_fixture.py creates synthetic SQLite/WAL data from an empty database: 200
// sessions were checkpointed into the database file, then 150 more, one with a summary large
// enough for overflow pages, and the deletion of main-0007 live only in the write-ahead log.
func copyStore(t *testing.T, wal func([]byte) []byte) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"session-store.db", "session-store.db-wal"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if wal != nil && filepath.Ext(name) == ".db-wal" {
			b = wal(b)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "session-store.db")
}

func TestIndexReadsCommittedRowsFromTheWriteAheadLog(t *testing.T) {
	ids, err := IndexedSessions(copyStore(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 350 || !ids["main-0000"] || !ids["main-0199"] || !ids["wal-0149"] || !ids["wal-big"] || ids["main-0007"] {
		t.Fatalf("read %d sessions: %v %v %v", len(ids), ids["main-0000"], ids["wal-big"], ids["main-0007"])
	}
}

func TestIndexIgnoresATornOrStaleLogTail(t *testing.T) {
	const frame = 24 + 4096
	frames := func(b []byte) int { return (len(b) - 32) / frame }
	// A frame cut short by a crash, or one left from an earlier log, ends the committed log.
	torn, err := IndexedSessions(copyStore(t, func(b []byte) []byte { return b[:len(b)-100] }))
	if err != nil {
		t.Fatal(err)
	}
	if !torn["main-0007"] || !torn["wal-0000"] {
		t.Fatal("only fully committed transactions count")
	}
	stale, err := IndexedSessions(copyStore(t, func(b []byte) []byte {
		at := 32 + (frames(b)-1)*frame
		binary.BigEndian.PutUint32(b[at+8:], binary.BigEndian.Uint32(b[at+8:])+1)
		return b
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !stale["main-0007"] || !stale["wal-big"] {
		t.Fatal("a frame with another log's salt must end the log before the last commit")
	}
	// Without the log, the database file alone holds the checkpointed rows.
	path := copyStore(t, nil)
	if err := os.Remove(path + "-wal"); err != nil {
		t.Fatal(err)
	}
	if ids, err := IndexedSessions(path); err != nil || len(ids) != 200 {
		t.Fatalf("database file alone: %d, %v", len(ids), err)
	}
}

func TestIndexRefusesOtherFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-store.db")
	if err := os.WriteFile(path, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := IndexedSessions(path); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := IndexedSessions(filepath.Join(t.TempDir(), "missing.db")); !os.IsNotExist(err) {
		t.Fatalf("a missing index must say so: %v", err)
	}
}

func TestIndexRereadsWhenCopilotWritesDuringTheRead(t *testing.T) {
	path := copyStore(t, nil)
	defer func() { readFile = os.ReadFile }()
	walReads := 0
	touch := func(name string) {
		later := time.Now().Add(time.Duration(walReads) * time.Second)
		if err := os.Chtimes(name, later, later); err != nil {
			t.Fatal(err)
		}
	}
	// Copilot commits or checkpoints once, between the log and database reads.
	readFile = func(name string) ([]byte, error) {
		b, err := os.ReadFile(name)
		if strings.HasSuffix(name, "-wal") {
			if walReads++; walReads == 1 {
				touch(name)
			}
		}
		return b, err
	}
	ids, err := IndexedSessions(path)
	if err != nil || len(ids) != 350 || walReads != 2 {
		t.Fatalf("read %d sessions in %d tries: %v", len(ids), walReads, err)
	}
	// A database that never holds still is reported, not read as a mix of two states.
	readFile = func(name string) ([]byte, error) {
		b, err := os.ReadFile(name)
		if strings.HasSuffix(name, "-wal") {
			walReads++
			touch(name)
		}
		return b, err
	}
	if _, err := IndexedSessions(path); err == nil {
		t.Fatal("expected an error while the index keeps changing")
	}
}
