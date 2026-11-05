package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"copilotready/internal/installer"
	"copilotready/internal/migration"
)

func TestApprovedHooksAreWrittenInTheReviewedPlan(t *testing.T) {
	home, dest := t.TempDir(), t.TempDir()
	a := &app{root: filepath.Join(home, "ready"), manager: installer.New(filepath.Join(home, "ready"), home)}
	if err := a.manager.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	hook := migration.Activation{Kind: "hook", Name: "claude-SessionStart", HookSource: "claude", HookEvent: "SessionStart", Config: map[string]any{"entries": []any{map[string]any{"matcher": "startup", "hooks": []any{map[string]any{"type": "command", "command": "echo hi"}}}}}}
	changes, err := a.hookChanges(dest, []migration.Activation{hook})
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dest, "hooks", "copilot-ready-claude-SessionStart.json")
	found := map[string]string{}
	for _, c := range changes {
		found[c.Path] = string(c.Content)
	}
	if len(changes) != 3 || !strings.Contains(found[wrapper], "--hook-adapter") || !strings.Contains(found[wrapper], "startup") {
		t.Fatalf("changes=%v", found)
	}
	// The same event from a second source file joins the first wrapper instead of colliding.
	second := hook
	second.Config = map[string]any{"entries": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo again"}}}}}
	merged, err := a.hookChanges(dest, []migration.Activation{hook, second})
	if err != nil {
		t.Fatal(err)
	}
	wrappers := 0
	for _, c := range merged {
		if c.Path == wrapper {
			wrappers++
			if !strings.Contains(string(c.Content), "startup") || strings.Count(string(c.Content), "--hook-adapter") != 2 {
				t.Fatalf("merged wrapper=%s", c.Content)
			}
		}
	}
	if wrappers != 1 {
		t.Fatalf("%d wrappers for one event", wrappers)
	}
	// A hook that is already installed is left as it is instead of failing the setup.
	if err = os.MkdirAll(filepath.Dir(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(wrapper, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if changes, err = a.hookChanges(dest, []migration.Activation{hook}); err != nil || len(changes) != 0 {
		t.Fatalf("reinstall changes=%d err=%v", len(changes), err)
	}
}

func TestApprovedPluginsInstallWithCopilotsCommand(t *testing.T) {
	dir := t.TempDir()
	copilot, script := filepath.Join(dir, "copilot"), "#!/bin/sh\necho \"$*\" >> \"$(dirname \"$0\")/calls.log\"\n[ \"$4\" = \"bad@market\" ] && { echo boom; exit 1; }\nexit 0\n"
	if runtime.GOOS == "windows" {
		copilot, script = filepath.Join(dir, "copilot.cmd"), "@echo off\r\necho %*>>\"%~dp0calls.log\"\r\nif \"%4\"==\"bad@market\" (echo boom& exit /b 1)\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(copilot, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := installPlugin(ctx, copilot, os.Environ(), dir, migration.Activation{Kind: "plugin", Specification: "good@market", MarketplaceSource: "octo/market"}); err != nil {
		t.Fatal(err)
	}
	err := installPlugin(ctx, copilot, os.Environ(), dir, migration.Activation{Kind: "plugin", Specification: "bad@market"})
	if err == nil || !strings.Contains(err.Error(), "copilot plugin install bad@market") {
		t.Fatalf("failure not explained: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	want := []string{"--no-auto-update plugin marketplace add octo/market", "--no-auto-update plugin install good@market", "--no-auto-update plugin install bad@market"}
	if got := strings.Fields(strings.ReplaceAll(string(calls), "\r", "")); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("calls=%q", calls)
	}
}
