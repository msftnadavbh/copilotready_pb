package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"

	"copilotready/internal/standin"
)

// IndexReply is the stand-in model's answer, the last message of every indexed conversation.
const IndexReply = "Ready to continue."

// Indexer adds imported conversations to Copilot's session index, the list the Copilot app
// shows. `copilot sessions import` does not index a session (Copilot CLI 1.0.105); one resumed
// turn does. That turn runs offline against a local stand-in model, so it uses no AI credits and
// nothing leaves the computer. It leaves one short exchange at the end of the conversation.
type Indexer struct {
	Copilot string   // verified runtime
	Env     []string // the profile's environment; model, network and token variables are replaced
	Profile string
	Dir     string // scratch folder for Copilot's logs, removed by the caller

	model   string
	server  *standin.Server
	mu      sync.Mutex
	servers map[string][]string // MCP servers kept off, by folder
}

// Start runs the stand-in model. Close it when indexing is done.
func (ix *Indexer) Start() error {
	server, err := standin.Start(IndexReply)
	if err != nil {
		return err
	}
	ix.server, ix.model, ix.servers = server, profileModel(ix.Profile), map[string][]string{}
	return nil
}

func (ix *Indexer) Close() {
	if ix.server != nil {
		_ = ix.server.Close()
	}
}

// profileModel is the model the profile resumes sessions with. A session keeps the model of its
// first resumed turn, so using the same one avoids a model change when the user continues it.
func profileModel(profile string) string {
	var settings struct {
		Model string `json:"model"`
	}
	for _, name := range []string{"settings.json", "config.json"} {
		if b, err := os.ReadFile(filepath.Join(profile, name)); err == nil {
			if v, err := hujson.Parse(b); err == nil {
				v.Standardize()
				if json.Unmarshal(v.Pack(), &settings) == nil && settings.Model != "" {
					return settings.Model
				}
			}
		}
	}
	return "auto"
}

// offline replaces everything that could send the turn elsewhere, or make it use an account:
// provider and model settings, proxies, OpenTelemetry exporters and GitHub tokens.
func (ix *Indexer) offline() []string {
	var env []string
	for _, entry := range ix.Env {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		switch {
		case strings.HasPrefix(key, "COPILOT_PROVIDER_"), strings.HasPrefix(key, "OTEL_"), strings.HasPrefix(key, "COPILOT_OTEL_"):
		case key == "COPILOT_MODEL", key == "COPILOT_OFFLINE", key == "COPILOT_AUTO_TIER", key == "COPILOT_GITHUB_TOKEN", key == "GH_TOKEN", key == "GITHUB_TOKEN",
			key == "HTTP_PROXY", key == "HTTPS_PROXY", key == "ALL_PROXY", key == "NO_PROXY":
		default:
			env = append(env, entry)
		}
	}
	// The prompt limit is far above any imported history, so Copilot never compacts it; a
	// compaction would make the stand-in's reply the conversation's summary.
	return append(env, "COPILOT_OFFLINE=true", "COPILOT_PROVIDER_TYPE=openai", "COPILOT_PROVIDER_BASE_URL="+ix.server.URL, "COPILOT_PROVIDER_API_KEY="+ix.server.Key,
		"COPILOT_MODEL="+ix.model, "COPILOT_PROVIDER_MAX_PROMPT_TOKENS=10000000", "COPILOT_PROVIDER_MAX_OUTPUT_TOKENS=16384")
}

// Workspace is what Copilot recorded about a session's folder.
type Workspace struct {
	Name    string `yaml:"name"`
	Cwd     string `yaml:"cwd"`
	GitRoot string `yaml:"git_root"`
	Branch  string `yaml:"branch"`
}

func ReadWorkspace(profile, id string) (Workspace, error) {
	var ws Workspace
	if id == "" || filepath.Base(id) != id {
		return ws, errors.New("invalid session id")
	}
	b, err := os.ReadFile(filepath.Join(profile, "session-state", id, "workspace.yaml"))
	if err == nil {
		err = yaml.Unmarshal(b, &ws)
	}
	return ws, err
}

