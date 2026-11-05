# Copilot Ready

Pinned CLI: **1.0.105**. Windows x64/ARM64 and macOS Intel/Apple Silicon are targeted; Linux and WSL are not supported installer targets. This repository provides source and a website demo, not a hosted installer or published release. See the [README](../README.md) to build the helper.

## Start

For source use, run the helper you built on a supported host. If you package installers yourself, on Windows extract the ZIP, then right-click **Install Copilot.ps1** and choose **Run with PowerShell** (on Windows 11, choose **Show more options** first). If script policy prevents it, use **copilot-ready.exe** from the same folder. ARM PCs have their own package. On macOS, open the packaged ZIP and then **Install Copilot.command**. The unsigned wrapper/helper may require operating-system approval. The installer does not change execution policy or disable Gatekeeper. A Copilot-enabled account, internet access, and normal GitHub sign-in are still required.

The local wizard lets you choose an existing Copilot profile or a new one, pick a project, and review imports. Existing-profile mode changes the reviewed target; separate-profile mode does not clone authentication or session state.

When you update your normal profile, its preferences stay as they are. Updates, approval mode, protection and Git sign-in change only if you choose recommended settings or pick a protection option under Advanced; the default is **Keep my current setting**. The review lists every setting that will change with its current and new value. Instruction imports keep your existing instructions file and add a reference at its end, so they are not shown as replacements. **Open Copilot** opens a terminal in your project and runs the same `copilot` command a new terminal would, so both ways of starting Copilot behave the same. No personal inventory is uploaded to the website.

## Native support and conversion

Copilot combines applicable instruction files without a general precedence order. Project AGENTS.md, selected Claude instructions/rules, and skills can be natively discovered. Personal Codex and Claude configuration needs reviewed conversion. Path-specific instructions must retain matching scope. [GitHub instruction reference](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-custom-instructions).

Inspect skill/agent metadata and dependencies. A skill's presence is not proof its tools exist in Copilot. Shared personal skills can be discovered even with a separate Copilot profile. [GitHub skill locations](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-skills).

