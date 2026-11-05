package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMCPCredentialConversion(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		config       map[string]any
		headers      map[string]any
	}{
		{"codex", "codex", map[string]any{"url": "https://example.test/mcp", "bearer_token_env_var": "READY_TEST_TOKEN", "env_http_headers": map[string]any{"X-Key": "READY_TEST_KEY"}, "http_headers": map[string]any{"Accept": "application/json"}}, map[string]any{"Authorization": "Bearer ${READY_TEST_TOKEN}", "X-Key": "${READY_TEST_KEY}", "Accept": "application/json"}},
		{"claude", "claude", map[string]any{"type": "http", "url": "https://example.test/mcp", "headers": map[string]any{"Authorization": "Bearer ${READY_TEST_TOKEN}", "X-Key": "${READY_TEST_KEY}"}}, map[string]any{"Authorization": "Bearer ${READY_TEST_TOKEN}", "X-Key": "${READY_TEST_KEY}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("READY_TEST_TOKEN", "dummy-credential-never-persist")
			t.Setenv("READY_TEST_KEY", "dummy-key-never-persist")
			got, reasons := convertMCP(tc.source, "docs", tc.config)
			if len(reasons) != 0 || !reflect.DeepEqual(got["headers"], tc.headers) {
				t.Fatalf("conversion: %#v %v", got, reasons)
			}
			if strings.Contains(string(encode(got)), "never-persist") {
				t.Fatal("resolved credential persisted")
			}
		})
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, reasons := convertMCP("codex", "local", map[string]any{"command": exe, "env_vars": []any{"READY_TEST_TOKEN", map[string]any{"name": "READY_TEST_KEY", "source": "local"}}, "env": map[string]any{"NODE_ENV": "production"}})
	want := map[string]any{"READY_TEST_TOKEN": "${READY_TEST_TOKEN}", "READY_TEST_KEY": "${READY_TEST_KEY}", "NODE_ENV": "production"}
	if len(reasons) != 0 || !reflect.DeepEqual(out["env"], want) {
		t.Fatalf("local forwarding: %#v %v", out, reasons)
	}
}

func TestMCPRequiresReviewInsteadOfDroppingBehaviorOrSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		extra        map[string]any
	}{
		{"literal-token", "claude", map[string]any{"headers": map[string]any{"Authorization": "Bearer opaque-private-value"}}},
		{"token-attached-to-reference", "claude", map[string]any{"headers": map[string]any{"Authorization": "Bearer opaque-private-value${SUFFIX}"}}},
		{"credential-in-safe-header", "claude", map[string]any{"headers": map[string]any{"X-Figma-Region": "opaque-private-value"}}},
		{"case-duplicate", "claude", map[string]any{"headers": map[string]any{"Authorization": "${ONE}", "authorization": "${TWO}"}}},
		{"protected-claude-variable", "claude", map[string]any{"headers": map[string]any{"Authorization": "Bearer ${ANTHROPIC_API_KEY}"}}},
		{"claude-fallback", "claude", map[string]any{"headers": map[string]any{"Authorization": "${TOKEN:-opaque-private-value}"}}},
		{"claude-dynamic-auth", "claude", map[string]any{"headersHelper": "do-not-execute"}},
		{"claude-oauth-metadata", "claude", map[string]any{"oauth": map[string]any{"clientId": "custom-client"}}},
		{"codex-literal-substitution", "codex", map[string]any{"http_headers": map[string]any{"Authorization": "${LITERAL}"}}},
		{"codex-chatgpt-auth", "codex", map[string]any{"auth": "chatgpt"}},
		{"codex-disabled", "codex", map[string]any{"enabled": false}},
		{"codex-tools", "codex", map[string]any{"enabled_tools": []any{"read"}}},
		{"source-timeout", "codex", map[string]any{"tool_timeout_sec": 60}},
		{"wrong-type", "claude", map[string]any{"type": 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{"url": "https://example.test/mcp"}
			for k, v := range tc.extra {
				cfg[k] = v
			}
			out, reasons := convertMCP(tc.source, "fixture", cfg)
			if len(reasons) == 0 {
				t.Fatalf("unsafe/unsupported config accepted: %#v", out)
			}
			if strings.Contains(string(encode(out)), "opaque-private-value") {
				t.Fatal("literal credential entered preview")
			}
		})
	}
	exe, _ := os.Executable()
	for _, value := range []string{"opaque-private-value", "opaque-private-value${SUFFIX}"} {
		out, reasons := convertMCP("claude", "fixture", map[string]any{"command": exe, "env": map[string]any{"NODE_ENV": value}})
		if len(reasons) == 0 || strings.Contains(string(encode(out)), "opaque-private-value") {
			t.Fatal("literal env credential accepted")
		}
	}
}

