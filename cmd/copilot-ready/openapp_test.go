package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"copilotready/internal/sessions"
)

// openAppFixture records n shown conversations in the profile the app reads, the newest last.
func openAppFixture(t *testing.T, n int) (*app, string, *[]string) {
	home := t.TempDir()
	profile := filepath.Join(home, ".copilot")
	t.Setenv("COPILOT_HOME", "")
	a := &app{home: home, root: filepath.Join(home, ".copilot-ready")}
	if err := os.MkdirAll(a.root, 0700); err != nil {
		t.Fatal(err)
	}
	shown := a.shownRecord()
	start := time.Now().Add(-time.Duration(n) * time.Hour)
	for i := range n {
		id := fmt.Sprintf("session-%02d", i)
		events := filepath.Join(profile, "session-state", id, "events.jsonl")
		put(t, events, "{}\n")
		when := start.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(events, when, when); err != nil {
			t.Fatal(err)
		}
		if err := shown.Add(profile, id, when.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	var sent []string
	previous, pause := openURL, openPause
	openURL = func(url string) error { sent = append(sent, url); return nil }
	openPause = 0
	t.Cleanup(func() { openURL, openPause = previous, pause })
	return a, profile, &sent
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenInAppSendsNewestFirstInBatchesAndContinues(t *testing.T) {
	a, _, sent := openAppFixture(t, openBatch+5)
	msg, err := a.openInApp(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(*sent) != openBatch || (*sent)[0] != "ghapp://sessions/session-24" || (*sent)[openBatch-1] != "ghapp://sessions/session-05" {
		t.Fatalf("first batch: %v", *sent)
	}
	if !strings.Contains(msg, "choose Allow") || !strings.Contains(msg, "5 more remain") {
		t.Fatalf("message: %s", msg)
	}
	*sent = nil
	if msg, err = a.openInApp(true); err != nil || len(*sent) != 5 || (*sent)[0] != "ghapp://sessions/session-04" || strings.Contains(msg, "remain") {
		t.Fatalf("second batch: %v %v %s", err, *sent, msg)
	}
	// Once all are sent, choosing it again resends them, for any dismissed at Open session?.
	*sent = nil
	if msg, err = a.openInApp(true); err != nil || len(*sent) != openBatch || (*sent)[0] != "ghapp://sessions/session-24" || !strings.Contains(msg, "sent again") || !strings.Contains(msg, "5 more remain") {
		t.Fatalf("starting over: %v %v %s", err, *sent, msg)
	}
	*sent = nil
	if msg, err = a.openInApp(true); err != nil || len(*sent) != 5 || strings.Contains(msg, "sent again") {
		t.Fatalf("rest of the second round: %v %v %s", err, *sent, msg)
	}
}

func TestOpenInAppSkipsDeletedConversationsAndReportsAMissingApp(t *testing.T) {
	a, profile, sent := openAppFixture(t, 2)
	if err := os.RemoveAll(filepath.Join(profile, "session-state", "session-01")); err != nil {
		t.Fatal(err)
	}
	openURL = func(string) error { return errors.New("no application knows how to open the URL") }
	if _, err := a.openInApp(true); err == nil || !strings.Contains(err.Error(), "didn't open") || !strings.Contains(err.Error(), "Show Copilot CLI Session") {
		t.Fatalf("missing app: %v", err)
	}
	if sessions.OpenRecord(filepath.Join(a.root, "opened-in-app.json")).Has(profile, "session-00") {
		t.Fatal("a conversation the app never received was recorded as sent")
	}
	openURL = func(url string) error { *sent = append(*sent, url); return nil }
	if _, err := a.openInApp(true); err != nil || len(*sent) != 1 || (*sent)[0] != "ghapp://sessions/session-00" {
		t.Fatalf("after installing the app: %v %v", err, *sent)
	}
}

func TestOpenInAppExplainsASeparateProfile(t *testing.T) {
	a, _, sent := openAppFixture(t, 0)
	other := filepath.Join(a.root, "profiles", "default")
	put(t, filepath.Join(other, "session-state", "elsewhere", "events.jsonl"), "{}\n")
	if err := a.shownRecord().Add(other, "elsewhere", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.openInApp(true); err == nil || !strings.Contains(err.Error(), "separate Copilot profile") || len(*sent) != 0 {
		t.Fatalf("separate profile: %v %v", err, *sent)
	}
}

func TestOpenInAppIncludesImportsTheUserAlreadyContinued(t *testing.T) {
	a, profile, sent := openAppFixture(t, 0)
	// Continued in the CLI after import: in Copilot's index, but never given setup's turn.
	for _, name := range []string{"session-store.db", "session-store.db-wal"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "sessions", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(profile, name), string(b))
	}
	put(t, filepath.Join(profile, "session-state", "main-0001", "events.jsonl"), "{}\n")
	if err := sessions.OpenRecord(filepath.Join(a.root, "imported-sessions.json")).Add(profile, "claude:continued", "main-0001"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.openInApp(true); err != nil || len(*sent) != 1 || (*sent)[0] != "ghapp://sessions/main-0001" {
		t.Fatalf("continued import: %v %v", err, *sent)
	}
}

func TestOpenInAppReportsAStopPartwayAndKeepsProgress(t *testing.T) {
	a, profile, sent := openAppFixture(t, 3)
	openURL = func(url string) error {
		if len(*sent) == 1 {
			return errors.New("launch failed")
		}
		*sent = append(*sent, url)
		return nil
	}
	_, err := a.openInApp(true)
	if err == nil || !strings.Contains(err.Error(), "Sent 1 conversation to the Copilot app, then it stopped opening; 2 remain") {
		t.Fatalf("partway stop: %v", err)
	}
	if !sessions.OpenRecord(filepath.Join(a.root, "opened-in-app.json")).Has(profile, "session-02") {
		t.Fatal("the conversation sent before the stop was not recorded")
	}
}

func TestOpenInAppStopsWhenItCannotRecordWhatItSent(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a read-only folder needs a non-root user on a Unix system")
	}
	a, _, sent := openAppFixture(t, 2)
	if err := os.Chmod(a.root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.root, 0700) })
	if _, err := a.openInApp(true); err == nil || !strings.Contains(err.Error(), "couldn't record") || len(*sent) != 1 {
		t.Fatalf("unrecorded batch: %v %v", err, *sent)
	}
}

func TestShowDoesNotResendButOpenDoes(t *testing.T) {
	a, _, sent := openAppFixture(t, 2)
	if _, err := a.openInApp(false); err != nil || len(*sent) != 2 {
		t.Fatalf("first send: %v %v", err, *sent)
	}
	*sent = nil
	if msg, err := a.openInApp(false); err != nil || len(*sent) != 0 || !strings.Contains(msg, "choose Open in the Copilot app") {
		t.Fatalf("show after all were sent: %v %v %s", err, *sent, msg)
	}
	if msg, err := a.openInApp(true); err != nil || len(*sent) != 2 || !strings.Contains(msg, "sent again") {
		t.Fatalf("open after all were sent: %v %v %s", err, *sent, msg)
	}
}

func TestOpenInAppSkipsIDsThatAreNotSessionIDs(t *testing.T) {
	a, profile, sent := openAppFixture(t, 1)
	put(t, filepath.Join(profile, "session-state", "x&y=1", "events.jsonl"), "{}\n")
	if err := a.shownRecord().Add(profile, "x&y=1", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.openInApp(true); err != nil || len(*sent) != 1 || (*sent)[0] != "ghapp://sessions/session-00" {
		t.Fatalf("unexpected links: %v %v", err, *sent)
	}
}

func TestOpenInAppReportsAnUnreadableSessionList(t *testing.T) {
	// Only conversations continued in Copilot since need the list, so nothing can be sent.
	a, profile, sent := openAppFixture(t, 0)
	put(t, filepath.Join(profile, "session-state", "session-00", "events.jsonl"), "{}\n")
	put(t, filepath.Join(profile, "session-store.db"), "not a database")
	if err := sessions.OpenRecord(filepath.Join(a.root, "imported-sessions.json")).Add(profile, "claude:x", "session-00"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.openInApp(true); err == nil || !strings.Contains(err.Error(), "Couldn't read Copilot's session list") || len(*sent) != 0 {
		t.Fatalf("unreadable index: %v %v", err, *sent)
	}
}

func TestOpenInAppStillSendsWhatSetupAddedWhenTheListIsUnreadable(t *testing.T) {
	a, profile, sent := openAppFixture(t, 1)
	put(t, filepath.Join(profile, "session-store.db"), "not a database")
	if err := sessions.OpenRecord(filepath.Join(a.root, "imported-sessions.json")).Add(profile, "claude:y", "session-07"); err != nil {
		t.Fatal(err)
	}
	msg, err := a.openInApp(true)
	if err != nil || len(*sent) != 1 || (*sent)[0] != "ghapp://sessions/session-00" || !strings.Contains(msg, "Couldn't read Copilot's session list") {
		t.Fatalf("unreadable index with conversations setup added: %q %v %v", msg, err, *sent)
	}
}

func TestOpenInAppOnWindowsSendsEvenWithoutARegisteredHandler(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the note is for Windows")
	}
	a, _, sent := openAppFixture(t, 1)
	previous := ghappRegistered
	ghappRegistered = func() bool { return false }
	t.Cleanup(func() { ghappRegistered = previous })
	if msg, err := a.openInApp(true); err != nil || len(*sent) != 1 || !strings.Contains(msg, "Windows doesn't list the Copilot app") {
		t.Fatalf("%q %v %v", msg, err, *sent)
	}
}

func TestOpenInAppReportsASessionListItCannotReach(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a folder that can't be searched needs a non-root user on a Unix system")
	}
	a, profile, sent := openAppFixture(t, 1)
	if err := sessions.OpenRecord(filepath.Join(a.root, "imported-sessions.json")).Add(profile, "claude:x", "session-00"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(profile, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(profile, 0700) })
	if _, err := a.openInApp(true); err == nil || !strings.Contains(err.Error(), "Couldn't read Copilot's session list") || len(*sent) != 0 {
		t.Fatalf("session list that can't be reached: %v %v", err, *sent)
	}
}

func TestOpenInAppReportsWhenItCannotStartOver(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a read-only folder needs a non-root user on a Unix system")
	}
	a, _, sent := openAppFixture(t, 1)
	if _, err := a.openInApp(true); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(a.root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.root, 0700) })
	*sent = nil
	if _, err := a.openInApp(true); err == nil || !strings.Contains(err.Error(), "Couldn't start sending the conversations again") || len(*sent) != 0 {
		t.Fatalf("start over in a read-only folder: %v %v", err, *sent)
	}
}
