//go:build !windows

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"copilotready/internal/migration"
)

// runAdapter runs runHook for an adapter definition with a hook payload on stdin and returns stdout.
func runAdapter(t *testing.T, a migration.Activation) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adapter.json")
	data, _ := json.Marshal(a)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = stdin, stdout }()
	_, _ = inW.WriteString(`{"session_id":"x"}`)
	inW.Close()
	err := runHook(path)
	outW.Close()
	out, _ := io.ReadAll(outR)
	return string(out), err
}

func TestHookAdapterKeepsLegacyCopilotAdaptersAndTranslatesPromptBlocks(t *testing.T) {
	// 2.0.5 wrote SessionStart adapters for hooks from another Copilot profile.
	out, err := runAdapter(t, migration.Activation{Kind: "hook", HookSource: "copilot", HookEvent: "SessionStart", HookCommand: `printf '%s' '{"additionalContext":"legacy"}'`, TimeoutSeconds: 10})
	if err != nil || !strings.Contains(out, `"additionalContext": "legacy"`) {
		t.Fatalf("legacy adapter: %q %v", out, err)
	}
	// Copilot ignores exit code 2 from prompt hooks, so the adapter must emit a block decision.
	out, err = runAdapter(t, migration.Activation{Kind: "hook", HookSource: "claude", HookEvent: "UserPromptSubmit", HookCommand: "echo 'No secrets in prompts.' >&2; exit 2"})
	if err != nil || !strings.Contains(out, `"decision": "block"`) || !strings.Contains(out, "No secrets in prompts.") {
		t.Fatalf("exit 2 prompt block: %q %v", out, err)
	}
}
