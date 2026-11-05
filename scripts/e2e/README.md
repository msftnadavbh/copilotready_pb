# End-to-end check: showing imported conversations in the Copilot app

These scripts check **Show these conversations in the Copilot desktop app** against a compiled helper and the real Copilot CLI 1.0.105, using synthetic data. Run all commands below in a disposable VM or a dedicated OS user account with no real profiles or credentials, not in your normal account. Some commands use inherited environment settings, and Go tests auto-detect installed CLIs. `HOME` and `COPILOT_HOME` alone do **not** isolate Keychain, preferences, URL handlers, or desktop state.

These are opt-in manual checks, not evidence that this source snapshot has passed end-to-end validation. See [current validation status](../../docs/validation.md). On Windows, setup leaves Claude hooks for review; macOS checks should also cover:
- the ordering with an imported Claude hook;
- setup with the network blocked by `sandbox-exec`;
- the Copilot app itself (E4).

## Run (macOS)

Run from the repository root inside the disposable environment, with Go 1.27, Python 3, git and Codex installed. Before the initial Go checks, follow the [source-test isolation rules](../../docs/validation.md#local-checks), which exclude real CLIs from `PATH`; the later E2E commands explicitly use real runtimes:

```bash
go vet ./... && go test ./...
go build -o /tmp/copilot-ready ./cmd/copilot-ready
E=/private/tmp/cr-e2e-on                            # must not exist; /tmp is a link, and setup refuses linked paths
python3 scripts/e2e/fixtures.py $E                  # downloads the official 1.0.105 and checks setup's pinned digest
python3 scripts/e2e/hashtree.py $E/home $E/before.json
python3 scripts/e2e/drive.py $E /tmp/copilot-ready setup on --offline
python3 scripts/e2e/check.py $E $E/runs/1-setup-on
python3 scripts/e2e/hashtree.py $E/home $E/after.json $E/before.json
```

`--offline` runs setup, and every Copilot and Codex process it starts, with IP traffic allowed only to localhost. A plugin install that needs the network may fail under it; the conversations must not. `drive.py` prints what it selected and what was left for review. The Claude `SessionStart` and `Stop` hooks should be selected on macOS.

What to look for:
- **E1, E2:** every session has an index row, `git_root` when it has a repository, the exchange last and no compaction. Every log the run left is unrelated to indexing.
- **Markers:** during the indexing window, only `existing-hook.txt` gets lines. `claude-hook.txt`, `plugin-hook.txt`, `user-mcp.txt` and `plugin-mcp.txt` do not, and no file appears in `markers/` before the window ends.
- **E6:** `changed` and `removed` are empty. Codex adds its own databases to `.codex` when its app server starts.
- **E5:** run `python3 scripts/e2e/drive.py $E /tmp/copilot-ready setup on --offline` again. No Copilot turn should run, and the `events.jsonl` files should stay identical.

**E3**, continuing in the CLI with a fake model (prints the port; stop it afterwards):

```bash
python3 scripts/e2e/fakemodel.py $E/e3.log &
ID=$(grep -l CLAUDE-A-FIRST $E/home/.copilot/session-state/*/events.jsonl | head -1 | xargs dirname | xargs basename)
cd $E/work/repo-a && HOME=$E/home COPILOT_HOME=$E/home/.copilot COPILOT_OFFLINE=true \
  COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:<port>/v1 COPILOT_PROVIDER_API_KEY=e3 COPILOT_MODEL=claude-sonnet-4.6 \
  COPILOT_PROVIDER_MAX_PROMPT_TOKENS=1000000 $E/home/.copilot-ready/runtimes/*/copilot --no-auto-update --resume=$ID -p "E3 check" --no-ask-user
cat $E/e3.log   # first message is CLAUDE-A-FIRST; the import exchange is just before "E3 check"
```

This turn also runs the imported hooks and starts the MCP servers, so their markers now get lines. That shows the markers work.

## E4: the Copilot app

Run the app only inside the disposable VM or dedicated OS account. If sign-in is required, sign in yourself there and keep credentials local to that environment; do not copy tokens or include them in logs. Start the app from a terminal with the test environment below. Overriding `HOME` does not guarantee isolation from Keychain or other OS-user shared state. When finished, clean up the entire disposable environment, not just the throwaway home.

1. Make a disposable home with conversations imported but not shown:

   ```bash
   E=/private/tmp/cr-e2e-app && python3 scripts/e2e/fixtures.py $E && python3 scripts/e2e/drive.py $E /tmp/copilot-ready setup off
   ```

2. Start the app and sign in:

   ```bash
   HOME=$E/home COPILOT_HOME=$E/home/.copilot "/Applications/GitHub Copilot.app/Contents/MacOS/github"
   ```

   Skip the first-run folder screen. Set Settings → Sessions → Show Copilot CLI Session to Last 360 days (a new app profile starts with it Off, which hides every CLI session). The sidebar should show no sessions: this is the baseline.
3. With the app still open, run `PATH=<folder with a stand-in open>:$PATH python3 scripts/e2e/drive.py $E /tmp/copilot-ready show`. Record:
   - **(b)** whether the four conversations appear without a restart;
   - **(a)** whether they appear with no repository added in the app.
4. Add `$E/work/repo-a` and `$E/work/repo-b` in the app with File → Create from Local Folder or Repository. Record **(a)** again.
5. Quit the app and start it again with the same command. Record **(b)** after a restart.
6. Record where each conversation is listed:
   - **(c)** "CLAUDE-B-FIRST draft the release notes", from a folder that is not a repository;
   - **(d)** "CODEX-FIRST add a health check endpoint", a Codex conversation in `repo-b`, beside the Claude Code ones in `repo-a`.
7. **(e)** Open "CLAUDE-A-FIRST fix the login bug". The full history should show, ending with "Imported from Claude Code by Copilot Ready." and "Ready to continue." The 140-turn conversation starts with Copilot Ready's note about the earlier turns it left out.
8. Repeat steps 2 and 5–7 with a fresh home made by `setup on`, the path most users take.
9. Quit the app and dispose of the entire test VM or OS user account, including its profiles and credentials. Deleting `$E` alone does not remove OS-user shared state.

Then match the wording to what you saw:
- the finish text in `cmd/copilot-ready/web/app.js` (`importText`);
- the maintenance message in `cmd/copilot-ready/show.go`;
- **Showing conversations in the Copilot app** in `docs/guide.md`;
- [validation status](../../docs/validation.md).

The current UI offers **Open in the Copilot app**: it sends `ghapp://sessions/<id>` for each conversation and the user chooses Allow in the app. Without it, the user adds the repository in the app, sets Show Copilot CLI Session to a range that covers the conversation's date, and restarts the app if it was open. Record the tested app version and actual results separately; do not infer coverage from these instructions.

`show` sends those links through the system's `open` command. System URL handlers may target an existing app or profile, so run this only inside the disposable environment. For controlled link delivery, put a stand-in `open` first on `PATH` (for example a script that appends its argument to a file), then hand each logged link to the test app yourself.

## Windows

The same commands work in PowerShell or Git Bash with `python`, except that `--offline` is macOS-only. `drive.py` builds `scripts/e2e/powershell-shim` and puts it first on the helper's PATH. It answers setup's user-PATH registration, so the registry is not changed, and passes every other call to Windows PowerShell. `netmon.ps1` samples the connections of the run's Copilot processes. An optional network-blocked source test is `unshare -rn go test ./internal/sessions -run '^TestIndexerWithRealCopilot$' -v`, with a disposable home and Copilot for Linux explicitly on PATH. This is not a supported WSL installer workflow and is not part of ordinary source-only verification.
