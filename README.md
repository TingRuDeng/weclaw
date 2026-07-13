# WeClaw

[中文文档](README_CN.md)

WeChat & Feishu AI Agent Bridge — connect WeChat (personal) and Feishu/Lark to AI agents (Claude, Codex, Gemini, Kimi, etc.).

> This project is inspired by [@tencent-weixin/openclaw-weixin](https://npmx.dev/package/@tencent-weixin/openclaw-weixin). For personal learning only, not for commercial use.

| | | |
|:---:|:---:|:---:|
| <img src="previews/preview1.png" width="280" /> | <img src="previews/preview2.png" width="280" /> | <img src="previews/preview3.png" width="280" /> |

## Quick Start

```bash
# One-line install
curl -sSL https://raw.githubusercontent.com/TingRuDeng/weclaw/main/install.sh | sh

# Private repository install
export GITHUB_TOKEN=ghp_xxx
curl -H "Authorization: Bearer $GITHUB_TOKEN" -sSL https://raw.githubusercontent.com/TingRuDeng/weclaw/main/install.sh | sh

# Start (first run will prompt QR code login)
weclaw start
```

That's it. On first start, WeClaw will:
1. Show a QR code — scan with WeChat to login
2. Auto-detect installed AI agents (Claude, Codex, Gemini, etc.)
3. Save config to `~/.weclaw/config.json`
4. Start receiving and replying to WeChat messages

Use `weclaw wechat login` to add additional WeChat accounts.

飞书接入默认关闭。启用前先保存并校验飞书应用凭证：

```bash
weclaw feishu bootstrap --name project-a --app-id cli_xxx --app-secret xxx --allowed-users ou_xxx --default-agent codex --progress stream
weclaw feishu login --name project-a --app-id cli_xxx --app-secret xxx
weclaw feishu status --name project-a
```

`bootstrap` saves Feishu credentials and updates `platforms.feishu.bots[]` in one step, which is the recommended first-time setup path. If the official `lark-cli` is installed, the command suggests using it for permission, event subscription, and message-send diagnostics. WeClaw runtime still uses the built-in Feishu SDK websocket client and does not depend on `lark-cli`.

飞书应用建议按最小权限开通。WeClaw 运行时使用应用身份，不需要 `user` scopes；开通或修改权限后必须重新发布版本并完成审批。

```json
{
  "scopes": {
    "tenant": [
      "im:message.p2p_msg:readonly",
      "im:message.group_at_msg:readonly",
      "im:message.group_at_msg.include_bot:readonly",
      "im:message:send_as_bot",
      "im:resource",
      "im:chat",
      "cardkit:card:read",
      "cardkit:card:write",
      "application:bot.basic_info:read",
      "application:bot.menu:write"
    ],
    "user": []
  }
}
```

其中 `im:message.p2p_msg:readonly` 负责单聊入站消息。如果机器人能主动发消息，但单聊回复没有触发 `im.message.receive_v1` 事件，优先检查这个权限和版本发布状态。

### Other install methods

```bash
# Via Go
go install github.com/fastclaw-ai/weclaw@latest

# Via Docker
docker run -it -v ~/.weclaw:/root/.weclaw ghcr.io/fastclaw-ai/weclaw start
```

## How It Works

<p align="center">
  <img src="previews/architecture.png" width="600" />
</p>

**Agent modes:**

| Mode | How it works | Examples |
|------|-------------|----------|
| ACP  | Long-running subprocess, JSON-RPC over stdio. Fastest — reuses process and sessions. | Claude, Codex, Kimi, Gemini, Cursor, OpenClaw |
| CLI  | Spawns a new process per message. | Codex (`codex exec`) |
| HTTP | OpenAI-compatible chat completions API. | OpenClaw (HTTP fallback) |
| Companion | WeClaw keeps the WeChat bridge in the background while a local visible CLI terminal stays attached. | OpenCode, Codex app-server |

Claude remote access is ACP-only; native `claude` is used only to hand off an idle session locally. OpenCode is detected as Companion mode. Codex still defaults to ACP so its workspace, thread, and model controls remain available.

For OpenCode Companion mode, start WeClaw first, then run this in the same workspace terminal:

```bash
weclaw companion --agent opencode --cwd /path/to/project
```

Codex Companion starts a local `codex app-server`, then attaches a visible `codex --remote` terminal. Example:

```json
{
  "agents": {
    "codex": {
      "type": "companion",
      "command": "codex",
      "cwd": "/path/to/project"
    }
  }
}
```

Then run this in the same workspace terminal:

```bash
weclaw companion --agent codex --cwd /path/to/project
```

## Platform Support

| Capability | WeChat (personal) | Feishu / Lark |
|------------|:-----------------:|:-------------:|
| Text & slash commands | ✅ | ✅ |
| Images (send/receive) | ✅ | ✅ |
| Files (send/receive) | ✅ | ✅ |
| Voice → text (inbound) | ✅ (WeChat STT) | ⚠️ received as file, no auto-transcription |
| Rich cards | ❌ | ✅ (CardKit) |
| Streaming (typewriter) | ❌ degrades to typing + text | ✅ CardKit stream |
| Interactive buttons | ❌ degrades to numbered text | ✅ (choices / approvals) |
| Group chat | ❌ 1:1 only | ✅ (requires @bot) |
| Proactive send | ✅ | ✅ (text) |
| Login | QR scan (`weclaw wechat login`) | app_id/secret (`weclaw feishu login`) |

Business logic (commands, agent routing, sessions, progress) is platform-agnostic; each adapter degrades gracefully to its native capabilities.

## Chat Commands

Send these as WeChat or Feishu messages:

| Command | Description |
|---------|-------------|
| `hello` | Send to default agent |
| `/codex write a function` | Send to a specific agent |
| `/cc explain this code` | Send to agent by alias |
| `/cc help` | 查看 Claude 会话命令 |
| `/claude` | Switch default agent to Claude |
| `/cwd /path/to/project` | Switch workspace directory (regular users are confined to `allowed_workspace_roots`; admins are exempt) |
| `/new` | Start a new conversation (clear session) |
| `/model` / `/model <id>` | Show or switch the current session agent model; an attached Claude session updates immediately, otherwise select or create a session first |
| `/reasoning` / `/reasoning <effort>` | Show or switch the current session agent reasoning effort; an attached Claude session updates immediately, otherwise select or create a session first |
| `/mode` / `/mode yolo` / `/mode default` | Show / current-user auto-approve / button-confirm Codex approvals |
| `/ps` | List your running tasks |
| `/stop` | Stop the current running task |
| `/update` / `/upgrade` | Admin-only remote WeClaw self-update (requires `admin_users`) |
| `/restart` / `/restart --force` | Admin-only remote WeClaw restart (requires `admin_users`) |
| `/status` | Show WeClaw runtime status (agent, uptime, running tasks, call/error counts, mode, limits) |
| `/help` | Show help message |

### 飞书机器人推荐菜单

飞书自定义菜单最多可配置 5 个主菜单，每个主菜单最多 5 个子菜单。建议先按下面这组常用命令配置；子菜单动作直接填写命令文本。

| 主菜单 | 子菜单 | 命令 |
| ------ | ------ | ---- |
| 🧭 常用 | 帮助 | `/help` |
| 🧭 常用 | 状态 | `/status` |
| 🧭 常用 | 进度模式 | `/progress` |
| 🧭 常用 | 确认模式 | `/mode` |
| 🧭 常用 | 停止任务 | `/stop` |
| 🤖 Codex | 工作空间 | `/cx ls` |
| 🤖 Codex | 会话状态 | `/cx status` |
| 🤖 Codex | 新建会话 | `/cx new` |
| 🤖 Codex | 当前目录 | `/cx pwd` |
| 🤖 Codex | 模型列表 | `/cx model ls` |
| 🧠 Claude | 会话列表 | `/cc ls` |
| 🧠 Claude | 会话状态 | `/cc status` |
| 🧠 Claude | 新建会话 | `/cc new` |
| 🧠 Claude | 当前目录 | `/cc pwd` |
| 🧠 Claude | 模型列表 | `/cc model ls` |
| 📁 工作区 | 当前目录 | `/cwd` |
| 📁 工作区 | Codex 帮助 | `/cx help` |
| 📁 工作区 | Codex 额度 | `/cx quota` |
| 📁 工作区 | Codex 清理 | `/cx clean` |
| 📁 工作区 | WeClaw 信息 | `/info` |
| ⚙️ 控制 | 运行任务 | `/ps` |
| ⚙️ 控制 | 引导任务 | `/guide` |
| ⚙️ 控制 | 停止任务 | `/stop` |
| ⚙️ 控制 | 更新 WeClaw | `/update` |
| ⚙️ 控制 | 重启 WeClaw | `/restart` |

普通计划确认仍直接回复“确认”。Codex 运行中收到的第二条普通消息会暂存，未选择 `/guide`、`/cancel` 或 `/stop` 时会在上一任务结束后自动执行；`/cancel` 只撤回暂存消息，停止运行中任务请用 `/stop`。

### Codex 主路径

Codex 的推荐使用方式是微信 remote-first，本地接手入口按需打开：

| 命令 | 说明 |
| ---- | ---- |
| `/cx status` | 查看当前 workspace、thread、remote 和本地入口记录 |
| `/cx quota` | 查看 Codex 账号额度 |
| `/cx ls` | 查看 Codex 工作空间或当前工作空间会话 |
| `/cx <编号|..>` | 选择当前列表项，或返回上一级 |
| `/cx cd <编号|工作空间名|..>` | 进入工作空间或返回工作空间列表 |
| `/cx switch <编号>` | 切换当前工作空间会话 |
| `/cx new` | 新建当前工作空间会话 |
| `/cx cli` | 在本地 Terminal 打开当前 thread 的 Codex CLI |
| `/cx app` | 在 Codex App 中打开当前工作空间 |
| `/cx clean` | 清理已不存在的 WeClaw 工作空间记录 |
| `/cx help` | 查看 Codex 高级会话命令 |

本地 Terminal 或 Codex App 只是接手入口。手动关闭它们不会影响微信 remote 会话，`/cx status` 也不会实时探测本地窗口是否仍然存在。若微信 / 飞书切换到 Codex App 正在运行的会话，WeClaw 会登记该任务、提示当前进度入口，并在任务完成后把结果回推到对应会话。

飞书会话按聊天窗口聚合：单聊使用该用户与机器人的 DM 会话，群聊使用群会话。回复串 / 话题不会再创建 WeClaw 子会话；如果需要多个项目并行，建议为不同项目配置不同飞书机器人入口。

### Claude 会话复用

Claude uses ACP `session/list`, `session/resume`, and `session/new` as the source of truth for real Claude Code sessions. A selected session keeps its context across WeChat or Feishu messages and is restored after restart:

| 命令 | 说明 |
| ---- | ---- |
| `/cc ls` | List workspaces and resumable sessions returned by Claude ACP |
| `/cc switch <index|sessionId>` | Resume and bind the selected session |
| `/cc new` | Create and bind a new session immediately |
| `/cc pwd` | Show the current Claude workspace |
| `/cc status` | Show workspace, session, restore state, model, and reasoning effort |
| `/cc cli` | Hand an idle session to `local_command --resume` in Terminal |
| `/cc help` | Show Claude session commands |

Claude ACP tasks open progress first and then run in the background. One follow-up message may be queued and runs after either success or failure. `/cancel` withdraws it, `/stop` stops the current task, and `/guide` is not supported for Claude ACP.

WeClaw does not scan `~/.claude` transcripts. Without a valid binding, normal messages require `/cc ls` or `/cc new`; no session is created implicitly.

### Aliases

| Alias | Agent |
|-------|-------|
| `/cc` | claude |
| `/cx` | codex |
| `/cs` | cursor |
| `/km` | kimi |
| `/gm` | gemini |
| `/ocd` | opencode |
| `/oc` | openclaw |

You can also define custom aliases per agent in config:

```json
{
  "agents": {
    "claude": {
      "type": "acp",
      "aliases": ["ai", "c"]
    }
  }
}
```

Then `/ai hello` or `/c hello` will route to claude.

Switching default agent is persisted to config — survives restarts.

## Media Messages

WeClaw supports sending images, videos, files, and voice messages to/from WeChat.

**Voice messages:** When you send a voice message in WeChat, WeClaw automatically uses WeChat's speech-to-text transcription and forwards the text to the AI agent. Duplicate voice message events are automatically deduplicated.

**From agent replies:** When an AI agent returns markdown with images (`![](url)`), WeClaw automatically extracts the image URLs, downloads them, uploads to WeChat CDN (AES-128-ECB encrypted), and sends them as image messages.

**Markdown handling:** Agent responses are automatically converted from markdown to plain text for WeChat display — code fences are stripped, links show display text only, bold/italic markers are removed, etc.

## Proactive Messaging

Send messages to WeChat users without waiting for them to message first.

**CLI:**

```bash
# Send text
weclaw wechat send --to "user_id@im.wechat" --text "Hello from weclaw"

# Send image
weclaw wechat send --to "user_id@im.wechat" --media "https://example.com/photo.png"

# Send text + image
weclaw wechat send --to "user_id@im.wechat" --text "Check this out" --media "https://example.com/photo.png"

# Send file
weclaw wechat send --to "user_id@im.wechat" --media "https://example.com/report.pdf"
```

**HTTP API** (runs on `127.0.0.1:18011` when `weclaw start` is running):

```bash
# Send text
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "text": "Hello from weclaw"}'

# Send image
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "media_url": "https://example.com/photo.png"}'

# Send text + media
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "text": "See this", "media_url": "https://example.com/photo.png"}'

# Multi-account platforms must specify the outbound account; Feishu uses the bot app_id
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"platform": "feishu", "account_id": "cli_xxx", "to": "ou_xxx", "text": "Hello"}'
```

Supported media types: images (png, jpg, gif, webp), videos (mp4, mov), files (pdf, doc, zip, etc.).

When a platform has multiple outbound accounts, `/api/send` requires `account_id`; otherwise it returns 400 to avoid sending through the wrong bot or account.

Set `WECLAW_API_ADDR` to change the listen address (e.g. `0.0.0.0:18011`).

## Configuration

Config file: `~/.weclaw/config.json`

```json
{
  "default_agent": "claude",
  "agents": {
    "claude": {
      "type": "acp",
      "command": "/usr/local/bin/claude-agent-acp",
      "local_command": "/usr/local/bin/claude",
      "env": {
        "ANTHROPIC_API_KEY": "sk-ant-xxx"
      },
      "model": "sonnet"
    },
    "codex": {
      "type": "acp",
      "command": "/usr/local/bin/codex-acp",
      "env": {
        "OPENAI_API_KEY": "sk-xxx"
      }
    },
    "openclaw": {
      "type": "http",
      "endpoint": "https://api.example.com/v1/chat/completions",
      "api_key": "sk-xxx",
      "model": "openclaw:main"
    }
  }
}
```

Environment variables:
- `WECLAW_DEFAULT_AGENT` — override default agent
- `WECLAW_PROGRESS_MODE` — 覆盖微信进度模式，例如 `summary`、`typing`、`stream`
- `WECLAW_PROGRESS_SUMMARY_INTERVAL_SECONDS` — 覆盖进度摘要发送间隔
- `WECLAW_PROGRESS_MAX_MESSAGES` — 覆盖单次任务最多发送的中间进度条数
- `OPENCLAW_GATEWAY_URL` — OpenClaw HTTP fallback endpoint
- `OPENCLAW_GATEWAY_TOKEN` — OpenClaw API token

### 多平台配置

`platforms` defaults to the legacy behavior: WeChat only. Feishu must be explicitly enabled with a non-empty `bots[]`; `enabled=true` without bots fails config validation. Each bot has its own `allowed_users`; an empty allowlist denies all inbound messages.

For first-time setup, use this command to generate the config shape below and store the secret in the dedicated credential file:

```bash
weclaw feishu bootstrap --name project-a --app-id cli_xxx --app-secret xxx --allowed-users ou_xxx --default-agent codex --progress stream
```

```json
{
  "default_agent": "codex",
  "platforms": {
    "wechat": {
      "enabled": true,
      "allowed_users": ["user_id@im.wechat"],
      "message_aggregation_ms": 800,
      "progress": {"mode": "typing"}
    },
    "feishu": {
      "enabled": true,
      "bots": [
        {
          "name": "project-a",
          "app_id": "cli_xxx",
          "allowed_users": ["ou_xxx"],
          "default_agent": "codex",
          "progress": {"mode": "stream"}
        }
      ]
    }
  }
}
```

Security note: allowlisted users can drive local shell agents to read files, run commands, or modify code. Configure `allowed_users` explicitly before production use.

WeChat `message_aggregation_ms` defaults to 800 and can be disabled with `0`. Feishu `bots[].default_agent`, `bots[].progress`, and `bots[].allowed_users` are isolated by `app_id` and support soft hot reload. Adding/removing a bot or changing `app_id` still requires restart.

### 微信进度反馈

默认配置使用 `typing` 模式：微信只显示“正在输入”和最终回复，不额外发送中间文字气泡。飞书在 `typing` 模式下使用 thinking 卡片，`stream`/`summary` 模式下使用 CardKit 卡片更新；`off` 模式只发送最终结果。

```json
{
  "default_agent": "codex",
  "progress": {
    "mode": "typing",
    "send_acceptance": false,
    "enable_typing": true,
    "typing_heartbeat_seconds": 8,
    "initial_delay_seconds": 10,
    "summary_interval_seconds": 20,
    "max_progress_messages": 4,
    "show_text_preview": false
  },
  "agents": {
    "codex": {
      "type": "acp",
      "command": "codex",
      "args": ["app-server", "--listen", "stdio://"]
    }
  }
}
```

如果需要低频文字进度，可以切到 `summary`；如果需要恢复旧实时正文流，可以切到 `stream`。

可选模式：

| 模式 | 行为 |
|------|------|
| `off` | 不发送输入中状态和中间进度，只发送最终结果 |
| `typing` | 发送输入中状态和最终结果 |
| `summary` | 发送受理确认、输入中状态、低频摘要和最终结果 |
| `verbose` | 预留的更详细摘要模式，当前按 summary 处理 |
| `stream` | 恢复旧的实时正文片段预览 |
| `debug` | 预留的内部调试模式，当前按 summary 处理 |

如果需要恢复旧实时正文流：

```json
{
  "progress": {
    "mode": "stream",
    "show_text_preview": true,
    "summary_interval_seconds": 5,
    "preview_runes": 180
  }
}
```

When `/progress <mode>` is sent from Feishu, it only changes the current bot account's progress mode; other Feishu bots and WeChat settings are not affected.

每个 Agent 可以覆盖全局进度配置：

```json
{
  "progress": {
    "mode": "summary"
  },
  "agents": {
    "claude": {
      "type": "acp",
      "command": "claude-agent-acp",
      "local_command": "claude",
      "progress": {
        "mode": "typing"
      }
    },
    "codex": {
      "type": "acp",
      "command": "codex",
      "args": ["app-server"],
      "progress": {
        "mode": "stream"
      }
    }
  }
}
```

Custom agent CLI environment variables:

```json
{
  "default_agent": "...",
  "agents": {
    "...": {
      ...
      "env": {
        "ENV_NAME": "ENV_VALUE"
      }
    },
  }
}
```

### Permission bypass

By default, some agents require interactive permission approval which doesn't work in WeChat. Add `args` to your agent config to bypass:

| Agent | Flag | What it does |
|-------|------|-------------|
| Codex (CLI) | `--skip-git-repo-check` | Allow running outside git repos |

Example:

```json
{
  "codex": {
    "type": "cli",
    "command": "/usr/local/bin/codex",
    "cwd": "/home/user/my-project",
    "args": ["--skip-git-repo-check"]
  }
}
```

Set `cwd` to specify the agent's working directory (workspace). If omitted, defaults to `~/.weclaw/workspace`.

> **Warning:** These flags disable safety checks. Claude remote access does not support CLI mode. ACP Codex agents use `permission_level` and do not need CLI permission-bypass flags.

When omitted, ACP Codex `permission_level` behaves as `default`. When set, it accepts only three values:

| Level | Codex mapping | What it does |
|-------|---------------|--------------|
| `default` | `workspace-write` + `on-request` + `user` reviewer | Recommended default. Work inside the workspace automatically and ask through Feishu before crossing the boundary. |
| `auto_review` | `workspace-write` + `on-request` + `auto_review` reviewer | Let Codex auto-review boundary-crossing approvals without expanding the sandbox. |
| `full_access` | `danger-full-access` + `never` | Run without sandbox restrictions or approval prompts. Use only in trusted environments. |

Old levels such as `request_approval` and `auto_approval` are no longer accepted; startup fails fast when they are configured.

## Security & Governance

WeClaw drives AI agents that can execute shell commands and read/write files. Anyone who can message the bot can drive that agent, so harden access before exposing it.

```json
{
  "allowed_workspace_roots": ["/home/me/projects"],
  "admin_users": ["user_id@im.wechat", "ou_xxx"],
  "rate_limit_per_minute": 20,
  "audit_log": true,
  "platforms": {
    "wechat": { "enabled": true, "allowed_users": ["user_id@im.wechat"] },
    "feishu": {
      "enabled": true,
      "bots": [
        { "name": "project-a", "app_id": "cli_xxx", "allowed_users": ["ou_xxx"] }
      ]
    }
  }
}
```

- **Access control (`allowed_users`)**: WeChat uses a platform-level allowlist; Feishu uses per-bot allowlists in `bots[]`. Empty allowlist = deny everyone (fail-safe) — WeClaw warns loudly at startup if unset.
- **Admin allowlist (`admin_users`)**: top-level allowlist for WeClaw management commands. A user must be present in both the platform `allowed_users` and top-level `admin_users` to run `/update`, `/upgrade`, `/restart`, or `/restart --force` from WeChat / Feishu. Empty = remote management disabled.
- **Workspace confinement (`allowed_workspace_roots`)**: regular users may only `/cwd` into these roots and their subdirectories. Empty roots reject regular-user remote directory switching. Users in `admin_users` are exempt from this allowlist.
- **Rate limiting (`rate_limit_per_minute`)**: max agent invocations per user per minute. `0` = off.
- **Audit log (`audit_log` / `audit_log_path`)**: structured JSON-Lines record of who triggered which agent, yolo auto-approvals, etc. (never contains secrets). Defaults on, written to `~/.weclaw/audit.log` with size-based rotation.
- **OS-user isolation (`run_as_user` / `run_as_env`)**: run a specific agent under a separate Unix user via passwordless `sudo` for filesystem isolation.
- **Codex permission level (`permission_level`)**: `default` uses workspace sandboxing plus manual approval, `auto_review` uses Codex auto-review, and `full_access` disables the sandbox boundary.
- **Session approval mode (`/mode`)**: `yolo` only makes the current user auto-approve Codex approval requests; `default` asks via interactive buttons (Feishu) and fail-safe denies on timeout.

Remote management commands are executed by WeClaw itself, not by Codex / Claude or another configured agent. `/update` and `/upgrade` call the current WeClaw binary self-update flow; `/restart` replies first, then asynchronously triggers `weclaw restart` so the service does not exit before the message is sent.

```json
{
  "agents": {
    "codex": { "type": "cli", "command": "codex", "run_as_user": "coder-bot", "run_as_env": ["OPENAI_API_KEY"] }
  }
}
```

### Pre-flight check

```bash
weclaw doctor
```

`weclaw doctor` validates config before you rely on it: agent binaries resolvable, platform credentials present, empty-allowlist warnings, API token required for non-loopback, `run_as_user` passwordless-sudo probe, workspace confinement, and audit-log writability. Exits non-zero on blocking issues.

## Web Config Panel

Run a local browser-based config panel instead of hand-editing `~/.weclaw/config.json`:

```bash
weclaw web                 # serves on 127.0.0.1:39282, prints a tokenized local URL, opens the browser
weclaw web --no-open       # don't auto-open the browser
weclaw web --addr 127.0.0.1:39282 --token <token>
```

The panel lets you edit security settings (`allowed_workspace_roots`, `rate_limit_per_minute`, audit), agents, Codex permission fields, write Feishu credentials, validate them, and complete WeChat QR login in-page.

**Security:** the panel reads/writes files containing shell-capable agent config and secrets, so by default it binds loopback only, requires a token (auto-generated for loopback; mandatory when binding a non-loopback address), enforces same-origin checks, and **never echoes secrets** (API token, agent api_key/env values, Feishu app_secret are masked; submitting the mask keeps the stored value). Codex `permission_level`, `approval_policy`, `approval_reviewer`, and `sandbox_mode` are preserved when saving config. Config is written atomically (`0600`). Soft config (agents/progress/allowed_users/admin_users/workspace roots/rate limit) is hot-reloaded by a running `weclaw start`; platform enable/credential changes (incl. newly scanned WeChat accounts) require `weclaw restart`.

## Background Mode

```bash
# Start (runs in background by default)
weclaw start

# Check if running
weclaw status

# Stop
weclaw stop

# Run in foreground (for debugging)
weclaw start -f
```

Logs are written to `~/.weclaw/weclaw.log`.

### System service (auto-start on boot)

**macOS (launchd):**

```bash
cp service/com.fastclaw.weclaw.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.fastclaw.weclaw.plist
```

**Linux (systemd):**

```bash
sudo cp service/weclaw.service /etc/systemd/system/
sudo systemctl enable --now weclaw
```

## Docker

```bash
# Build
docker build -t weclaw .

# Login (interactive — scan QR code)
docker run -it -v ~/.weclaw:/root/.weclaw weclaw wechat login

# Start with HTTP agent
docker run -d --name weclaw \
  -v ~/.weclaw:/root/.weclaw \
  -e OPENCLAW_GATEWAY_URL=https://api.example.com \
  -e OPENCLAW_GATEWAY_TOKEN=sk-xxx \
  weclaw

# View logs
docker logs -f weclaw
```

> Note: ACP and CLI agents require the agent binary inside the container.
> The Docker image ships only WeClaw itself. For ACP/CLI agents, mount
> the binary or build a custom image. HTTP agents work out of the box.

## Release

```bash
# Build and verify release assets without creating a tag or GitHub Release
scripts/release.sh --next-patch --dry-run

# Create the next patch release locally
scripts/release.sh --next-patch

# Or publish an explicit version
scripts/release.sh v0.1.48
```

发布脚本会检查工作区、运行验证、构建 `darwin/arm64` 二进制、生成 `checksums.txt`、推送 tag、创建 GitHub Release，并验证上传资产。只有在已经完成等价验证后，才使用 `--skip-tests`。

GitHub Actions Release workflow 仅作为手动兜底入口，触发时必须输入已存在的 `vX.Y.Z` tag。推送 tag 不会自动发布，避免与本地发布脚本并发创建同一个稳定版 Release。

## Update

```bash
# Update to the latest version (does not restart by default)
weclaw update

# Restart immediately after updating
weclaw update --restart

# Check current version
weclaw version
```

## Development

```bash
# Hot reload
make dev

# Build
go build -o weclaw .

# Run
./weclaw start
```

## Contributors

<a href="https://github.com/fastclaw-ai/weclaw/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=fastclaw-ai/weclaw" />
</a>

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=fastclaw-ai/weclaw&type=Timeline)](https://star-history.com/#fastclaw-ai/weclaw&Timeline)

## License

[AGPL-3.0-or-later](LICENSE)
