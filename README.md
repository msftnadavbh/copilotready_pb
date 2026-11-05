<p align="center"><img src="site/icon.svg" alt="Copilot Ready" width="72" /></p>

# Copilot Ready

An independent, unofficial setup utility for bringing a reviewed selection of Codex and Claude Code configuration into GitHub Copilot CLI. It provides a local browser wizard for discovery, compatibility review, and applying changes.

**Source only:** this repository currently provides no hosted instance, published release, or installer downloads. The website is packaging/demo source, not a live download service.

## What it does

- Discovers supported instructions, skills, agents, prompts, MCP definitions, hooks, and plugins. Compatible items start selected; conflicts and unsupported behavior are shown for review.
- Updates an existing Copilot profile or creates a separate one. It manages a pinned official CLI runtime, launchers, backups, repair, rollback, and removal of owned setup components.
- Imports saved Claude Code and Codex conversations through Copilot's session import command. Optional desktop visibility adds a synthetic exchange; opening conversations in the desktop app requires the app's confirmation.
- Includes an opt-in Cursor preview (`-cursor` or `COPILOT_READY_CURSOR=1`), with compatibility limits.

Windows x64/ARM64 and macOS Intel/Apple Silicon are installer targets. Linux and WSL are not supported installer targets, although source tests can run on Linux. See the [guide](docs/guide.md) for conversion limits and the [validation status](docs/validation.md) for what has actually been checked.

## Build from source

Requirements: **Go 1.27** (see `go.mod`); **Node 24+** and npm for the website; **Python 3** (3.11+ for the release builder) for packaging and Python checks. Go dependencies are vendored and npm dependencies are pinned.

```sh
git clone https://github.com/msftnadavbh/copilotready_pb.git
cd copilotready_pb
go build -trimpath -o build/copilot-ready ./cmd/copilot-ready
# Windows: use -o build/copilot-ready.exe
```

On a supported host, run the built helper to open the local setup wizard. **Running it reads configuration from your user profile and the project you choose.** Use a disposable home and project when developing or evaluating changes. Building alone does not run setup.

To build the website demo:

```sh
npm ci --registry=https://registry.npmjs.org
npm run build:web
```

`scripts/build_release.py` builds and packages the four target binaries, wrappers, scaffold, source, and checksums. It requires an explicit HTTPS `--base-url` supplied by whoever packages a release. No deployment or cloud sign-in is configured here. See [website notes](docs/website.md) and [local validation instructions](docs/validation.md).

## Privacy and safety limits

Discovery and conversion run locally, but this is **not an offline-only guarantee**. Runtime installation and updates download official binaries; plugin installs, MCP servers, hooks, authentication, and later Copilot use can execute commands or access the network. Existing hooks may run during the optional desktop-indexing step. Review imported scripts and connections before approving them.

Applying changes can write to your Copilot profile, install integrations, register launchers, and import conversation content and workspace metadata. Changed files have backups and conflicting edits require review; undo does not remove imported conversations. A separate profile does not clone authentication, and shared skills may still be discovered. Keep your own backups and check organization policy before migrating work data.

Source credential stores and OAuth sessions are not imported. Literal-secret detection and compatibility checks are best-effort, not proof that arbitrary content is safe or free of sensitive data. Conversation import intentionally transfers conversation content into Copilot; review its privacy implications before selecting it.

## Source map

| Location | Purpose |
| --- | --- |
| `cmd/copilot-ready` | Local wizard, reviewed Apply, integrations, and conversation import |
| `internal/migration` | Discovery, conversion, and conflict review |
| `internal/installer` | Runtime verification, launchers, backups, and recovery |
| `internal/sessions`, `internal/standin` | Conversation conversion, index reading, and local stand-in model |
| `website`, `site`, `server.cjs` | Website source, generated demo, and optional static server |
| `scripts` | Packaging, synthetic fixtures, and focused checks |

## More information

- [Setup and compatibility guide](docs/guide.md)
- [Reusable configuration scaffold](docs/scaffold/README.md)
- [Validation instructions and status](docs/validation.md)
- [Local download-log reporting](docs/download-monitoring.md)

Not an official GitHub, OpenAI, Anthropic, or Cursor product. Third-party notices and licenses are preserved in [docs/licenses](docs/licenses) and `vendor`. No first-party license has been selected or included.
