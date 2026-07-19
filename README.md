# WeClaw

[Chinese](README_CN.md)

[![CI](https://github.com/TingRuDeng/weclaw/actions/workflows/ci.yml/badge.svg)](https://github.com/TingRuDeng/weclaw/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/TingRuDeng/weclaw)](https://github.com/TingRuDeng/weclaw/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26.5-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-black)](https://github.com/TingRuDeng/weclaw/releases/latest)
[![License](https://img.shields.io/github/license/TingRuDeng/weclaw)](LICENSE)

Use local Codex and Claude remotely from WeChat or Feishu while keeping real workspace and session context, live progress, approvals, and results. Codex runs as one shared local app-server; WeChat and Feishu windows are frontend clients bound to a workspace/thread.

> Official releases support **macOS Apple Silicon / Intel (darwin/arm64 and darwin/amd64)** plus **Linux arm64 / amd64**. Windows assets are not currently published.

## Why WeClaw

- **Take over local work remotely**: continue Codex and Claude sessions from WeChat or Feishu after leaving your computer.
- **Keep the original context**: reuse Codex workspaces/threads and Claude ACP sessions instead of starting a new conversation for every message.
- **See progress and receive results**: Feishu uses CardKit updates, while WeChat provides typing state and task results.
- **Use one Codex runtime boundary**: every remote window connects to the same app-server; windows keep session bindings, while the server serializes active turns on each thread.
- **Configure security boundaries**: user allowlists, workspace roots, admin access, audit logs, and Codex permission levels are independent controls.

## Quick Start

Prerequisite: install the agents you plan to use. Codex uses `codex`, and Claude uses `claude`. When the one-line installer detects Claude CLI, it installs and configures a pinned `claude-agent-acp` version.

```bash
# Install the actively maintained distribution
curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/TingRuDeng/weclaw/main/install.sh | sh

# Check agents, platform credentials, and access control
weclaw doctor

# Connect WeChat or Feishu as needed
weclaw wechat login
weclaw feishu add

# Start the background service
weclaw start
weclaw status
```

The configuration file is `~/.weclaw/config.json`, the runtime log is `~/.weclaw/weclaw.log`, and the default audit log is `~/.weclaw/audit.log`.

## Core Workflows

### Start a Codex Task Remotely

```text
/cwd /path/to/project
/cx ls                 # List existing sessions
/cx <number>           # Select and bind a session; Feishu also supports session cards
# Or send /cx new      # Create and bind a session
Inspect the current project and fix the failing tests
```

After selecting an existing session or sending `/cx new`, send the task directly. Without a valid session binding, a regular message only asks the user to select a session or send `/cx new`; it never creates or binds a session implicitly.

### Use the Shared Codex app-server

```text
/cx ls                 # List existing local workspaces and threads
/cx <number>           # Bind this frontend window to the selected thread
/cx status             # Inspect the binding, shared host, workspace, thread, and task
```

WeClaw exposes native `codex app-server` through a stable Unix socket and connects with the upstream WebSocket-over-UDS protocol; the socket is not a raw JSONL stream. The first client starts the host on demand, and later WeChat, Feishu, or other WeClaw frontends reuse that service. Each window persists its own workspace/thread binding. Multiple windows may bind the same thread, but only one turn may write to that thread at a time. A transport disconnect does not clear a frontend binding; an accepted turn keeps its writer guard until authoritative terminal confirmation, and the next operation reconnects to the shared host. Legacy Codex owner state is discarded on load, and legacy `type: companion` configuration migrates to the shared app-server.

`/cx app`, `/cx cli`, `/cx attach`, and `/cx detach` are disabled because they would start an independent Codex writer. This version also does not treat a separately launched Codex Desktop as a shared-host client; a future local UI must connect to this same app-server rather than start a second one.

### Switch Codex OAuth Accounts Safely

WeClaw can save multiple authenticated Codex ChatGPT OAuth accounts and switch the host-level identity of the single shared app-server. A switch never changes a workspace, thread, or frontend binding and never replays the previous message; the next message uses the new account.

```bash
weclaw codex account list
weclaw codex account current
weclaw codex account save <label> [--replace] [--allow-file-store]
weclaw codex account use <id-or-label> [--yes]
weclaw codex account remove <id-or-label>
weclaw codex account doctor
```

The account index is isolated by a host ID derived from `CODEX_HOME + app-server socket` and stored under `~/.weclaw/codex-accounts/<host-id>/`. It contains only labels, masked email addresses, fingerprints, and secret references. Complete OAuth snapshots go to macOS Keychain or Linux Secret Service first. A `0600` file fallback is permitted only when the local user explicitly passes `--allow-file-store`. API keys, PATs, Bedrock, and other authentication modes are rejected. If Keychain or Secret Service cannot delete an old secret after a profile replacement or removal, the index retains a pending-cleanup record, retries it during later account transactions, and makes `doctor` report it instead of silently leaving OAuth material behind.

To collect several profiles, sign Codex into each target account and run `save`. While WeClaw is running, `save` accepts only the account actually used by the shared Host when it matches `auth.json`. If a manual sign-in has changed the file while the old Host still caches its previous token, stop WeClaw before saving the new account offline. A running service makes the CLI use the local control API; if the process exists but that API is unavailable, the CLI fails closed instead of editing authentication directly. With the service stopped, `use` only projects authentication atomically and updates the active profile for the next start.

An online `use` first rejects active tasks, active or uncertain writer leases, and every active or unknown thread. It persists a switch journal before stopping the real managed Host, projects the target authentication, starts the unique Host, and verifies both account identity and rate limits. A target startup or verification failure restores the previous authentication and Host. A mid-switch process exit or rollback failure remains fail-closed after restart instead of becoming writable when memory resets. Online `save` likewise commits the profile index and Host identity metadata as one compensated operation. WeClaw never terminates a legacy or otherwise unverified app-server; run `weclaw codex account doctor`. To clear an unsafe journal, stop the service, explicitly run offline `use`, then start it again.

Use `/cx account` or `/cx account status` from Feishu or WeChat to inspect the masked current profile. Only an administrator in a direct chat may list profiles or run `/cx account use <id-or-label>`. A Feishu list selection adds a five-minute confirmation card scoped to the operator, route, target profile, and list revision. A WeClaw host has one globally active Codex account, not one account per chat window.

### Reuse Claude Code Sessions

```text
/cc ls
/cc switch <number|sessionId>
/cc new
/cc status
/cc quota
```

Claude uses one process-resident shared ClaudeHost for real ACP sessions: each WeClaw service starts one `claude-agent-acp` process, while WeChat, Feishu, and other frontends persist only their own workspace/session bindings. `session/list` is the directory source of truth. Multiple frontends may bind the same session, the host performs only one effective `session/resume` for it, and a session-scoped writer lease is acquired only when a prompt starts. Another frontend cannot append work to the current writer's queue and receives an explicit busy result until that task ends.

Selecting or creating a session, choosing it from a Feishu card, or using global `/new` while Claude is selected changes only the current frontend binding. It never overwrites or releases another frontend. A failed `session/resume`, ACP disconnect, or ClaudeHost startup marks that binding runtime-unavailable without reverting the selected agent/session or clearing the binding; regular writes stay blocked until recovery succeeds. After a WeClaw restart, durable bindings return as `pending_resume` and the shared runtime is restored on real use.

If ACP has not persisted an empty session immediately after `/cc new`, `/cc ls` marks the acquired binding as the “current new session.” This entry is display-only until the first message makes it part of the normal catalog, and it never bypasses `/cc switch` validation against `session/list`.

`/cc owner` and `/cc cli` are disabled. ClaudeHost no longer has a frontend-exclusive owner, and an independent `claude --resume` would bypass the session writer lease and create a second writer. Legacy `remote`, `local`, and `unclaimed` control intents are discarded on load while every frontend binding is retained. Native `claude` is used only as a short-lived, prompt-free fallback for `/cc quota`, never for session writes. Claude tasks support `/stop` and one queued continuation from the same frontend, but not `/guide`.

### Control a Running Task

- Send one regular message while a task is active: queue it and run it automatically after the current task succeeds or fails.
- `/cancel`: remove the queued message without stopping the active task.
- `/guide`: steer the active Codex task with the queued message; Claude does not support it.
- `/stop`: stop the task running in the current chat window.
- `/ps`: list tasks running for the current user.

## How It Works

```mermaid
flowchart LR
    User[User] --> WeChat[WeChat Personal Account]
    User --> Feishu[Feishu / Lark]
    WeChat --> Bridge[WeClaw]
    Feishu --> Bridge
    Bridge --> Core[Session Binding · Task Queue · Approval · Progress]
    Core --> Codex[Single shared Codex app-server]
    Core --> Claude[Single shared ClaudeHost]
    Core --> Other[Other ACP / HTTP / Companion Agents]
    Codex --> Bindings[Multiple frontend bindings]
    Bindings --> WeChatClient[WeChat window]
    Bindings --> FeishuClient[Feishu window]
    Claude --> ClaudeBindings[Multiple frontend bindings]
    ClaudeBindings --> Session[Claude Code session writer lease]
```

WeClaw uses the `platform` abstraction to share commands, sessions, tasks, and approvals, then renders text, typing state, or Feishu cards according to platform capabilities. The main Codex path uses its native app-server protocol. Claude uses one process-resident ACP ClaudeHost; native `claude` is only a prompt-free quota-query fallback.

## Capability Matrix

| Capability | WeChat Personal Account | Feishu / Lark |
| --- | :---: | :---: |
| Text, images, and files | Yes | Yes |
| Live progress | Typing state + text | CardKit updates |
| Interactive choices and approvals | Numbered or text choices | Native buttons and cards |
| Group chat | Direct messages only | Yes, requires @bot by default |
| Multiple accounts / bots | Yes | Yes |
| Proactive send | Yes | Yes, text only today |
| User authorization codes | Yes | Yes |

| Agent | Remote Backend | Session Reuse | Model / Reasoning | Independent Writer |
| --- | --- | :---: | :---: | --- |
| Codex | Single shared app-server | Workspace + thread | Yes | No independent writer |
| Claude | Single shared ClaudeHost (ACP) | ACP session + writer lease | Yes | Disabled |
| OpenCode | Companion | Depends on local connection | Agent-dependent | Visible terminal |
| Other agents | ACP / HTTP / Companion | Protocol-dependent | Agent-dependent | Configuration-dependent |

## Chat Commands

| Command | Description |
| --- | --- |
| `/help`, `/status` | Show help and WeClaw runtime status |
| `/cwd [path]` | Show or switch the current frontend workspace; switching also updates Agent default cwd values, and regular users are confined to allowed workspace roots |
| `/new` | Explicitly create a session for the current default agent; also bind it when Codex is the default |
| `/model`, `/reasoning` | Show or change the bound session configuration, or the new-session defaults when no session is bound |
| `/mode [default|yolo]` | Show or change Agent approval behavior for the current frontend; it applies to both Codex and Claude, and bare `/mode` opens a Feishu choice card |
| `/progress [mode]` | Show or change progress mode |
| `/ps`, `/stop` | List or stop current tasks |
| `/cancel`, `/guide` | Remove a queued message or steer the active Codex task |
| `/cx help`, `/cc help` | Show complete Codex or Claude session commands |
| `/cx <number>`, `/cx switch <number>` | Select and bind a Codex session in the current workspace |
| `/cx new` | Create and bind a Codex session in the current workspace |
| `/cx account`, `/cx account status` | Inspect the host-level Codex account; administrator direct messages may select and switch |
| `/update`, `/restart [--force]` | Remotely update or restart WeClaw as an administrator |

<details>
<summary>Common Codex commands</summary>

Select and bind: `/cx <number>`, `/cx switch <session>`, `/cx cd <workspace>` when that workspace has one session, and `/cx new`.

Runtime boundary: `/cx status` is the single view for the current binding, shared host, writer, and task state.

Other commands: `/cx whoami`, `/cx ls`, `/cx ..`, `/cx cd <workspace|..>`, `/cx pwd`, `/cx status`, `/cx quota`, `/cx account`, `/cx account status`, `/cx account use <profile>`, `/cx model status|ls`, and `/cx clean`. `/cx model status` shows defaults for newly created Codex sessions; use `/model` and `/reasoning` for the bound session.

</details>

<details>
<summary>Common Claude commands</summary>

`/cc whoami`, `/cc ls`, `/cc switch <number|sessionId>`, `/cc new`, `/cc pwd`, `/cc status`, `/cc quota`, `/cc model status|ls`. `/cc status` is the unified binding, shared-ClaudeHost, and writer view. `/cc model status` shows defaults for newly created Claude sessions; use `/model` and `/reasoning` for the bound session. `/cc owner` and `/cc cli` are disabled.

`/cc quota` reuses the local Claude Code OAuth login to read the 5-hour, 7-day, and model-scoped limits without sending a model request. WeClaw first supports Claude Code's legacy Keychain/credentials file and its Anthropic usage endpoint, then falls back to a short-lived native `get_usage` control query when those credentials are unavailable or the request fails. The token is kept in memory, sent only to the fixed Anthropic endpoint, never logged or persisted, and never forwarded through redirects. These credential, endpoint, and structured-control contracts are not stable public APIs and may change in later Claude Code releases. API key, Bedrock, Vertex, and sessions without profile scope report that subscription limits are unavailable.

</details>

## Platform Setup

### WeChat

```bash
weclaw wechat login
weclaw wechat users pending
weclaw wechat users approve-code <authorization-code> [--admin]
```

An unauthorized WeChat user receives a short-lived authorization code. An empty `allowed_users` list rejects everyone by default.

### Feishu

```bash
weclaw feishu add
weclaw feishu status --name <bot-name>
weclaw feishu users pending
weclaw feishu users approve-code <authorization-code> [--bot <name|app_id>] [--admin]
```

`weclaw feishu add` saves credentials interactively and updates `platforms.feishu.bots[]`. The `app_secret` is stored only in a separate credential file, never in `config.json`. Each bot can have its own user allowlist, default agent, and progress mode.

<details>
<summary>Minimum Feishu application permissions</summary>

Tenant scopes: `im:message.p2p_msg:readonly`, `im:message.group_at_msg:readonly`, `im:message.group_at_msg.include_bot:readonly`, `im:message:send_as_bot`, `im:resource`, `im:chat`, `cardkit:card:read`, `cardkit:card:write`, `application:bot.basic_info:read`, and `application:bot.menu:write`. WeClaw runtime does not require user scopes. Publish a new Feishu application version and complete approval after changing permissions.

</details>

<details>
<summary>Recommended Feishu menu</summary>

- Common: `/help`, `/status`, `/model`, `/reasoning`, `/cwd`
- Codex: `/cx ls`, `/cx status`, `/cx new`, `/cx quota`, `/cx account`
- Claude: `/cc ls`, `/cc status`, `/cc new`, `/cc pwd`, `/cc quota`, `/cc model ls`
- Control: `/ps`, `/cancel`, `/guide`, `/stop`, `/restart`

</details>

## Configuration and Security

Use the local panel or CLI before editing JSON manually:

```bash
weclaw web
weclaw config agent --name claude
weclaw config permission --agent codex --level default
weclaw doctor
```

`weclaw web` binds to `127.0.0.1:39282` by default, injects the token through a URL fragment that is never sent to the server, and opens the browser. Soft settings such as agents, progress, allowlists, administrators, and workspace roots support hot reload. Platform enablement, credentials, or account topology changes require a restart. The built-in server has no TLS: non-loopback listeners are rejected by default and require an explicit `--allow-insecure-http` opt-in on a trusted LAN (a strong random token is still generated when `--token` is omitted); use an HTTPS tunnel or reverse proxy for public access.

Key security rules:

- An empty platform `allowed_users` list rejects everyone by default.
- `admin_users` grants only WeClaw management access; the user must still belong to the relevant platform allowlist.
- Regular users may only `/cwd` into `allowed_workspace_roots` and their descendants; administrators are exempt.
- A non-loopback `api_addr` requires `api_token`.
- Audit logging is enabled by default and never records secrets.
- Codex `permission_level` accepts `default`, `auto_review`, and `full_access`; the effective default is `default`.
- Codex manages the shared Unix socket automatically. Set `app_server_socket` only for multi-process or `run_as_user` deployments; its parent must be owned by the target user and no more permissive than `0700`.

| Codex Permission Level | Behavior |
| --- | --- |
| `default` | `workspace-write` + on-request approval + user confirmation |
| `auto_review` | Keeps the sandbox and lets Codex review escalation requests |
| `full_access` | `danger-full-access` + no approval; trusted environments only |

## Run and Update

```bash
weclaw start                 # Start in the background
weclaw start --foreground    # Run in the foreground for debugging
weclaw status
weclaw restart
weclaw restart --force       # Explicitly interrupt active tasks
weclaw stop
weclaw update
weclaw update --restart
weclaw version
```

`weclaw update` returns immediately when the installed version is already current. Configuration and agent preflight runs only after installing a new version or when `update --restart` is explicitly requested. `restart` and `update --restart` finish preflight before stopping the old service, and a normal restart does not interrupt active tasks. Update official installations with `weclaw update`; never overwrite the binary in PATH with a local build.

## Build from Source

```bash
git clone https://github.com/TingRuDeng/weclaw.git
cd weclaw
go build -o weclaw .
./weclaw --help
```

The repository currently uses Go 1.26.5. No publicly pullable container image is currently published in sync with this maintained distribution.

`scripts/release.sh` is the only authoritative stable-release entrypoint. The manual GitHub Actions Release workflow checks out clean `main` and delegates to that script instead of maintaining a second test, build, or upload pipeline.

## Upstream and License

This repository is an actively maintained fork of [fastclaw-ai/weclaw](https://github.com/fastclaw-ai/weclaw) and its WeChat integration is inspired by [@tencent-weixin/openclaw-weixin](https://npmx.dev/package/@tencent-weixin/openclaw-weixin). Follow the project license and relevant platform terms, and only use accounts and devices you are authorized to control.

[Contributors](https://github.com/TingRuDeng/weclaw/graphs/contributors) · [Releases](https://github.com/TingRuDeng/weclaw/releases) · [Star History](https://star-history.com/#TingRuDeng/weclaw&Timeline)

License: [AGPL-3.0-or-later](LICENSE)
