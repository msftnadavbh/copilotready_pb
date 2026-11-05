"""Builds a throwaway e2e home for "Show these conversations in the Copilot desktop app".

    python3 scripts/e2e/fixtures.py <E>

Everything is synthetic and lives under <E>: Claude Code and Codex conversations (a GitHub
repository, a folder that is not a repository, a 140-turn history, and a Codex thread), a Claude
hook and MCP server, a local-marketplace plugin with hooks and its own MCP server, and a Copilot
profile that already has a hook and a model. Every hook and server appends to a file in
<E>/markers, so a check can tell what ran. The managed runtime is seeded with Copilot CLI 1.0.105
so setup downloads nothing and can run with the network blocked."""
import hashlib, json, os, platform, shutil, subprocess, sys, tarfile, urllib.request, uuid
from datetime import datetime

E = os.path.abspath(sys.argv[1])
H, M, W = (os.path.join(E, d) for d in ("home", "markers", "work"))
WINDOWS = os.name == "nt"
if os.path.exists(E):
    sys.exit(f"{E} exists; choose a new folder")
for d in (H, M, W):
    os.makedirs(d)


def put(path, data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        f.write(data if isinstance(data, str) else json.dumps(data, indent=2))


def marker(name, label, when=True):
    """A hook command in both shells. MCP servers use when=False: discovery flags $(...)."""
    p = os.path.join(M, name)
    value = f"\"{label} $(Get-Date -Format o)\"" if when else label
    return {"bash": f"echo \"{label} $(date -u +%FT%TZ)\" >> '{p}'" if when else f"echo {label} >> '{p}'",
            "powershell": f"Add-Content -Path '{p}' -Value {value}"}


def server(name, label):
    m = marker(name, label, False)
    if WINDOWS:
        return {"command": "powershell.exe", "args": ["-NoProfile", "-Command", m["powershell"]]}
    return {"command": "sh", "args": ["-c", m["bash"]]}


def stamp(path, iso):
    t = datetime.fromisoformat(iso.replace("Z", "+00:00")).timestamp()
    os.utime(path, (t, t))


for name, slug in {"repo-a": "octo-org/demo", "repo-b": "octo-org/tools"}.items():
    d = os.path.join(W, name)
    os.makedirs(d)
    for args in (["init", "-q", "-b", "main"], ["remote", "add", "origin", f"https://github.com/{slug}.git"],
                 ["-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"]):
        subprocess.run(["git", *args], cwd=d, check=True)
os.makedirs(os.path.join(W, "plain"))
repo_a, repo_b, plain = (os.path.join(W, d) for d in ("repo-a", "repo-b", "plain"))


def claude(name, cwd, branch, turns, end):
    sid = str(uuid.uuid5(uuid.NAMESPACE_URL, name))
    lines, parent = [], None
    for i, (user, tool, answer) in enumerate(turns):
        ts = f"2026-08-{10 + i % 15:02d}T10:00:00Z"
        base = {"sessionId": sid, "cwd": cwd, "timestamp": ts}
        u = str(uuid.uuid4())
        lines.append({"type": "user", "uuid": u, "parentUuid": parent, "gitBranch": branch, **base, "message": {"role": "user", "content": user}})
        parent = u
        if tool:
            a, r, call = str(uuid.uuid4()), str(uuid.uuid4()), f"toolu_{i}"
            lines.append({"type": "assistant", "uuid": a, "parentUuid": parent, **base, "message": {"id": f"msg_{i}a", "role": "assistant", "model": "claude-fixture", "content": [{"type": "tool_use", "id": call, "name": "Bash", "input": {"command": "go test ./..."}}]}})
            lines.append({"type": "user", "uuid": r, "parentUuid": a, **base, "message": {"role": "user", "content": [{"type": "tool_result", "tool_use_id": call, "content": tool}]}})
            parent = r
        b = str(uuid.uuid4())
        lines.append({"type": "assistant", "uuid": b, "parentUuid": parent, **base, "message": {"id": f"msg_{i}b", "role": "assistant", "model": "claude-fixture", "content": [{"type": "text", "text": answer}]}})
        parent = b
    p = os.path.join(H, ".claude", "projects", "fixture-" + name, sid + ".jsonl")
    put(p, "\n".join(json.dumps(l) for l in lines) + "\n")
    stamp(p, end)


claude("login", repo_a, "main", [("CLAUDE-A-FIRST fix the login bug", "ok  pkg/auth 0.2s", "CLAUDE-A-ANSWER fixed the token check")], "2026-08-10T10:05:00Z")
claude("notes", plain, "", [("CLAUDE-B-FIRST draft the release notes", None, "CLAUDE-B-ANSWER here is a draft")], "2026-08-12T15:30:00Z")
filler = "please review this part of the change carefully " * 20
claude("big", repo_a, "main", [(f"CLAUDE-BIG-{i} {filler}", "PASS " * 400, f"CLAUDE-BIG-ANSWER-{i} " + "done " * 60) for i in range(140)], "2026-08-20T18:00:00Z")

market = os.path.join(E, "markets", "team")
plugin = os.path.join(market, "plugins", "helper")
put(os.path.join(market, ".claude-plugin", "marketplace.json"), {"name": "team", "owner": {"name": "Fixture"}, "plugins": [{"name": "helper", "source": "./plugins/helper", "description": "Fixture plugin with hooks and an MCP server"}]})
put(os.path.join(plugin, ".claude-plugin", "plugin.json"), {"name": "helper", "version": "1.0.0", "description": "Fixture plugin with hooks and an MCP server"})
plugin_hook = {"type": "command", **marker("plugin-hook.txt", "plugin-hook")}
put(os.path.join(plugin, "hooks", "hooks.json"), {"version": 1, "hooks": {"sessionStart": [plugin_hook], "userPromptSubmitted": [plugin_hook], "sessionEnd": [plugin_hook]}})
put(os.path.join(plugin, ".mcp.json"), {"mcpServers": {"helper-mcp": server("plugin-mcp.txt", "plugin-mcp")}})
put(os.path.join(H, ".claude", "plugins", "known_marketplaces.json"), {"team": {"source": {"source": "directory", "path": market.replace("\\", "/")}}})
put(os.path.join(H, ".claude", "plugins", "installed_plugins.json"), {"version": 2, "plugins": {"helper@team": [{}]}})
# Windows leaves Claude hooks for review; macOS imports this one, so its ordering is tested there.
put(os.path.join(H, ".claude", "settings.json"), {"enabledPlugins": {"helper@team": True}, "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": marker("claude-hook.txt", "claude-SessionStart", False)["bash"]}]}],
    "Stop": [{"hooks": [{"type": "command", "command": marker("claude-hook.txt", "claude-Stop", False)["bash"]}]}]}})