Codex MCP configuration is TOML; Claude and Copilot definitions use JSON. Conversions must account for transport, environment references, authentication, timeouts, and tool permissions. Codex runtime integrations such as node_repl are not assumed portable. [Codex MCP](https://learn.chatgpt.com/docs/extend/mcp?surface=cli), [Copilot MCP](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-mcp-servers).

Hooks require matching inputs, output wrappers, side effects, and failure behavior. Imported hooks are not executed during discovery. Plugins require a compatible native installation; copying enabled-plugin entries is insufficient. [Copilot hooks](https://docs.github.com/en/copilot/reference/hooks-reference), [Codex hooks](https://learn.chatgpt.com/docs/hooks), [plugin reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-plugin-reference).

Claude user settings and selected-project settings are separate sources. The inventory covers both; supported project discovery does not imply that all personal configuration transfers unchanged. [Claude Code settings](https://code.claude.com/docs/en/settings).

## Bringing connections and skills

Ready items start selected. **What needs work** lists missing credentials, disabled tools, name conflicts, and source behavior that cannot be transferred safely. Discovery checks files and dependencies without starting servers or running skills.

For MCP, setup preserves supported environment-variable references, not their resolved secrets. Codex's explicit environment forwarding and bearer/header references are translated; Claude's simple variable references are retained. Missing variables must be available to the launcher you use for Copilot. Literal credentials, dynamic credential helpers, custom OAuth metadata, source-specific tool filters, and unsupported substitutions require manual adaptation. Unknown fields are reported instead of silently dropped. [Codex MCP](https://learn.chatgpt.com/docs/extend/mcp?surface=cli), [Claude MCP](https://code.claude.com/docs/en/mcp).

Everything you approve is applied in one step. Selected MCP servers are written to Copilot's `mcp-config.json` with all of their tools, as they were in the source app; Copilot starts each server when it needs it, and a server that needs OAuth asks you to sign in there. Hooks are installed with the same transaction, and plugins are installed right after it with `copilot plugin install`; a plugin that cannot be installed is listed with the setup checks. Servers that an earlier setup added with no tools allowed are offered a one-click fix. Source OAuth tokens, browser sessions, and credential stores are never imported. On Windows, `${VAR}` references must be user or system environment variables for the Copilot app to see them. [Copilot MCP setup](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-mcp-servers).

Claude's custom configuration directory is honored. Project-local definitions stay project-scoped and need separate adoption; setup does not promote them into a personal profile. Explicit disabled servers and MCP tool-deny rules require review. Source permissions and organization policy are not transferred. Command-line overrides, remote environments, and every managed-policy source cannot be reconstructed from local files; verify your organization's restrictions in Copilot. [Claude configuration locations](https://code.claude.com/docs/en/troubleshoot-install#check-directory-permissions), [Claude settings](https://code.claude.com/docs/en/settings).

Compatible skills keep supporting files, manual-invocation settings, and file modes. Tool pre-approvals are removed from imported skills and disclosed. Descriptive fields such as `version` and `author` move under `metadata`, as in the Agent Skills format. Dependency caches (`node_modules`, Python virtual environments) are not copied. A skill that both apps have moves once: identical copies count as one, and when they differ the more recently edited copy is selected and the other stays available to choose instead; only one copy can be selected. "More recently edited" goes by file dates, which a sync or `git clone` can change, so check the note on the selected copy. Missing or outside file links, literal credentials, behavior-changing metadata such as `context` or `hooks`, source runtime dependencies, and disabled-skill settings need work first. Package names (`@scope/package`), environment-variable references (`$API_KEY`), code (`apiKey: options.apiKey`) and placeholders in examples are not treated as file links or credentials, in skills or in instructions. A skill folder that is a link is listed but not copied; copy the folder into place to bring it over. Shared and project skills may already be discovered by Copilot; setup reports restrictions it finds but does not rewrite those files. The checks cannot prove what an arbitrary script will do when you later run it. [Copilot skills](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-skills).

## Models, permissions, and memory

New profiles use Auto/balance, manual approvals, and native memory off. Existing valid choices are preserved unless the reviewed changes replace them. Cross-product model names and reasoning labels do not establish equivalence. Auto model selection, Assisted permissions, and Autopilot are separate features. [Copilot configuration](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference).

Reviewed Markdown memory is context, not a migration into Copilot's memory service. Credentials, OAuth caches, and databases are not copied. Existing Codex automations remain in Codex, including paused jobs. [Codex automations](https://learn.chatgpt.com/docs/automations).

Cursor is a preview and off by default. To include it, start the helper from a terminal with `-cursor`, or set `COPILOT_READY_CURSOR=1` before starting it. Cursor is then read from `~/.cursor` and from the selected project's `.cursor` folder. Copilot reads none of Cursor's files. Personal MCP servers, commands, skills and subagents are converted like the other apps'; Cursor's `${env:NAME}` becomes `${NAME}`. Project rules (`.cursor/rules/*.mdc`, `.cursorrules`) are listed with the Copilot instructions file each would become, but setup doesn't write into the repository. Rules that Cursor applies by description or only when @-mentioned have no Copilot equivalent. Cursor hooks are listed for review only: their input, output and failure behavior differ from Copilot's. User Rules live in Cursor's settings, not a file, and need copying by hand. [Cursor rules](https://cursor.com/docs/rules), [Cursor MCP](https://cursor.com/docs/context/mcp), [Cursor hooks](https://cursor.com/docs/hooks).

## Conversations

Saved Claude Code and Codex conversations (and Cursor's, when Cursor is turned on) appear as one row per app and start selected. Leave a row selected to import them, or deselect it to keep them only in the original app. After you apply, they import in the background with Copilot's own `copilot sessions import` (Copilot CLI 1.0.85 or later). Setup completes and Copilot opens without waiting, and a conversation that cannot be imported is listed under **Setup details** without affecting anything else. Continue an imported conversation with `/resume` in Copilot.

Cursor conversations come from the transcripts Cursor writes under `~/.cursor/projects`. They hold your messages and Cursor's replies, and name the tools Cursor used, but not their results, the model's reasoning or message times. Imported Cursor conversations say so in their first message and are dated when Cursor last saved them. Chats that exist only in Cursor's own databases are not read.

### Showing conversations in the Copilot app

The Copilot desktop app lists a CLI conversation only once it is in Copilot's session index. Copilot adds a conversation to that index when it is continued, not when it is imported (Copilot CLI 1.0.105). So without help, an imported conversation can appear in the app only after you continue it once in the CLI (`copilot --resume`), and then only with the app settings described below.

**Show these conversations in the Copilot desktop app**, on the review step and off by default, does that for you. After the import, setup continues each conversation once, on this computer:
- Copilot runs one turn against a stand-in model inside Copilot Ready. It listens only on 127.0.0.1 and accepts only a random key made for that run, and it always answers "Ready to continue." The model exchange is local and uses no AI credits. MCP servers and model tool calls are disabled; existing hooks may still run and can access files or the network.
- Each conversation ends with that exchange: "Imported from Claude Code by Copilot Ready." (or Codex) and "Ready to continue." It uses your profile's model name, so continuing it later doesn't switch models.
- Copilot's index records the conversation as updated today. `/resume` and the app keep its real date.
- Hooks already in your Copilot run as in any session, so they may run once for each conversation. The hooks and plugins you import in this setup are installed only after the conversations are done.

A conversation that cannot be added is listed under **Setup details**. If you imported conversations with an earlier version, choose **Show my imported conversations in the Copilot app** under maintenance; it adds only those that aren't in Copilot's index yet, then opens the ones not sent to the app before.

**Open in the Copilot app**, on the finish screen and under maintenance, sends each conversation to the app through its documented link `ghapp://sessions/<id>`, newest first, 20 at a time; choose it again for the next ones. Once all have been sent, choosing it again sends them all again, so one you dismissed in the app gets another chance; the app opens the ones you already allowed without asking. In the app, choose **Allow** on **Open session?** for each one. The app then adds its repository, or its folder for one that isn't a Git repository, and keeps the conversation in the sidebar with its full history. This works without any other setting and without restarting the app. The confirmation is the app's own; Copilot Ready doesn't answer it for you. [Copilot app links](https://docs.github.com/en/copilot/how-tos/github-copilot-app/open-with-deep-links).

Without that step, the app lists a CLI conversation only once two of its own settings allow it (Copilot app 1.1.33):
- **Add the repository.** Use **File → Create from Local Folder or Repository**. The app lists a CLI conversation only under a repository you have added there. A conversation from a folder that isn't a Git repository doesn't appear this way at all.
- **Turn on Settings → Sessions → Show Copilot CLI Session.** A new app profile starts with it **Off**, which hides every CLI conversation. Choose a range that covers the conversations' real dates, such as **Last 360 days**; older conversations stay hidden.
- **Restart the app if it was open.** A running app doesn't pick up the conversations until it restarts.

Opening a conversation in the app marks it as an app session in Copilot. The app keeps it, but if you later remove its project from the app, open it again with **Open in the Copilot app** rather than waiting for the app to list it ([github/app#3346](https://github.com/github/app/issues/3346)).

User and assistant messages, tool calls and their results, the title, folder, branch, and GitHub repository carry over. Long tool output is shortened. Model reasoning, images, per-message times, and compaction history stay in the original app. Conversations whose folder no longer exists cannot be imported, because Copilot needs the folder. Imported conversations are recorded in the setup folder, so running setup again does not create duplicates.

Claude Code transcripts are read from your Claude configuration folder; their format is internal to Claude Code and may change. [Claude Code sessions](https://code.claude.com/docs/en/sessions). Codex conversations are read through Codex's app server, using the newest Codex found: the command on your PATH or the one bundled with the Codex app. [Codex app server](https://learn.chatgpt.com/docs/app-server).

## Sandbox

Sandboxing restricts process access; manual approvals ask before actions. Setup prefers the sandbox on supported hosts and offers an explicit manual-mode choice when unavailable. Organization rules may prohibit fallback. Verify actual session enforcement; an OS check alone is not enough. Supported Git/gh credential handling may require OS approval. [GitHub sandbox documentation](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/about-cloud-and-local-sandboxes).

## Runtime maintenance

Use the visible Copilot Ready launcher to reopen the manager, choose a project, repair or roll back the managed runtime, or remove owned setup components. Profile data and projects are preserved by default. **Undo setup changes**, on the finish screen and under maintenance, restores the files your last setup changed from their backups and removes files it created, together with tool connections finished after it; files you edited since are kept, and imported conversations stay in Copilot. If setup stops partway, a recovery prompt appears at the top right away. Tool connections you have not finished stay listed when you reopen setup, ready to finish without another scan. Conflicting user edits require review. Multi-file migration uses a recovery journal and individual atomic writes, not a claim that the entire operation is atomic.

Copilot 1.0.105 does recognize COPILOT_CACHE_HOME. Package-cache and distribution overrides still matter. Setup installs the verified 1.0.105 executable and turns on Copilot's automatic updates, so Copilot downloads newer versions into Copilot Ready's cache and uses them in later sessions. Setup's own checks still run the verified executable. Native explicit update can replace the executable; changed bytes need verification/repair. The [official release](https://github.com/github/copilot-cli/releases/tag/v1.0.105) includes MSI assets; those install the CLI but do not perform this migration.

## Inspect

The release builder packages readable bootstraps, source, reusable templates, artifact hashes, and validation notes. There is no published release here. A digest detects changed bytes but is not an independent signature. See [validation](validation.md) for current checks and unverified paths.
