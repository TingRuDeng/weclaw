# WeClaw

[Chinese](README_CN.md)

[![CI](https://github.com/TingRuDeng/weclaw/actions/workflows/ci.yml/badge.svg)](https://github.com/TingRuDeng/weclaw/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/TingRuDeng/weclaw)](https://github.com/TingRuDeng/weclaw/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.26.5-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20Apple%20Silicon-black?logo=apple)](https://github.com/TingRuDeng/weclaw/releases/latest)
[![License](https://img.shields.io/github/license/TingRuDeng/weclaw)](LICENSE)

Remote-control local Codex and Claude from WeChat or Feishu. Keep real workspace and session context, receive live progress, approvals, and results, and explicitly hand Codex control between Desktop and a remote chat window.

> Official releases currently support **macOS Apple Silicon (darwin/arm64)**. The source can be built on other Go-supported platforms, but those builds are outside the current release asset scope.

## Why WeClaw

- **Take over local work remotely**: continue Codex and Claude sessions from WeChat or Feishu after leaving your computer.
- **Keep the original context**: reuse Codex workspaces/threads and Claude ACP sessions instead of starting a new conversation for every message.
- **See progress and receive results**: Feishu uses CardKit updates, while WeChat provides typing state and task results.
- **Use explicit ownership**: `/cx owner` hands a Codex session between Desktop and a remote chat window without concurrent writes.
- **Configure security boundaries**: user allowlists, workspace roots, admin access, audit logs, and Codex permission levels are independent controls.

## Quick Start

Prerequisite: install the agents you plan to use. Codex uses `codex`, and Claude uses `claude`. When the one-line installer detects Claude CLI, it installs and configures a pinned `claude-agent-acp` version.

```bash
# Install the actively maintained distribution
curl -sSL https://raw.githubusercontent.com/TingRuDeng/weclaw/main/install.sh | sh

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
/cx ls
/cx new
/cx owner remote
Inspect the current project and fix the failing tests
```

Without a valid session binding, a regular message only asks the user to select a session or send `/cx new`; it never creates a session implicitly.

### Take Over and Return a Codex Desktop Session

```text
/cx ls                 # Select an existing local workspace and thread
/cx owner              # Inspect owner, runtime location, and task state
/cx owner remote       # Hand control to the current WeChat or Feishu window
/cx owner desktop      # Return control to Codex Desktop when idle
```

A handoff completes runtime probing and state persistence before reporting success. While a remote task is active, wait for completion or send `/stop` before returning control to Desktop.

### Reuse Claude Code Sessions

```text
/cc ls
/cc switch <number|sessionId>
/cc new
/cc status
/cc cli
```

Claude uses ACP `session/list`, `session/resume`, and `session/new` to manage real sessions. A selected session keeps its context and is restored before the next message after WeClaw restarts. Claude ACP supports `/stop` and queued continuation, but not `/guide`.

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
    Core --> Codex[Codex app-server]
    Core --> Claude[Claude ACP]
    Core --> Other[Other ACP / HTTP / Companion Agents]
    Codex --> Owner{Explicit Ownership}
    Owner --> Remote[Current Remote Window]
    Owner --> Desktop[Codex Desktop]
    Claude --> Session[Claude Code Session]
```

WeClaw uses the `platform` abstraction to share commands, sessions, tasks, and approvals, then renders text, typing state, or Feishu cards according to platform capabilities. The main Codex path uses its native app-server protocol. Claude is ACP-only for remote access; native `claude` is only used to hand off an idle session locally.

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

| Agent | Remote Backend | Session Reuse | Model / Reasoning | Local Handoff |
| --- | --- | :---: | :---: | --- |
| Codex | app-server | Workspace + thread | Yes | Codex CLI / Desktop |
| Claude | ACP | ACP session | Yes | Native Claude CLI |
| OpenCode | Companion | Depends on local connection | Agent-dependent | Visible terminal |
| Other agents | ACP / HTTP / Companion | Protocol-dependent | Agent-dependent | Configuration-dependent |

## Chat Commands

| Command | Description |
| --- | --- |
| `/help`, `/status` | Show help and WeClaw runtime status |
| `/cwd [path]` | Show or switch the working directory; regular users are confined to allowed workspace roots |
| `/new` | Explicitly create a session for the current default agent |
| `/model`, `/reasoning` | Show or change the current session model and reasoning effort |
| `/mode [default|yolo]` | Show or change Codex approval behavior for the current user |
| `/progress [mode]` | Show or change progress mode |
| `/ps`, `/stop` | List or stop current tasks |
| `/cancel`, `/guide` | Remove a queued message or steer the active Codex task |
| `/cx help`, `/cc help` | Show complete Codex or Claude session commands |
| `/update`, `/restart [--force]` | Remotely update or restart WeClaw as an administrator |

<details>
<summary>Common Codex commands</summary>

`/cx ls`, `/cx <number|..>`, `/cx cd <workspace|..>`, `/cx switch <session>`, `/cx new`, `/cx pwd`, `/cx status`, `/cx owner [remote|desktop]`, `/cx quota`, `/cx model status|ls`, `/cx cli`, `/cx app`, `/cx clean`, `/cx detach`.

</details>

<details>
<summary>Common Claude commands</summary>

`/cc ls`, `/cc switch <number|sessionId>`, `/cc new`, `/cc pwd`, `/cc status`, `/cc model status|ls`, `/cc cli`.

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
- Codex: `/cx ls`, `/cx status`, `/cx owner`, `/cx new`, `/cx quota`
- Claude: `/cc ls`, `/cc status`, `/cc new`, `/cc pwd`, `/cc model ls`
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

`weclaw web` binds to `127.0.0.1:39282` by default, prints a tokenized local URL, and opens the browser. Soft settings such as agents, progress, allowlists, administrators, and workspace roots support hot reload. Platform enablement, credentials, or account topology changes require a restart.

Key security rules:

- An empty platform `allowed_users` list rejects everyone by default.
- `admin_users` grants only WeClaw management access; the user must still belong to the relevant platform allowlist.
- Regular users may only `/cwd` into `allowed_workspace_roots` and their descendants; administrators are exempt.
- A non-loopback `api_addr` requires `api_token`.
- Audit logging is enabled by default and never records secrets.
- Codex `permission_level` accepts `default`, `auto_review`, and `full_access`; the effective default is `default`.

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

`restart` and `update --restart` validate configuration and agent dependencies before stopping the old service. A normal restart does not interrupt active tasks. Update official installations with `weclaw update`; never overwrite the binary in PATH with a local build.

## Build from Source

```bash
git clone https://github.com/TingRuDeng/weclaw.git
cd weclaw
go build -o weclaw .
./weclaw --help
```

The repository currently uses Go 1.26.5. No publicly pullable container image is currently published in sync with this maintained distribution.

## Upstream and License

This repository is an actively maintained fork of [fastclaw-ai/weclaw](https://github.com/fastclaw-ai/weclaw) and its WeChat integration is inspired by [@tencent-weixin/openclaw-weixin](https://npmx.dev/package/@tencent-weixin/openclaw-weixin). Follow the project license and relevant platform terms, and only use accounts and devices you are authorized to control.

[Contributors](https://github.com/TingRuDeng/weclaw/graphs/contributors) · [Releases](https://github.com/TingRuDeng/weclaw/releases) · [Star History](https://star-history.com/#TingRuDeng/weclaw&Timeline)

License: [AGPL-3.0-or-later](LICENSE)
