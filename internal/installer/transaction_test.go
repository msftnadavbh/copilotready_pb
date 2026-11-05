package installer

import (
	"copilotready/internal/core"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPreservesConflictAndBacksUpSuccess(t *testing.T) {
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	profile := filepath.Join(home, "profile")
	os.MkdirAll(profile, 0700)
	path := filepath.Join(profile, "settings.json")
	os.WriteFile(path, []byte("old"), 0600)
	before, _ := Snapshot(path)
	tx, err := m.Apply([]core.Change{{Path: path, Content: []byte("new"), BeforeSHA256: before, Mode: 0600}}, []string{profile})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatal(string(got))
	}
	if len(tx.Entries) != 1 {
		t.Fatal("missing journal")
	}
	old, _ := os.ReadFile(tx.Entries[0].Backup)
	if string(old) != "old" {
		t.Fatalf("backup %q", old)
	}
	_, err = m.Apply([]core.Change{{Path: path, Content: []byte("overwrite"), BeforeSHA256: before}}, []string{profile})
	if err == nil {
		t.Fatal("expected stale review conflict")
	}
	got, _ = os.ReadFile(path)
	if string(got) != "new" {
		t.Fatal("conflict changed file")
	}
}

func TestApplyRejectsOutsideAndLinkedPaths(t *testing.T) {
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	profile := filepath.Join(home, "profile")
	os.MkdirAll(profile, 0700)
	outside := filepath.Join(home, "outside")
	_, err := m.Apply([]core.Change{{Path: outside, Content: []byte("bad"), BeforeSHA256: "absent"}}, []string{profile})
	if err == nil {
		t.Fatal("escaped allowed roots")
	}
	if _, err = os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("outside file written")
	}
	linked := filepath.Join(profile, "link")
	if err = os.Symlink(home, linked); err != nil {
		t.Skip("host cannot create symlinks")
	}
	_, err = m.Apply([]core.Change{{Path: filepath.Join(linked, "escape"), Content: []byte("bad"), BeforeSHA256: "absent"}}, []string{profile})
	if err == nil {
		t.Fatal("followed symlink")
	}
}

func TestRecoverRestoresOnlyOurWrittenContent(t *testing.T) {
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	profile := filepath.Join(home, "profile")
	os.MkdirAll(profile, 0700)
	path := filepath.Join(profile, "a")
	os.WriteFile(path, []byte("old"), 0600)
	before, _ := Snapshot(path)
	tx, err := m.Apply([]core.Change{{Path: path, Content: []byte("new"), BeforeSHA256: before, Mode: 0600}}, []string{profile})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("user edit"), 0600)
	if err = m.Recover(tx.ID); err == nil {
		t.Fatal("rollback overwrote user edit")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "user edit" {
		t.Fatal(string(got))
	}
	os.WriteFile(path, []byte("new"), 0600)
	if err = m.Recover(tx.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "old" {
		t.Fatal(string(got))
	}
}

func TestLockReleasedAndForeignRootRefused(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "kit")
	m := New(root, home)
	release, err := m.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if second, err := m.Acquire(); err == nil {
		second()
		t.Fatal("concurrent lock accepted")
	}
	release()
	again, err := m.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	again()
	foreign := filepath.Join(home, "foreign")
	os.Mkdir(foreign, 0700)
	os.WriteFile(filepath.Join(foreign, "mine"), []byte("user"), 0600)
	if err = New(foreign, home).EnsureRoot(); err == nil {
		t.Fatal("adopted foreign root")
	}
}

func TestUndoRestoresTheSetupAndLaterActivations(t *testing.T) {
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	if err := m.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(home, "profile")
	os.MkdirAll(profile, 0700)
	path, created, hook := filepath.Join(profile, "settings.json"), filepath.Join(profile, "skills.md"), filepath.Join(profile, "hooks", "start.json")
	os.WriteFile(path, []byte("old"), 0600)
	before, _ := Snapshot(path)
	setup, err := m.Apply([]core.Change{{Path: path, Content: []byte("new"), BeforeSHA256: before, Mode: 0600}, {Path: created, Content: []byte("added"), BeforeSHA256: "absent", Mode: 0600}}, []string{profile})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.MarkSetup(setup.ID); err != nil {
		t.Fatal(err)
	}
	// A later activation is its own transaction; Undo must not stop at it.
	if _, err = m.Apply([]core.Change{{Path: hook, Content: []byte("{}"), BeforeSHA256: "absent", Mode: 0600}}, []string{profile}); err != nil {
		t.Fatal(err)
	}
	if s, err := m.Status(); err != nil || s.Setup != setup.ID || len(s.Transactions) != 0 {
		t.Fatalf("status=%+v err=%v", s, err)
	}
	if err = m.UndoSetup(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("settings=%q", got)
	}
	for _, p := range []string{created, hook} {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s was not removed", p)
		}
	}
	if s, _ := m.Status(); s.Setup != "" || m.UndoSetup() == nil {
		t.Fatal("undone setup still offered")
	}
}
