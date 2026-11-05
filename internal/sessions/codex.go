package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Codex is a client for `codex app-server`, used only for its read-only thread calls.
// thread/list is stable; thread/turns/list needs the experimental capability in Codex 0.146.
// https://learn.chatgpt.com/docs/app-server
type Codex struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	mu   sync.Mutex
	next int
}

func StartCodex(ctx context.Context, executable, codexHome string) (*Codex, error) {
	cmd := exec.CommandContext(ctx, executable, "app-server")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	c := &Codex{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<20)}
	init := map[string]any{"clientInfo": map[string]any{"name": "copilot_ready", "title": "Copilot Ready", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}
	if err = c.call("initialize", init, nil); err == nil {
		err = c.send(map[string]any{"method": "initialized"})
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("Codex did not start its app server: %w", err)
	}
	return c, nil
}

func (c *Codex) Close() {
	_ = c.in.Close()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
	}
}

func (c *Codex) send(v any) error {
	b, err := json.Marshal(v)
	if err == nil {
		_, err = c.in.Write(append(b, '\n'))
	}
	return err
}

func (c *Codex) call(method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	if err := c.send(map[string]any{"id": c.next, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		line, err := c.out.ReadBytes('\n')
		if len(line) > 0 {
			var reply struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			// Notifications and server requests carry a method; only our reply matters.
			if json.Unmarshal(line, &reply) == nil && reply.Method == "" && reply.ID != nil && *reply.ID == c.next {
				if reply.Error != nil {
					return fmt.Errorf("%s: %s", method, reply.Error.Message)
				}
				if result == nil {
					return nil
				}
				return json.Unmarshal(reply.Result, result)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
	}
}

type codexThread struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Preview   string `json:"preview"`
	Cwd       string `json:"cwd"`
	Ephemeral bool   `json:"ephemeral"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	GitInfo   *struct {
		Branch    string `json:"branch"`
		OriginURL string `json:"originUrl"`
	} `json:"gitInfo"`
}

// List returns active and archived top-level threads; thread/list leaves out subagent threads.
func (c *Codex) List() ([]Session, error) {
	var out []Session
	for _, archived := range []bool{false, true} {
		cursor := ""
		for {
			params := map[string]any{"limit": 100, "archived": archived}
			if cursor != "" {
				params["cursor"] = cursor
			}
			var page struct {
				Data       []codexThread `json:"data"`
				NextCursor string        `json:"nextCursor"`
			}
			if err := c.call("thread/list", params, &page); err != nil {
				return nil, err
			}
			for _, t := range page.Data {
				if t.Ephemeral {
					continue
				}
				s := Session{Source: "codex", ID: t.ID, Title: firstOf(t.Name, titleLine(codexRequest(t.Preview))), Cwd: t.Cwd, Created: unixTime(t.CreatedAt), Updated: unixTime(t.UpdatedAt)}
				if t.GitInfo != nil {
					s.Branch, s.Origin = t.GitInfo.Branch, t.GitInfo.OriginURL
				}
				out = append(out, s)
			}
			if page.NextCursor == "" || page.NextCursor == cursor {
				break
			}
			cursor = page.NextCursor
		}
	}
	return out, nil
}

// ListCodex starts the app server, lists threads and stops it.
func ListCodex(ctx context.Context, executable, codexHome string) ([]Session, error) {
	c, err := StartCodex(ctx, executable, codexHome)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.List()
}

// Load reads every turn of a thread, oldest first, with full items.
func (c *Codex) Load(s Session) (Conversation, error) {
	conv := Conversation{Session: s}
	cursor := ""
	for {
		params := map[string]any{"threadId": s.ID, "itemsView": "full", "sortDirection": "asc", "limit": 50}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data []struct {
				Items []json.RawMessage `json:"items"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.call("thread/turns/list", params, &page); err != nil {
			return Conversation{}, err
		}
		for _, turn := range page.Data {
			for _, raw := range turn.Items {
				conv.Events = append(conv.Events, codexEvents(raw)...)
			}
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return conv, nil
		}
		cursor = page.NextCursor
	}
}

type codexItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Text    string `json:"text"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Command          string `json:"command"`
	Cwd              string `json:"cwd"`
	AggregatedOutput string `json:"aggregatedOutput"`
	ExitCode         *int   `json:"exitCode"`
	Status           string `json:"status"`
	Server           string `json:"server"`
	Tool             string `json:"tool"`
	Namespace        string `json:"namespace"`
	Arguments        any    `json:"arguments"`
	Result           *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	ContentItems []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"contentItems"`
	Success *bool `json:"success"`
	Changes []struct {
		Path string `json:"path"`
		Diff string `json:"diff"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
	} `json:"changes"`
	Query string `json:"query"`
}

// codexEvents maps one thread item. Reasoning, plans, compaction, review markers and subagent
// activity are left out.
func codexEvents(raw json.RawMessage) []Event {
	var it codexItem
	if json.Unmarshal(raw, &it) != nil {
		return nil
	}
	call := func(name string, args any, result string, failed bool) []Event {
		return []Event{{Kind: "call", CallID: it.ID, Name: name, Args: args}, {Kind: "result", CallID: it.ID, Text: result, Failed: failed}}
	}
	switch it.Type {
	case "userMessage":
		var parts []string
		for _, c := range it.Content {
			switch c.Type {
			case "text":
				parts = append(parts, codexRequest(c.Text))
			case "image", "localImage":
				parts = append(parts, "[image not imported]")
			}
		}
		return []Event{{Kind: "user", Text: strings.Join(parts, "\n")}}
	case "agentMessage":
		return []Event{{Kind: "assistant", Text: it.Text}}
	case "commandExecution":
		failed := it.Status == "failed" || it.Status == "declined" || it.ExitCode != nil && *it.ExitCode != 0
		return call("shell", map[string]any{"command": it.Command, "cwd": it.Cwd}, it.AggregatedOutput, failed)
	case "mcpToolCall":
		var parts []string
		if it.Result != nil {
			for _, c := range it.Result.Content {
				if c.Type == "text" {
					parts = append(parts, c.Text)
				}
			}
		}
		text, failed := strings.Join(parts, "\n"), it.Error != nil || it.Status == "failed"
		if it.Error != nil {
			text = it.Error.Message
		}
		return call(it.Server+"."+it.Tool, it.Arguments, text, failed)
	case "dynamicToolCall":
		var parts []string
		for _, c := range it.ContentItems {
			if c.Type == "inputText" {
				parts = append(parts, c.Text)
			}
		}
		return call(strings.TrimPrefix(it.Namespace+"."+it.Tool, "."), it.Arguments, strings.Join(parts, "\n"), it.Success != nil && !*it.Success)
	case "fileChange":
		var files []any
		var diffs []string
		for _, ch := range it.Changes {
			files = append(files, map[string]any{"path": ch.Path, "kind": ch.Kind.Type})
			diffs = append(diffs, ch.Diff)
		}
		return call("apply_patch", map[string]any{"changes": files}, strings.Join(diffs, "\n"), it.Status == "failed" || it.Status == "declined")
	case "webSearch":
		return call("web_search", map[string]any{"query": it.Query}, "", false)
	}
	return nil
}

// codexRequest drops the editor context the Codex desktop app puts before a request.
func codexRequest(text string) string {
	if _, after, ok := strings.Cut(text, "## My request for Codex:"); ok {
		return strings.TrimSpace(after)
	}
	return text
}

func unixTime(v int64) time.Time {
	switch {
	case v <= 0:
		return time.Time{}
	case v > 1e12:
		return time.UnixMilli(v)
	}
	return time.Unix(v, 0)
}

var ErrNoCodex = errors.New("Codex is not installed, so its conversations cannot be read")

// CodexExecutable picks the newest Codex: the CLI on PATH or the engine bundled with the Codex
// desktop app, which is often newer. An older Codex cannot read threads that a newer one wrote.
// Versions decide; modification time only breaks ties or unreadable versions.
func CodexExecutable(home string) string {
	var candidates []string
	if p, err := exec.LookPath("codex"); err == nil {
		candidates = append(candidates, p)
	}
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		bundled, _ := filepath.Glob(filepath.Join(local, "OpenAI", "Codex", "bin", "*", "codex.exe"))
		candidates = append(candidates, bundled...)
	case "darwin":
		// Codex.app keeps its engine in a nested CodexCLI.app; older builds had it in Resources.
		for _, app := range []string{"/Applications/Codex.app", filepath.Join(home, "Applications", "Codex.app")} {
			candidates = append(candidates,
				filepath.Join(app, "Contents", "Resources", "codex-cli", "CodexCLI.app", "Contents", "MacOS", "codex"),
				filepath.Join(app, "Contents", "Resources", "codex"))
		}
	}
	best, bestVersion, bestTime := "", []int(nil), time.Time{}
	for _, c := range candidates {
		info, err := os.Stat(c)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		v := codexVersion(c)
		if order := slices.Compare(v, bestVersion); best == "" || order > 0 || order == 0 && info.ModTime().After(bestTime) {
			best, bestVersion, bestTime = c, v, info.ModTime()
		}
	}
	return best
}

// codexVersion reads "codex-cli 0.165.0-alpha.1" as [0 165 0]; nil when it cannot tell.
func codexVersion(executable string) []int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, executable, "--version").Output()
	fields := strings.Fields(string(out))
	if err != nil || len(fields) == 0 {
		return nil
	}
	core, _, _ := strings.Cut(fields[len(fields)-1], "-")
	var version []int
	for _, part := range strings.Split(core, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		version = append(version, n)
	}
	return version
}
