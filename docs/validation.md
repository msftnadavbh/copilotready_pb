# Validation

Validation applies only to the checks actually run against this source snapshot. It does not establish coverage for untested platforms or workflows.

## Local checks

Use Go 1.27, Node 24 or newer, and Python 3. Run checks with a disposable `HOME`, XDG directories, and temporary directory. Remove real `copilot`, Codex, and Claude commands from `PATH`: some integration tests auto-detect an installed CLI. Do not point tests at a real profile or project.

For real-CLI or desktop E2E checks, use a disposable VM or a dedicated OS user account with no real profiles or credentials. Environment variables alone do not isolate Keychain, preferences, URL handlers, or desktop state. Do not run these checks in your normal account; keep any test sign-in credentials local and clean up the entire disposable environment afterward.

```sh
go vet ./...
go test ./...
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/check_download_monitoring.py
npm ci --registry=https://registry.npmjs.org
npm run build:web
node --check server.cjs
node --check cmd/copilot-ready/web/app.js
```

The session-index fixtures contain only synthetic rows, generated from an empty SQLite database with Python's standard library. Regenerate them with `python3 scripts/generate_session_fixture.py`; WAL salts and thus binary hashes can change. Run the Go suite after regenerating.

The browser checks in `scripts/check_bulk_selection.js` and `scripts/check_show_in_app.js` require a browser and an isolated helper URL; they intercept setup API calls. See [manual end-to-end instructions](../scripts/e2e/README.md) for opt-in real-CLI checks. Those can download official binaries, run hooks/plugins, or launch an app and must not be run as ordinary source-only checks.

## Current status

Checks run for this source snapshot on Linux, with disposable home/XDG/temp directories and real CLIs excluded from `PATH`:

- Go 1.27.3: `go vet ./...` passed. `go test -count=1 -v ./...` passed the wizard, migration, sessions, and stand-in packages (including all session-index fixture consumers), but the installer package failed in `TestUninstallRemovesOnlyUneditedRegisteredHooks`, `TestUninstallPreservesHelperForLegacyHook`, and `TestLauncherPreviewDoesNotCreateManagedRoot`: their launcher code supports Windows/macOS only. The full suite is not green on Linux. Real-CLI and platform-specific checks were skipped by their guards.
- Python 3.12.3: `python3 -m unittest discover -s scripts -p 'test_*.py' -v` passed all 8 tests, including packaging and the local-only report CLI. `python3 scripts/check_download_monitoring.py` passed its disposable-server response and log checks.
- Node 24.22.1 / npm 11.19.1: installation with isolated npm configuration/cache, public registry, and install scripts disabled passed. All 31 locked package tarballs matched the lockfile integrities and exact public metadata. `npm run build:web` passed in an isolated build directory. The generated site was inspected for demo disclosure and local assets.
- `node --check` passed for `server.cjs`, the helper UI, both browser-check scripts, and the generated website bundle. Browser interaction checks were not run.
- Local Markdown links resolve. The tests above cover runtime integrity, credential detection, and session-index fixtures. Third-party notices and vendored dependency licenses are included.

Live Windows/macOS installation, desktop integration, authenticated workflows, and real-CLI end-to-end checks are not verified for this source snapshot. Linux is a source-test host, not a supported installer target.