var compaction = regexp.MustCompile(`"type"\s*:\s*"session\.(compaction_|truncation)`)

// Index runs the turn for one imported session, in its own folder, with every MCP server off and
// no tool allowed. The session file keeps its modification time, which orders /resume.
func (ix *Indexer) Index(ctx context.Context, id, application string) error {
	ws, err := ReadWorkspace(ix.Profile, id)
	if err != nil {
		return err
	}
	if !isDir(ws.Cwd) {
		return errors.New("its folder no longer exists")
	}
	servers, err := ix.mcpServers(ctx, ws.Cwd)
	if err != nil {
		return err
	}
	events := filepath.Join(ix.Profile, "session-state", id, "events.jsonl")
	before, err := os.Stat(events)
	if err != nil {
		return err
	}
	args := []string{"--resume=" + id, "-p", "Imported from " + application + " by Copilot Ready.", "--no-ask-user", "--disable-builtin-mcps", "--disable-client-telemetry"}
	for _, name := range servers {
		args = append(args, "--disable-mcp-server", name)
	}
	out, runErr := runCopilot(ctx, ix.Copilot, ix.Profile, ix.Dir, ws.Cwd, ix.offline(), args)
	_ = os.Chtimes(events, before.ModTime(), before.ModTime())
	if b, err := os.ReadFile(events); err == nil && int64(len(b)) >= before.Size() && compaction.Match(b[before.Size():]) {
		return errors.New("Copilot shortened the conversation while adding it")
	}
	if runErr != nil {
		return fmt.Errorf("Copilot could not add it: %s", lastLine(out, runErr))
	}
	return nil
}

// mcpServers lists the MCP servers Copilot would start in folder, from the profile, plugins and
// the folder itself, so the turn can keep every one of them off.
func (ix *Indexer) mcpServers(ctx context.Context, folder string) ([]string, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if names, ok := ix.servers[folder]; ok {
		return names, nil
	}
	out, err := runCopilot(ctx, ix.Copilot, ix.Profile, ix.Dir, folder, ix.offline(), []string{"mcp", "list", "--json"})
	var list struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	start := bytes.IndexByte(out, '{')
	if err != nil || start < 0 || json.Unmarshal(out[start:], &list) != nil || list.MCPServers == nil {
		return nil, fmt.Errorf("Copilot could not list its MCP servers: %s", lastLine(out, err))
	}
	names := make([]string, 0, len(list.MCPServers))
	for name := range list.MCPServers {
		names = append(names, name)
	}
	ix.servers[folder] = names
	return names, nil
}

// runCopilot runs the verified runtime with its log in a scratch folder instead of the profile's
// logs folder. Any log the run still writes there is removed, found by its process id.
func runCopilot(ctx context.Context, copilot, profile, scratch, folder string, env, args []string) ([]byte, error) {
	logs, err := os.MkdirTemp(scratch, "logs-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(logs)
	cmd := exec.CommandContext(ctx, copilot, append([]string{"--no-auto-update", "--log-dir", logs}, args...)...)
	cmd.Env, cmd.Dir, cmd.WaitDelay = env, folder, 5*time.Second
	profileLogs := filepath.Join(profile, "logs")
	existing := logNames(profileLogs)
	out, err := cmd.CombinedOutput()
	if cmd.Process != nil && profile != "" {
		removeProcessLogs(profileLogs, existing, cmd.Process.Pid)
	}
	return out, err
}

func logNames(dir string) map[string]bool {
	names := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		names[e.Name()] = true
	}
	return names
}

// removeProcessLogs deletes the process-<time>-<pid>.log files that appeared during one run of
// that process. Every other log, including an older one with a reused process id, stays.
func removeProcessLogs(dir string, existing map[string]bool, pid int) {
	suffix := fmt.Sprintf("-%d.log", pid)
	for name := range logNames(dir) {
		if !existing[name] && strings.HasPrefix(name, "process-") && strings.HasSuffix(name, suffix) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func lastLine(out []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if line == "" && err != nil {
		line = err.Error()
	}
	return clip(line, 300)
}
