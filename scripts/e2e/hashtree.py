"""Hashes every file of the fixture Claude Code and Codex data.

    python3 scripts/e2e/hashtree.py <E>/home <out.json>              record
    python3 scripts/e2e/hashtree.py <E>/home <out.json> <before.json> compare"""
import hashlib, json, os, sys
home, out = sys.argv[1], sys.argv[2]
result = {}
for top in (".claude", ".claude.json", ".codex"):
    p = os.path.join(home, top)
    paths = [p] if os.path.isfile(p) else [os.path.join(d, f) for d, _, fs in os.walk(p) for f in fs]
    for f in paths:
        result[os.path.relpath(f, home)] = hashlib.sha256(open(f, "rb").read()).hexdigest()
json.dump(result, open(out, "w"), indent=1, sort_keys=True)
print(len(result), "files")
if len(sys.argv) > 3:
    before = json.load(open(sys.argv[3]))
    print("changed:", [k for k in before if before[k] != result.get(k)])
    print("removed:", [k for k in before if k not in result])
    print("added:", sorted(k for k in result if k not in before))
