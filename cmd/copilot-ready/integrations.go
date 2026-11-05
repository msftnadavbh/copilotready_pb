package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"copilotready/internal/core"
	"copilotready/internal/migration"
)

// hookChanges installs approved hooks in Copilot's hooks folder in their original format, as part
// of the setup transaction. Copilot honours Claude/Codex matchers, exit code 2 and permission
// decisions as written, but reads context only as {"additionalContext": ...}, so context events
// run through the output adapter. A hook that is already installed is left as it is.
func (a *app) hookChanges(dest string, hooks []migration.Activation) ([]core.Change, error) {
	var changes, wrappers []core.Change
	helper := filepath.Join(a.root, "bin", "copilot-ready")
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	for _, activation := range mergeHooks(hooks) {
		path := filepath.Join(dest, "hooks", "copilot-ready-"+activation.Name+".json")
		_, before, err := snapshot(path)
		if err != nil {
			return nil, err
		}
		if before != "absent" {
			continue
		}
		// Round-trip the reviewed entries so wrapping commands never edits the scan report.
		data, _ := json.Marshal(activation.Config["entries"])
		var entries []any
		if err := json.Unmarshal(data, &entries); err != nil || len(entries) == 0 {
			return nil, fmt.Errorf("The %s hook has no reviewed entries", activation.HookEvent)
		}
		if migration.ContextHookEvent(activation.HookSource, activation.HookEvent) {
			for n, h := range migration.HookHandlers(entries) {
				key := "command"
				if _, ok := h[key].(string); !ok {
					key = "bash"
				}
				original, _ := h[key].(string)
				if kind, _ := h["type"].(string); kind != "" && kind != "command" {
					continue
				}
				if original == "" {
					return nil, fmt.Errorf("The %s hook's context output can be translated only for shell commands. Deselect it or scan again", activation.HookEvent)
				}
				adapter := activation
				adapter.Config, adapter.HookCommand, adapter.TimeoutSeconds = nil, original, hookTimeout(h)
				adapterPath := filepath.Join(dest, "ready-candidates", "hooks", fmt.Sprintf("%s-%d.json", activation.Name, n))
				adapterData, _ := json.MarshalIndent(adapter, "", "  ")
				_, adapterBefore, err := snapshot(adapterPath)
				if err != nil {
					return nil, err
				}
				changes = append(changes, core.Change{Path: adapterPath, Content: adapterData, BeforeSHA256: adapterBefore, Mode: 0600, Source: "Hook output adapter"})
				h[key] = shellQuote(helper) + " --hook-adapter " + shellQuote(adapterPath)
			}
		}
		nativeData, _ := json.MarshalIndent(map[string]any{"version": 1, "hooks": map[string]any{activation.HookEvent: entries}}, "", "  ")
		wrappers = append(wrappers, core.Change{Path: path, Content: nativeData, BeforeSHA256: before, Mode: 0600, Source: activation.HookEvent + " hook"})
	}
	if len(wrappers) == 0 {
		return changes, nil
	}
	ownership, err := a.manager.PrepareHookOwnership(dest, wrappers)
	if err != nil {
		return nil, err
	}
	return append(append(changes, wrappers...), ownership), nil
}

// mergeHooks combines approved hooks for the same event from several source files, such as
// Claude's settings.json and hooks.json, so they share one wrapper instead of colliding.
func mergeHooks(hooks []migration.Activation) []migration.Activation {
	var merged []migration.Activation
	index := map[string]int{}
	for _, h := range hooks {
		data, _ := json.Marshal(h.Config["entries"])
		var entries []any
		_ = json.Unmarshal(data, &entries)
		if i, ok := index[h.Name]; ok {
			previous, _ := merged[i].Config["entries"].([]any)
			merged[i].Config = map[string]any{"entries": append(previous, entries...)}
			continue
		}
		index[h.Name] = len(merged)
		h.Config = map[string]any{"entries": entries}
		merged = append(merged, h)
	}
	return merged
}

// installPlugin registers the marketplace the source app recorded, then installs the plugin with
// Copilot's own command. An already registered marketplace or installed plugin is kept as is.
func installPlugin(ctx context.Context, copilot string, env []string, project string, plugin migration.Activation) error {
	if plugin.Specification == "" || strings.HasPrefix(plugin.Specification, "-") || strings.HasPrefix(plugin.MarketplaceSource, "-") {
		return errors.New("its source needs review")
	}
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, copilot, append([]string{"--no-auto-update", "plugin"}, args...)...)
		cmd.Env, cmd.Dir = env, project
		out, err := cmd.CombinedOutput()
		return strings.ToLower(string(out)), err
	}
	if plugin.MarketplaceSource != "" {
		if out, err := run("marketplace", "add", plugin.MarketplaceSource); err != nil && !strings.Contains(out, "already") {
			return fmt.Errorf("Copilot could not add its marketplace; run copilot plugin marketplace add %s to see why", plugin.MarketplaceSource)
		}
	}
	if out, err := run("install", plugin.Specification); err != nil && !strings.Contains(out, "already installed") {
		return fmt.Errorf("Copilot could not install it; run copilot plugin install %s to see why", plugin.Specification)
	}
	return nil
}

func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
func runHook(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var a migration.Activation
	if err = json.Unmarshal(raw, &a); err != nil {
		return err
	}
	// Adapters written by 2.0.5 for hooks from another Copilot profile have HookSource "copilot"
	// and must keep running after the helper is upgraded.
	if (a.HookEvent != "SessionStart" && a.HookEvent != "UserPromptSubmit") || a.HookCommand == "" {
		return errors.New("Invalid hook adapter")
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return err
	}
	var payload map[string]any
	if err = json.Unmarshal(input, &payload); err != nil {
		return err
	}
	payload["hook_event_name"] = a.HookEvent
	input, _ = json.Marshal(payload)
	timeout := a.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "pwsh", "-NoProfile", "-Command", a.HookCommand)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/bash", "-c", a.HookCommand)
	}
	if a.WorkingDirectory != "" {
		cmd.Dir = a.WorkingDirectory
	}
	cmd.Stdin = bytes.NewReader(input)
	var output, stderr bytes.Buffer
	cmd.Stdout = &limitedBuffer{b: &output, remaining: 1 << 20}
	cmd.Stderr = &limitedBuffer{b: &stderr, remaining: 64 << 10}
	if err = cmd.Run(); err != nil {
		// Exit code 2 is the source's blocking signal. Copilot blocks a prompt only on
		// {"decision":"block"}, so translate it there; other events keep the exit code.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			if a.HookEvent == "UserPromptSubmit" && a.HookSource != "copilot" {
				_, err = os.Stdout.Write(migration.PromptBlockOutput(strings.TrimSpace(stderr.String())))
				return err
			}
			_, _ = os.Stderr.Write(stderr.Bytes())
			os.Exit(2)
		}
		return errors.New("Source hook failed or timed out; no context emitted")
	}
	mapped, err := migration.AdaptContextOutput(a.HookSource, a.HookEvent, output.Bytes())
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(mapped)
	return err
}

type limitedBuffer struct {
	b         *bytes.Buffer
	remaining int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > l.remaining {
		return 0, errors.New("Output limit exceeded")
	}
	l.remaining -= len(p)
	return l.b.Write(p)
}

// hookTimeout reads a handler's own timeout (Claude/Codex "timeout", Copilot "timeoutSec") in seconds.
func hookTimeout(h map[string]any) int {
	for _, key := range []string{"timeout", "timeoutSec"} {
		if v, ok := h[key].(float64); ok && v > 0 {
			return int(v)
		}
	}
	return 30
}
