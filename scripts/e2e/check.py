"""Checks a run of drive.py: python3 scripts/e2e/check.py <E> <E>/runs/<n>-...

Reads Copilot's session index read-only and prints, for every imported conversation, its index
row, git_root, first and last messages and any compaction; then the hook and MCP markers with
the indexing window, the profile's logs, scratch folders left behind, and (Windows) sampled
connections that left loopback."""
import glob, json, os, re, sqlite3, sys
from datetime import datetime, timezone

E, out = os.path.abspath(sys.argv[1]), os.path.abspath(sys.argv[2])
H = os.path.join(E, "home")
profile, root = os.path.join(H, ".copilot"), os.path.join(H, ".copilot-ready")
timeline = json.load(open(os.path.join(out, "timeline.json")))
show = [t for t, w in timeline if w.startswith("phase=show")]
after = [t for t, w in timeline if w.startswith("phase=finish") or w == "finished"]
window = (show[0], after[0]) if show else None
print("indexing window (UTC):", window)

record = json.load(open(os.path.join(root, "imported-sessions.json")))
shown_path = os.path.join(root, "indexed-sessions.json")
shown = json.load(open(shown_path)) if os.path.exists(shown_path) else {}
ids = {cid: ext for p in record.values() for ext, cid in p.items()}
index = os.path.join(profile, "session-store.db")
rows = {}
if os.path.exists(index):
    rows = {r[0]: r for r in sqlite3.connect("file:" + index + "?mode=ro", uri=True).execute("select id, cwd, repository, branch, summary, updated_at from sessions")}
ok = True
for cid, ext in sorted(ids.items(), key=lambda x: x[1]):
    state = os.path.join(profile, "session-state", cid)
    ws = open(os.path.join(state, "workspace.yaml"), encoding="utf-8").read()
    events = [json.loads(l) for l in open(os.path.join(state, "events.jsonl"), encoding="utf-8")]
    types = [e["type"] for e in events]
    users = [e["data"].get("content", "") for e in events if e["type"] == "user.message"]
    answers = [e["data"].get("content", "") for e in events if e["type"] == "assistant.message"]
    models = [e["data"].get("selectedModel") for e in events if e["type"] == "session.resume"]
    git_root = re.search(r"^git_root: (.*)$", ws, re.M)
    row = rows.get(cid)
    compaction = [t for t in types if t.startswith("session.compaction") or t == "session.truncation"]
    print(f"\n{ext} -> {cid}")
    print(f"  index row: {bool(row)}  repository={row and row[2]}  summary={row and row[4]!r}")
    print(f"  git_root={git_root and git_root.group(1)}  recorded as shown={any(cid in p for p in shown.values())}")
    print(f"  first user message: {users[0][:70]!r}")
    print(f"  last exchange: {users[-1][:60]!r} / {answers[-1][:40]!r}")
    print(f"  compaction={compaction}  model_change={types.count('session.model_change')}  resume models={models}  events.jsonl={os.path.getsize(os.path.join(state, 'events.jsonl'))} bytes")
    good = bool(row) and not compaction and users[-1].startswith("Imported from") and answers[-1] == "Ready to continue." and bool(git_root or "plain" in ws)
    ok = ok and good
print("\nevery session indexed, with git_root where it has a repository, and no compaction:", ok and bool(rows))


def when(text):
    """The UTC time at the start of a sampled connection or a timeline entry, to the microsecond."""
    m = re.match(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d+)?(Z|[+-]\d\d:\d\d)?", text)
    return datetime.fromisoformat(m.group(1) + (m.group(2) or "")[:7] + (m.group(3) or "+00:00").replace("Z", "+00:00")).astimezone(timezone.utc)


print("\nmarkers (an imported hook or MCP marker inside the indexing window is a failure):")
for m in sorted(glob.glob(os.path.join(E, "markers", "*"))):
    lines = open(m, encoding="utf-8", errors="replace").read().split("\n")
    print(f"  {os.path.basename(m)}: {len([l for l in lines if l.strip()])} lines; {' | '.join(l.strip() for l in lines if l.strip())[:300]}")

logs = sorted(glob.glob(os.path.join(profile, "logs", "process-*.log")))
print("\nprofile logs:", len(logs))
for l in logs:
    text = open(l, encoding="utf-8", errors="replace").read()
    hits = [cid for cid in ids if cid in text]
    if hits:
        print("  INDEXING LOG LEFT:", os.path.basename(l), hits)
for name in ("conversations-*", "show-*"):
    left = glob.glob(os.path.join(root, name))
    if left:
        print("  SCRATCH LEFT:", left)

net = os.path.join(out, "net.log")
if os.path.exists(net):
    lines = open(net).read().splitlines()
    remote = [l for l in lines if not re.search(r"remote=(127\.0\.0\.1|::1|0\.0\.0\.0|::):", l)]
    inside = [l for l in remote if window and when(window[0]) <= when(l) <= when(window[1])]
    print(f"\nsampled connections: {len(lines)}; off the machine: {len(remote)}; off the machine during indexing: {len(inside)}")
    for l in remote[:10]:
        print(" ", l)
