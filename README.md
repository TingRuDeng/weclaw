# WeClaw

[中文文档](README_CN.md)

WeChat AI Agent Bridge — connect WeChat to AI agents (Claude, Codex, Gemini, Kimi, etc.).

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

Use `weclaw login` to add additional WeChat accounts.

飞书接入默认关闭。启用前先保存并校验飞书应用凭证：

```bash
weclaw feishu login --app-id cli_xxx --app-secret xxx
weclaw feishu status
```

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
| CLI  | Spawns a new process per message. Supports session resume via `--resume`. | Claude (`claude -p`), Codex (`codex exec`) |
| HTTP | OpenAI-compatible chat completions API. | OpenClaw (HTTP fallback) |
| Companion | WeClaw keeps the WeChat bridge in the background while a local visible CLI terminal stays attached. | OpenCode, Codex app-server |

Auto-detection picks ACP over CLI when both are available. OpenCode is detected as Companion mode. Codex still defaults to ACP so `/codex ls`, `/codex switch`, and model queries keep their existing behavior. Configure Codex Companion explicitly when you want a visible local Codex terminal.

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

## Chat Commands

Send these as WeChat messages:

| Command | Description |
|---------|-------------|
| `hello` | Send to default agent |
| `/codex write a function` | Send to a specific agent |
| `/cc explain this code` | Send to agent by alias |
| `/cc help` | 查看 Claude 会话命令 |
| `/claude` | Switch default agent to Claude |
| `/cwd /path/to/project` | Switch workspace directory |
| `/new` | Start a new conversation (clear session) |
| `/status` | Show WeClaw runtime status |
| `/help` | Show help message |

### Codex 主路径

Codex 的推荐使用方式是微信 remote-first，本地接手入口按需打开：

| 命令 | 说明 |
| ---- | ---- |
| `/cx status` | 查看当前 workspace、thread、remote 和本地入口记录 |
| `/cx quota` | 查看 Codex 账号额度 |
| `/cx ls` | 查看 Codex 工作空间或当前工作空间会话 |
| `/cx <编号|..>` | 选择当前列表项，或返回上一级 |
| `/cx switch <编号>` | 切换当前工作空间会话 |
| `/cx cli` | 在本地 Terminal 打开当前 thread 的 Codex CLI |
| `/cx app` | 在 Codex App 中打开当前工作空间 |
| `/cx clean` | 清理已不存在的 WeClaw 工作空间记录 |
| `/cx help` | 查看 Codex 高级会话命令 |

本地 Terminal 或 Codex App 只是接手入口。手动关闭它们不会影响微信 remote 会话，`/cx status` 也不会实时探测本地窗口是否仍然存在。

### Claude 会话复用

Claude CLI 模式支持按工作空间复用 Claude Code session，并可从微信侧切换到本机已有会话：

| 命令 | 说明 |
| ---- | ---- |
| `/cc ls` | 查看 WeClaw 已记录和本机 Claude Code 可发现的可切换会话 |
| `/cc switch <编号|sessionId>` | 切换到指定 Claude session，编号来自 `/cc ls` |
| `/cc new` | 新建当前工作空间会话，下一条 Claude 消息会创建新 session |
| `/cc pwd` | 查看当前 Claude 工作空间 |
| `/cc status` | 查看当前工作空间、session 和 Agent 模式 |
| `/cc cli` | 在本地 Terminal 中用 `claude --resume` 接手当前 session |
| `/cc help` | 查看 Claude 会话命令 |

`/claude` 可作为 `/cc` 的兼容入口，例如 `/claude ls`。完整 `/cc switch` 体验依赖 Claude CLI Agent；如果 Claude 使用 ACP 模式，普通聊天仍可复用自身会话，但不会强行映射到 Claude Code 本机 session。

本机 Claude Code 历史来自 `~/.claude` 的只读扫描。WeClaw 只读取项目配置、session 文件名、mtime 和 transcript 首行摘要，不读取或展示完整 transcript 正文。

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
weclaw send --to "user_id@im.wechat" --text "Hello from weclaw"

# Send image
weclaw send --to "user_id@im.wechat" --media "https://example.com/photo.png"

# Send text + image
weclaw send --to "user_id@im.wechat" --text "Check this out" --media "https://example.com/photo.png"

# Send file
weclaw send --to "user_id@im.wechat" --media "https://example.com/report.pdf"
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
```

Supported media types: images (png, jpg, gif, webp), videos (mp4, mov), files (pdf, doc, zip, etc.).

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

`platforms` 缺省时保持旧行为：只启用微信。飞书需要显式启用；`allowed_users` 为空时默认拒绝所有入站消息。

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
      "allowed_users": ["ou_xxx"],
      "default_agent": "codex",
      "progress": {"mode": "stream"}
    }
  }
}
```

安全提示：白名单用户可以驱动本机 shell agent 读取文件、运行命令或修改代码。生产使用时必须显式配置 `allowed_users`。

微信 `message_aggregation_ms` 默认 800，设置为 `0` 可关闭。`default_agent`、`progress` 和 `allowed_users` 支持软配置热重载；平台启用状态和平台凭证仍需重启生效。

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

每个 Agent 可以覆盖全局进度配置：

```json
{
  "progress": {
    "mode": "summary"
  },
  "agents": {
    "claude": {
      "type": "cli",
      "command": "claude",
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
| Claude (CLI) | `--dangerously-skip-permissions` | Skip all tool permission prompts |
| Codex (CLI) | `--skip-git-repo-check` | Allow running outside git repos |

Example:

```json
{
  "claude": {
    "type": "cli",
    "command": "/usr/local/bin/claude",
    "cwd": "/home/user/my-project",
    "args": ["--dangerously-skip-permissions"]
  },
  "codex": {
    "type": "cli",
    "command": "/usr/local/bin/codex",
    "cwd": "/home/user/my-project",
    "args": ["--skip-git-repo-check"]
  }
}
```

Set `cwd` to specify the agent's working directory (workspace). If omitted, defaults to `~/.weclaw/workspace`.

> **Warning:** These flags disable safety checks. Only enable them if you understand the risks. ACP agents handle permissions automatically and don't need these flags.

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
docker run -it -v ~/.weclaw:/root/.weclaw weclaw login

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

The release script checks the working tree, runs validation, builds `darwin/linux/windows` x `amd64/arm64` binaries, generates `checksums.txt`, pushes the tag, creates a GitHub Release, and verifies the uploaded assets. Use `--skip-tests` only after an equivalent validation has already passed.

You can also trigger the GitHub Actions Release workflow manually, or push a `vX.Y.Z` tag to let Actions build and publish the same asset set.

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