func TestMCPDiscoveryRespectsClaudeConfigHomeAndProjectScope(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "custom"}[custom], func(t *testing.T) {
			o := fixture(t)
			o.Project = filepath.Join(o.Home, "repo", "sub")
			put(t, filepath.Join(o.Home, "repo", ".git", "HEAD"), "fixture")
			path := filepath.Join(o.Home, ".claude.json")
			if custom {
				o.ClaudeHome = filepath.Join(o.Home, "custom-claude")
				path = filepath.Join(o.ClaudeHome, ".claude.json")
				put(t, filepath.Join(o.Home, ".claude.json"), `{"mcpServers":{"wrong-home":{"url":"https://wrong.example/mcp"}}}`)
			}
			data := map[string]any{"mcpServers": map[string]any{"docs": map[string]any{"url": "https://example.test/mcp"}}, "projects": map[string]any{filepath.Join(o.Home, "repo"): map[string]any{"mcpServers": map[string]any{"project-only": map[string]any{"url": "https://example.test/project"}}}}}
			put(t, path, string(encode(data)))
			credential := filepath.Join(o.Home, ".codex", "auth.json")
			put(t, credential, `{"access_token":"fixture-secret-stays-local"}`)
			before, _ := os.ReadFile(credential)
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			i := byName(t, r, "mcp", "docs")
			if !i.Selectable {
				t.Fatal("remote server not offered")
			}
			scoped := byName(t, r, "mcp", "project-only")
			if scoped.Selectable {
				t.Fatal("project config promoted to personal scope")
			}
			for _, item := range r.Items {
				if item.Name == "wrong-home" {
					t.Fatal("read unrelated default profile")
				}
			}
			changes, err := BuildChanges(r, []string{i.ID}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range changes {
				if strings.Contains(string(change.Content), "fixture-secret") {
					t.Fatal("source auth copied")
				}
			}
			after, _ := os.ReadFile(credential)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("source credentials changed")
			}
		})
	}
}

func TestMCPClaudeRestrictionsAndCurrentConfigSnapshot(t *testing.T) {
	for _, setting := range []string{`{"permissions":{"deny":["mcp__docs__read"]}}`, `{"disabledMcpjsonServers":["docs"]}`} {
		o := fixture(t)
		o.Project = filepath.Join(o.Home, "repo", "sub")
		put(t, filepath.Join(o.Home, "repo", ".git", "HEAD"), "fixture")
		put(t, filepath.Join(o.Home, ".claude.json"), `{"mcpServers":{"docs":{"url":"https://example.test/mcp"}}}`)
		config := filepath.Join(o.Home, "repo", ".claude", "settings.json")
		put(t, config, setting)
		r, err := Discover(o)
		if err != nil {
			t.Fatal(err)
		}
		i := byName(t, r, "mcp", "docs")
		if i.Selectable || i.Disposition != "needs-review" {
			t.Fatal("Claude source restriction ignored")
		}
	}
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude.json"), `{"mcpServers":{"docs":{"url":"https://example.test/mcp"}}}`)
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	i := byName(t, r, "mcp", "docs")
	put(t, filepath.Join(o.Home, ".claude", "settings.json"), `{"permissions":{"deny":["mcp__docs__*"]}}`)
	if _, err = BuildChanges(r, []string{i.ID}, nil); err == nil {
		t.Fatal("new source restriction after review ignored")
	}
}

func TestMCPMissingCredentialReferenceAndNameCollisionNeedReview(t *testing.T) {
	o := fixture(t)
	t.Setenv("READY_TEST_MISSING", "")
	put(t, filepath.Join(o.Home, ".codex", "config.toml"), "[mcp_servers.docs]\nurl='https://example.test/mcp'\nbearer_token_env_var='READY_TEST_MISSING'\n")
	put(t, filepath.Join(o.Home, ".claude.json"), `{"mcpServers":{"docs":{"url":"https://example.test/other"}}}`)
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, i := range r.Items {
		if i.Kind == "mcp" && i.Name == "docs" {
			count++
			if i.Disposition != "needs-review" {
				t.Fatal("competing or missing-credential import selected by default")
			}
			if i.Source == "codex" && !strings.Contains(i.Detail, "READY_TEST_MISSING") {
				t.Fatal("missing reference not explained")
			}
		}
	}
	if count != 2 {
		t.Fatalf("found %d definitions", count)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "access_token") {
		t.Fatal("credential store included")
	}
}
