"""Drives a compiled helper through its HTTP API against the throwaway home from fixtures.py.

    python3 scripts/e2e/drive.py <E> <helper> setup on|off [--offline]   scan, review, apply, wait
    python3 scripts/e2e/drive.py <E> <helper> show [--offline]           maintenance: show in app

"on" turns on "Show these conversations in the Copilot desktop app". --offline (macOS) runs the
helper, and every Copilot and Codex process it starts, under sandbox-exec with IP traffic allowed
only to localhost. Results go to <E>/runs/<n>-<action>. On Windows a stand-in powershell.exe
answers setup's user-PATH registration so the registry is not changed, and netmon.ps1 samples
the connections of the run's Copilot processes. Run from the repository root."""
import json, os, re, subprocess, sys, time, urllib.error, urllib.request
from datetime import datetime, timezone

E, helper, action = os.path.abspath(sys.argv[1]), os.path.abspath(sys.argv[2]), sys.argv[3]
show_in_app = action == "setup" and sys.argv[4] == "on"
offline = "--offline" in sys.argv
WINDOWS = os.name == "nt"
H = os.path.join(E, "home")
runs = os.path.join(E, "runs")
os.makedirs(runs, exist_ok=True)
out = os.path.join(runs, f"{len(os.listdir(runs)) + 1}-{action}" + (f"-{sys.argv[4]}" if action == "setup" else ""))
os.makedirs(out)

env = {k: v for k, v in os.environ.items() if k.upper() not in ("CLAUDE_CONFIG_DIR", "COPILOT_HOME", "CODEX_HOME", "GH_TOKEN", "GITHUB_TOKEN", "COPILOT_GITHUB_TOKEN")}
env.update(HOME=H, COPILOT_HOME=os.path.join(H, ".copilot"), CODEX_HOME=os.path.join(H, ".codex"))
command = [helper, "--home", H, "--root", os.path.join(H, ".copilot-ready"), "--no-browser"]
if WINDOWS:
    shim = os.path.join(E, "shim")
    subprocess.run(["go", "build", "-o", os.path.join(shim, "powershell.exe"), "./scripts/e2e/powershell-shim"], check=True)
    env.update(USERPROFILE=H, APPDATA=os.path.join(H, "AppData", "Roaming"), PATH=shim + os.pathsep + env["PATH"], SHIM_LOG=os.path.join(out, "shim.log"))
if offline:
    if sys.platform != "darwin":
        sys.exit("--offline needs macOS sandbox-exec")
    profile = '(version 1)(allow default)(deny network-outbound (remote ip "*:*"))(allow network-outbound (remote ip "localhost:*"))'
    command = ["sandbox-exec", "-p", profile, *command]

now = lambda: datetime.now(timezone.utc).isoformat()
proc = subprocess.Popen(command, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
url = None
while url is None:
    line = proc.stdout.readline()
    if not line and proc.poll() is not None:
        sys.exit("helper exited before printing its link")
    m = re.search(r"(http://127\.0\.0\.1:\d+)", line)
    url = m and m.group(1)
token = re.search(r'name="setup-token" content="([0-9a-f]+)"', urllib.request.urlopen(url + "/").read().decode()).group(1)


def api(path, data=None, timeout=3600):
    req = urllib.request.Request(url + "/api/" + path, data=None if data is None else json.dumps(data).encode(), method="GET" if data is None else "POST",
                                 headers={"X-Setup-Token": token, "Content-Type": "application/json", "Origin": url})
    try:
        return json.loads(urllib.request.urlopen(req, timeout=timeout).read())
    except urllib.error.HTTPError as e:
        proc.terminate()
        sys.exit(f"{path}: {e.code} {e.read().decode()}")


monitor = None
if WINDOWS:
    monitor = subprocess.Popen([r"C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe", "-NoProfile", "-File", os.path.join(os.path.dirname(__file__), "netmon.ps1"), E, os.path.join(out, "net.log")])
timeline = [(now(), "start")]
if action == "setup":
    opts = {"mode": "existing", "project": os.path.join(E, "work", "repo-a"), "sandbox": "preserve", "gitAuth": False, "baseline": False}
    report = api("scan", opts)
    json.dump(report, open(os.path.join(out, "report.json"), "w"), indent=1)
    ids = [i["id"] for i in report["items"] if i.get("selectable") and not i.get("conflict")]
    preview = api("preview", {"ids": ids, "overwrites": []})
    json.dump(preview, open(os.path.join(out, "preview.json"), "w"), indent=1)
    timeline.append((now(), "apply"))
    json.dump(api("apply", {"planId": preview["planId"], "options": opts, "ids": ids, "overwrites": [], "showInApp": show_in_app}), open(os.path.join(out, "apply.json"), "w"), indent=1)
    timeline.append((now(), "applied"))
    print("selected:", [(i["kind"], i["name"]) for i in report["items"] if i["id"] in ids])
    print("left for review:", [(i["kind"], i["name"]) for i in report["items"] if i["id"] not in ids and i.get("disposition") != "native"])
elif action == "show":
    timeline.append((now(), "phase=show"))
    print(api("maintenance", {"action": "show", "confirm": True, "id": ""})["message"])
last = None
while True:
    s = api("state")
    p = s.get("imports") or {}
    key = (p.get("phase"), p.get("done"), p.get("showDone"))
    if key != last and p:
        timeline.append((now(), f"phase={p.get('phase')} done={p.get('done')}/{p.get('total')} show={p.get('showDone')}/{p.get('showTotal')}"))
        last = key
    if not p.get("running"):
        break
    time.sleep(0.3)
timeline.append((now(), "finished"))
json.dump(s, open(os.path.join(out, "state.json"), "w"), indent=1)
json.dump(timeline, open(os.path.join(out, "timeline.json"), "w"), indent=1)
if monitor:
    open(os.path.join(out, "net.log.stop"), "w").close()
    monitor.wait(timeout=30)
proc.terminate()
print("imports:", json.dumps(s.get("imports")))
for c in (s.get("result") or {}).get("validation", []):
    print(f"check: {c['name']}: {c['status']}")
for t, what in timeline:
    print(t, what)
print("results in", out)