put(os.path.join(H, ".claude.json"), {"mcpServers": {"fixture-local": {"type": "stdio", **server("user-mcp.txt", "user-mcp")}}})

tid = "0199f0aa-1111-7222-8333-944455556666"
ts = lambda s: f"2026-09-02T09:00:{s:02d}.000Z"
rollout = os.path.join(H, ".codex", "sessions", "2026", "09", "02", f"rollout-2026-09-02T09-00-00-{tid}.jsonl")
put(rollout, "\n".join(json.dumps(l) for l in [
    {"timestamp": ts(0), "type": "session_meta", "payload": {"id": tid, "timestamp": ts(0), "cwd": repo_b, "originator": "codex_cli_rs", "cli_version": "0.146.0", "instructions": None, "source": "cli", "model_provider": "openai", "git": {"branch": "main", "repository_url": "https://github.com/octo-org/tools.git"}}},
    {"timestamp": ts(1), "type": "turn_context", "payload": {"cwd": repo_b, "approval_policy": "on-request", "sandbox_policy": {"type": "workspace-write"}, "model": "gpt-5.4", "summary": "auto"}},
    {"timestamp": ts(1), "type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "CODEX-FIRST add a health check endpoint"}]}},
    {"timestamp": ts(1), "type": "event_msg", "payload": {"type": "user_message", "message": "CODEX-FIRST add a health check endpoint", "images": [], "kind": "plain"}},
    {"timestamp": ts(5), "type": "response_item", "payload": {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "CODEX-ANSWER added /healthz"}]}},
    {"timestamp": ts(5), "type": "event_msg", "payload": {"type": "agent_message", "message": "CODEX-ANSWER added /healthz"}},
]) + "\n")
stamp(rollout, ts(5))

profile = os.path.join(H, ".copilot")
put(os.path.join(profile, "settings.json"), {"model": "claude-sonnet-4.6"})
put(os.path.join(profile, "hooks", "existing.json"), {"version": 1, "hooks": {"sessionStart": [{"type": "command", **marker("existing-hook.txt", "existing-hook")}]}})

# Seed the managed runtime: the official 1.0.105 build, checked against setup's pinned digest.
arch = {"x86_64": "x64", "amd64": "x64", "arm64": "arm64", "aarch64": "arm64"}[platform.machine().lower()]
key = ("win32" if WINDOWS else "darwin") + "-" + arch
root = os.path.join(H, ".copilot-ready")
put(os.path.join(root, ".owner"), "copilot-ready-v2\n")
exe = os.path.join(root, "runtimes", "1.0.105-" + key + "-seeded", "copilot.exe" if WINDOWS else "copilot")
os.makedirs(os.path.dirname(exe))
if WINDOWS:
    # The winget package installs the official executable.
    shutil.copy2(shutil.which("copilot"), exe)
else:
    digests = {"darwin-arm64": "34aacd03fb478d47e4b3cfc5ca045c37278c5d8d86239466521a16ea54ac84aa", "darwin-x64": "de5684eeb118ad1e54f9a01f4535cf74e121b8226d26473550b01a7565ce54f5"}
    archive = os.path.join(E, "copilot.tar.gz")
    urllib.request.urlretrieve(f"https://github.com/github/copilot-cli/releases/download/v1.0.105/copilot-{key}.tar.gz", archive)
    if hashlib.sha256(open(archive, "rb").read()).hexdigest() != digests[key]:
        sys.exit("downloaded Copilot does not match setup's pinned digest")
    with tarfile.open(archive) as t:
        member = next(m for m in t.getmembers() if os.path.basename(m.name) == "copilot" and m.isfile())
        with t.extractfile(member) as src, open(exe, "wb") as dst:
            shutil.copyfileobj(src, dst)
    os.chmod(exe, 0o755)
    os.remove(archive)
version = subprocess.run([exe, "--version"], capture_output=True, text=True).stdout
if "1.0.105" not in version:
    sys.exit(f"seeded runtime is not 1.0.105: {version}")
put(os.path.join(root, "runtime-state.json"), {"current": {"version": "1.0.105", "path": exe, "sha256": hashlib.sha256(open(exe, "rb").read()).hexdigest(), "platform": key}})
print(json.dumps({"home": H, "markers": M, "repoA": repo_a, "repoB": repo_b, "plain": plain, "root": root}, indent=1))
