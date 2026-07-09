# WeClaw

[English](README.md)

微信 & 飞书 AI Agent 桥接器 — 将微信（个人号）和飞书消息接入 AI Agent（Claude、Codex、Gemini、Kimi 等）。

> 本项目参考 [@tencent-weixin/openclaw-weixin](https://npmx.dev/package/@tencent-weixin/openclaw-weixin) 实现，仅限个人学习，勿做他用。

|                                                 |                                                 |                                                 |
| :---------------------------------------------: | :---------------------------------------------: | :---------------------------------------------: |
| <img src="previews/preview1.png" width="280" /> | <img src="previews/preview2.png" width="280" /> | <img src="previews/preview3.png" width="280" /> |

## 快速开始

```bash
# 一键安装
curl -sSL https://raw.githubusercontent.com/TingRuDeng/weclaw/main/install.sh | sh

# 私有仓库安装
export GITHUB_TOKEN=ghp_xxx
curl -H "Authorization: Bearer $GITHUB_TOKEN" -sSL https://raw.githubusercontent.com/TingRuDeng/weclaw/main/install.sh | sh

# 启动
weclaw start
```

就这么简单。首次启动时，WeClaw 会：

1. 读取 `~/.weclaw/config.json`
2. 自动检测已安装的 AI Agent（Claude、Codex、Gemini 等）
3. 启动已启用的平台
4. 开始接收和回复微信 / 飞书消息

微信需要扫码登录时，先执行 `weclaw wechat login`；仅启用飞书时，启动不会要求登录微信。

飞书接入默认关闭。需要启用时可用交互式命令添加机器人：

```bash
weclaw feishu add
weclaw feishu status --name project-a
```

`weclaw feishu add` 会保存飞书凭证并更新 `platforms.feishu.bots[]`，适合首次配置。飞书 `open_id` 是应用级身份，同一个人在不同机器人应用下不同；多机器人建议在 `allowed_users` 使用同开发商下稳定的 `union_id`。若本机安装了官方 `lark-cli`，命令会提示继续用它检查应用权限、事件订阅和消息发送能力；WeClaw 运行时仍使用内置飞书 SDK 长连接，不依赖 `lark-cli`。

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

### 其他安装方式

```bash
# 通过 Go 安装
go install github.com/fastclaw-ai/weclaw@latest

# 通过 Docker
docker run -it -v ~/.weclaw:/root/.weclaw ghcr.io/fastclaw-ai/weclaw start
```

## 架构

<p align="center">
  <img src="previews/architecture.png" width="600" />
</p>

**Agent 接入模式：**

| 模式 | 工作方式                                                         | 支持的 Agent                                            |
| ---- | ---------------------------------------------------------------- | ------------------------------------------------------- |
| ACP  | 长驻子进程，通过 stdio JSON-RPC 通信。速度最快，复用进程和会话。 | Claude, Codex, Kimi, Gemini, Cursor, OpenClaw |
| CLI  | 每条消息启动一个新进程，支持通过 `--resume` 恢复会话。           | Claude (`claude -p`)、Codex (`codex exec`)              |
| HTTP | OpenAI 兼容的 Chat Completions API。                             | OpenClaw（HTTP 回退）                                   |
| Companion | WeClaw 后台接微信，本地终端保持可见 CLI 连接。              | OpenCode                                               |

同时存在 ACP 和 CLI 时，自动优先选择 ACP。Codex 默认使用 `app-server --listen stdio://` 的 remote-first 模式：微信侧独立使用当前 workspace/thread，本地需要接手时再通过 `/cx cli` 或 `/cx app` 打开本地入口。

OpenCode Companion 属于高级能力：先启动 WeClaw，再在同一个工作空间终端执行：

```bash
weclaw companion --agent opencode --cwd /path/to/project
```

## 平台能力对照

| 能力 | 微信（个人号） | 飞书 / Lark |
|------|:--------------:|:-----------:|
| 文本 & 斜杠命令 | ✅ | ✅ |
| 图片收发 | ✅ | ✅ |
| 文件收发 | ✅ | ✅ |
| 语音转文字（入站） | ✅（微信转写） | ⚠️ 作为文件接收，无自动转写 |
| 富文本卡片 | ❌ | ✅（CardKit） |
| 流式（打字机） | ❌ 降级为 typing + 文本 | ✅ CardKit 流式 |
| 交互按钮 | ❌ 降级为编号文本 | ✅（选择 / 审批） |
| 群聊 | ❌ 仅单聊 | ✅（需 @bot 触发） |
| 主动发消息 | ✅ | ✅（文本） |
| 登录 | 扫码（`weclaw wechat login`） | app_id/secret（`weclaw feishu login`） |

业务逻辑（命令、agent 路由、会话、进度）与平台无关；各平台 adapter 按自身能力优雅降级。

## 聊天命令

在微信或飞书中发送以下命令：

| 命令                    | 说明                     |
| ----------------------- | ------------------------ |
| `你好`                  | 发送给默认 Agent         |
| `/codex 写一个排序函数` | 发送给指定 Agent         |
| `/cc 解释一下这段代码`  | 通过别名发送             |
| `/cc help`              | 查看 Claude 会话命令     |
| `/cwd /path/to/project` | 切换工作目录（普通用户限制在 `allowed_workspace_roots` 白名单内；管理员不受此限制） |
| `/new`                  | 开始新对话（清除会话）   |
| `/model` / `/model <id>` | 查看 / 切换模型（Codex：运行时切换，下个新会话生效） |
| `/reasoning` / `/reasoning <强度>` | 查看 / 切换推理强度（Codex） |
| `/mode` / `/mode yolo` / `/mode default` | 查看 / 本用户自动同意 / 按钮确认 Codex 审批请求 |
| `/ps`                   | 查看自己运行中的任务     |
| `/stop`                 | 停止当前运行的任务       |
| `/update`               | 管理员远程更新 WeClaw（需配置 `admin_users`） |
| `/restart` / `/restart --force` | 管理员远程重启 WeClaw（需配置 `admin_users`） |
| `/feishu users pending` / `/feishu users approve-code <授权码>` / `/feishu users revoke <用户ID>` | 管理员确认或取消飞书用户授权 |
| `/status`               | 查看运行态（agent、uptime、运行中任务、调用/错误计数、模式、限流） |
| `/help`                 | 查看帮助信息             |

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
| ⚙️ 控制 | 运行任务 | `/ps` |
| ⚙️ 控制 | 引导任务 | `/guide` |
| ⚙️ 控制 | 停止任务 | `/stop` |
| ⚙️ 控制 | 更新 WeClaw | `/update` |
| ⚙️ 控制 | 重启 WeClaw | `/restart` |

普通计划确认和暂存消息确认都直接回复“确认”。`/cancel` 只撤回暂存消息，不建议放入固定菜单；停止运行中任务请用 `/stop`。

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

完整 `/cc switch` 体验依赖 Claude CLI Agent；如果 Claude 使用 ACP 模式，普通聊天仍可复用自身会话，但不会强行映射到 Claude Code 本机 session。

本机 Claude Code 历史来自 `~/.claude` 的只读扫描。WeClaw 只读取项目配置、session 文件名、mtime 和 transcript 首行摘要，不读取或展示完整 transcript 正文。

### 快捷别名

| 别名   | Agent    |
| ------ | -------- |
| `/cc`  | Claude   |
| `/cx`  | Codex    |
| `/cs`  | Cursor   |
| `/km`  | Kimi     |
| `/gm`  | Gemini   |
| `/ocd` | OpenCode |
| `/oc`  | OpenClaw |

也可以在配置文件中为每个 Agent 自定义触发命令：

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

然后 `/ai 你好` 或 `/c 你好` 就会路由到 claude。

切换默认 Agent 会写入配置文件，重启后仍然生效。

## 富媒体消息

WeClaw 支持收发图片、视频、文件和语音消息。

**语音消息：** 在微信中发送语音消息时，WeClaw 会自动使用微信的语音转文字功能，将转写后的文本发送给 AI Agent。重复的语音消息事件会自动去重。

**Agent 回复自动处理：** 当 AI Agent 返回包含图片的 markdown（`![](url)`）时，WeClaw 会自动提取图片 URL，下载文件，上传到微信 CDN（AES-128-ECB 加密），然后作为图片消息发送。

**Markdown 转换：** Agent 的回复会自动从 markdown 转为纯文本再发送 — 代码块去掉围栏、链接只保留文字、加粗斜体标记去除等。

## 高级能力：主动推送消息

无需等待用户发消息，主动向微信用户推送消息。

**命令行：**

```bash
# 发送文本
weclaw wechat send --to "user_id@im.wechat" --text "你好，来自 weclaw"

# 发送图片
weclaw wechat send --to "user_id@im.wechat" --media "https://example.com/photo.png"

# 发送文本 + 图片
weclaw wechat send --to "user_id@im.wechat" --text "看看这个" --media "https://example.com/photo.png"

# 发送文件
weclaw wechat send --to "user_id@im.wechat" --media "https://example.com/report.pdf"
```

**HTTP API**（`weclaw start` 运行时，默认监听 `127.0.0.1:18011`）：

```bash
# 发送文本
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "text": "你好，来自 weclaw"}'

# 发送图片
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "media_url": "https://example.com/photo.png"}'

# 发送文本 + 媒体
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "user_id@im.wechat", "text": "看看这个", "media_url": "https://example.com/photo.png"}'

# 多账号平台需要指定账号；飞书账号使用 bot 的 app_id
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"platform": "feishu", "account_id": "cli_xxx", "to": "on_xxx", "text": "你好"}'
```

支持的媒体类型：图片（png、jpg、gif、webp）、视频（mp4、mov）、文件（pdf、doc、zip 等）。

当同一平台配置了多个可主动发送账号时，HTTP API 必须传 `account_id`，否则会返回 400，避免消息发到错误的机器人或账号。

设置 `WECLAW_API_ADDR` 环境变量可更改监听地址（如 `0.0.0.0:18011`）。

## 配置

配置文件路径：`~/.weclaw/config.json`

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

### 多平台配置

`platforms` 缺省时保持旧行为：只启用微信。飞书需要显式启用并配置非空 `bots[]`；`enabled=true` 但没有 bot 会在配置校验阶段失败。每个 bot 的 `allowed_users` 为空时默认拒绝所有入站消息。

首次配置可以用命令生成下面这段结构，并把 secret 保存到独立凭证文件：

```bash
weclaw feishu bootstrap --name project-a --app-id cli_xxx --app-secret xxx --allowed-users on_xxx --default-agent codex --progress stream
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
          "allowed_users": ["on_xxx"],
          "default_agent": "codex",
          "progress": {"mode": "stream"}
        }
      ]
    }
  }
}
```

安全提示：白名单用户可以驱动本机 shell agent 读取文件、运行命令或修改代码。生产使用时必须显式配置 `allowed_users`，不要把 bot 暴露给不可信用户。

微信 `message_aggregation_ms` 默认 800，表示 800ms 内同一用户的连续非命令消息会合并；设置为 `0` 可关闭。飞书 `bots[].default_agent`、`bots[].progress` 和 `bots[].allowed_users` 按 `app_id` 隔离并支持软配置热重载；`allowed_users` 可填应用级 `open_id` 或同开发商下稳定的 `union_id`，多机器人优先使用 `union_id`；新增、删除 bot 或修改 `app_id` 仍需重启生效。

飞书未授权用户给任意 bot 发消息时，WeClaw 会把 `open_id/user_id/union_id` 记录到 `~/.weclaw/feishu-identities.json`，但不会自动放行。拒绝提示会返回短期授权码；管理员可在飞书里发送 `/feishu users approve-code <授权码>`，或本机执行 `weclaw feishu users approve-code <授权码>`，把稳定身份写入已配置 bot 的 `allowed_users`；需要限定单个 bot 时加 `--bot <name|app_id>`，需要同时加入远程管理白名单时加 `--admin`。本机可用 `weclaw feishu users pending` 查看待处理授权请求，用 `weclaw feishu users list` 查看已授权历史记录，用 `weclaw feishu users revoke <用户ID> [--bot <name|app_id>] [--admin]` 取消授权，也可用 `weclaw feishu users rename <id> <显示名>` 手动补全姓名。

微信未授权用户发消息时，也会收到短期授权码。管理员在本机执行 `weclaw wechat users approve-code <授权码>` 可把该微信用户写入 `platforms.wechat.allowed_users`；需要同时加入远程管理白名单时加 `--admin`。

环境变量：

- `WECLAW_DEFAULT_AGENT` — 覆盖默认 Agent
- `WECLAW_PROGRESS_MODE` — 覆盖微信进度模式，例如 `summary`、`typing`、`stream`
- `WECLAW_PROGRESS_SUMMARY_INTERVAL_SECONDS` — 覆盖进度摘要发送间隔
- `WECLAW_PROGRESS_MAX_MESSAGES` — 覆盖单次任务最多发送的中间进度条数
- `OPENCLAW_GATEWAY_URL` — OpenClaw HTTP 回退地址
- `OPENCLAW_GATEWAY_TOKEN` — OpenClaw API Token

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

在飞书里发送 `/progress <mode>` 时，只会修改当前机器人账号的进度模式；其他飞书机器人和微信配置不受影响。

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

自定义 agent cli 环境变量

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

### 权限配置

部分 Agent 默认需要交互式权限确认，在微信场景下无法操作会导致卡住。可通过 `args` 配置跳过：

| Agent | 参数 | 说明 |
|-------|------|------|
| Claude (CLI) | `--dangerously-skip-permissions` | 跳过所有工具权限确认 |
| Codex (CLI) | `--skip-git-repo-check` | 允许在非 git 仓库目录运行 |

配置示例：

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

通过 `cwd` 指定 Agent 的工作目录（workspace）。不设置则默认为 `~/.weclaw/workspace`。

> **注意：** 这些参数会跳过安全检查，请了解风险后再启用。ACP 模式的 Codex Agent 使用 `permission_level` 控制权限边界，无需通过 CLI 参数跳过审批。

ACP Codex 的 `permission_level` 不配置时等同于 `default`，显式配置时只支持三档：

| 档位 | Codex 映射 | 说明 |
|------|------------|------|
| `default` | `workspace-write` + `on-request` + `user` reviewer | 推荐默认值；工作区内自动执行，越界时走飞书审批。 |
| `auto_review` | `workspace-write` + `on-request` + `auto_review` reviewer | 由 Codex 自动审查越界审批；不扩大 sandbox。 |
| `full_access` | `danger-full-access` + `never` | 全权限执行，不弹审批；仅在可信环境使用。 |

旧档位 `request_approval`、`auto_approval` 不再兼容；配置后启动会报错。

## 安全与治理

WeClaw 驱动的 AI Agent 能执行 shell 命令、读写文件。任何能给 bot 发消息的人都能驱动该 Agent，因此对外暴露前务必收紧访问。

```json
{
  "allowed_workspace_roots": ["/home/me/projects"],
  "admin_users": ["user_id@im.wechat", "on_xxx"],
  "rate_limit_per_minute": 20,
  "audit_log": true,
  "platforms": {
    "wechat": { "enabled": true, "allowed_users": ["user_id@im.wechat"] },
    "feishu": {
      "enabled": true,
      "bots": [
        { "name": "project-a", "app_id": "cli_xxx", "allowed_users": ["on_xxx"] }
      ]
    }
  }
}
```

- **访问控制 (`allowed_users`)**：微信按平台配置，飞书按 `bots[]` 内每个机器人配置。飞书可填应用级 `open_id` 或同开发商下稳定的 `union_id`；多机器人优先使用 `union_id`。白名单为空 = 拒绝所有（fail-safe）；未配置时启动会显著告警。飞书自动发现只记录待确认身份，必须由管理员执行 `/feishu users approve` 后才写入白名单。
- **管理员白名单 (`admin_users`)**：顶层配置。只有同时位于对应平台 `allowed_users` 和顶层 `admin_users` 的用户，才能在微信 / 飞书执行 `/update`、`/restart`、`/restart --force` 和飞书用户授权命令。飞书管理员匹配会同时检查 `open_id/user_id/union_id`；为空 = 禁用远程管理命令。
- **工作目录限制 (`allowed_workspace_roots`)**：普通用户 `/cwd` 只能切到白名单根目录及其子目录；为空时普通用户远程切换目录会被拒绝。`admin_users` 中的管理员不受此白名单限制。
- **限流 (`rate_limit_per_minute`)**：每用户每分钟最多触发 agent 次数，`0` = 不限。
- **审计日志 (`audit_log` / `audit_log_path`)**：JSON Lines 记录谁触发了哪个 agent、yolo 自动放行等（不含密钥）。默认开启，写入 `~/.weclaw/audit.log`，按大小自动轮转。
- **OS 用户隔离 (`run_as_user` / `run_as_env`)**：通过免密 `sudo` 让指定 agent 以独立 Unix 用户运行，做文件系统隔离。
- **Codex 权限档位 (`permission_level`)**：`default` 走工作区 sandbox + 人工审批，`auto_review` 使用 Codex 自动审查，`full_access` 关闭 sandbox 边界。
- **会话审批模式 (`/mode`)**：`yolo` 只让当前用户自动同意 Codex 审批请求；`default` 弹按钮确认（飞书），超时 fail-safe 拒绝。

远程管理命令由 WeClaw 自身执行，不会进入 Codex / Claude 等 Agent：`/update` 调用当前 WeClaw 二进制的自更新逻辑；`/restart` 会先回复，再异步触发 `weclaw restart`，避免消息尚未发出时服务退出。

```json
{
  "agents": {
    "claude": { "type": "cli", "command": "claude", "run_as_user": "coder-bot", "run_as_env": ["ANTHROPIC_API_KEY"] }
  }
}
```

### 启动前体检

```bash
weclaw doctor
```

`weclaw doctor` 在你依赖配置前做预检：agent 二进制是否可达、平台凭证是否存在、空白名单告警、非回环地址必须配 token、`run_as_user` 免密 sudo 探测、工作目录限制、审计日志可写性。发现阻断性问题时以非零码退出。

## Web 配置面板

不想手编 `~/.weclaw/config.json` 时，可用本机网页面板：

```bash
weclaw web                 # 监听 127.0.0.1:39282，打印带 token 的本地 URL 并打开浏览器
weclaw web --no-open       # 不自动打开浏览器
weclaw web --addr 127.0.0.1:39282 --token <token>
```

面板可编辑安全配置、agent、Codex 权限字段、写入飞书凭证并校验、面板内完成微信扫码登录、查看运行状态。软配置（agent/进度/白名单/管理员白名单/工作目录/限流）由运行中的 `weclaw start` 热重载即时生效；平台启用/凭证变更（含新扫码的微信账号）需 `weclaw restart`。

**安全**：默认仅绑回环；非回环地址必须显式 token；同源防护；token 校验失败按来源限速；**密钥只写不回显**（api_token / agent api_key+env / 飞书 app_secret 均掩码，回写掩码即保持原值）；Codex `permission_level`、`approval_policy`、`approval_reviewer`、`sandbox_mode` 会随配置保存保留；`config.json` 原子写（`0600`）；微信登录二维码本机渲染、不外发第三方。

## 后台运行

```bash
# 启动（默认后台运行）
weclaw start

# 查看状态
weclaw status

# 停止
weclaw stop

# 前台运行（调试用）
weclaw start -f
```

日志输出到 `~/.weclaw/weclaw.log`。

### 系统服务（开机自启）

**macOS (launchd)：**

```bash
cp service/com.fastclaw.weclaw.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.fastclaw.weclaw.plist
```

**Linux (systemd)：**

```bash
sudo cp service/weclaw.service /etc/systemd/system/
sudo systemctl enable --now weclaw
```

## Docker

```bash
# 构建
docker build -t weclaw .

# 登录（交互式，扫描二维码）
docker run -it -v ~/.weclaw:/root/.weclaw weclaw wechat login

# 使用 HTTP Agent 启动
docker run -d --name weclaw \
  -v ~/.weclaw:/root/.weclaw \
  -e OPENCLAW_GATEWAY_URL=https://api.example.com \
  -e OPENCLAW_GATEWAY_TOKEN=sk-xxx \
  weclaw

# 查看日志
docker logs -f weclaw
```

> 注意：ACP 和 CLI 模式需要容器内有对应的 Agent 二进制文件。
> 默认镜像只包含 WeClaw 本体。如需使用 ACP/CLI Agent，请挂载二进制文件或构建自定义镜像。
> HTTP 模式开箱即用。

## 发版

```bash
# 只构建和校验发布产物，不创建 tag 或 GitHub Release
scripts/release.sh --next-patch --dry-run

# 本地创建下一个 patch 版本发布
scripts/release.sh --next-patch

# 或发布指定版本
scripts/release.sh v0.1.48
```

发布脚本会检查工作区、运行验证、构建 `darwin/arm64` 二进制、生成 `checksums.txt`、推送 tag、创建 GitHub Release、校验线上资产，并在 mac M 系列主机上用临时二进制执行一次 `weclaw update` smoke。只有在已经完成等价验证后，才使用 `--skip-tests`。

也可以手动触发 GitHub Actions Release workflow，或推送 `vX.Y.Z` tag 让 Actions 构建并发布同一组 `darwin/arm64` 资产。

## 更新

```bash
# 更新到最新版本（默认不自动重启）
weclaw update

# 更新完成后立即重启
weclaw update --restart

# 查看当前版本
weclaw version
```

## 开发

```bash
# 热重载
make dev

# 编译
go build -o weclaw .

# 运行
./weclaw start
```

## 贡献者

<a href="https://github.com/fastclaw-ai/weclaw/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=fastclaw-ai/weclaw" />
</a>

## Star 趋势

[![Star History Chart](https://api.star-history.com/svg?repos=fastclaw-ai/weclaw&type=Timeline)](https://star-history.com/#fastclaw-ai/weclaw&Timeline)

## 许可证

[AGPL-3.0-or-later](LICENSE)
