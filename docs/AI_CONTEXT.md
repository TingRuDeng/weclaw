---
ai_summary:
  purpose: "提供 WeClaw 代码结构、运行数据流、关键状态和验证命令的精简上下文地图。"
  read_when:
    - "需要修改 WeClaw 功能、命令、平台 adapter、Agent runtime、配置或发布流程时。"
    - "排查飞书、微信、Codex、Claude、主动发送 API 或更新发布问题时。"
  source_of_truth:
    - "cmd/start.go"
    - "messaging/handler.go"
    - "agent/agent.go"
    - "platform/platform.go"
    - "platform/message.go"
    - "config/config.go"
    - "scripts/release.sh"
  verify_with:
    - "go test ./... -count=1 -timeout 120s"
    - "go test -race ./agent ./cmd ./messaging -count=1 -timeout 60s"
    - "go vet ./..."
    - "python3 scripts/validate_docs.py . --profile generic"
  stale_when:
    - "顶层模块职责、消息数据流、配置字段、命令列表、发布流程或验证命令变化。"
---

# WeClaw AI 上下文

## Purpose

本文是 WeClaw 的当前代码上下文地图，帮助维护者和自动化编码代理快速定位源码事实、风险边界和验证命令。

## Source of truth

- 进程和命令入口：`main.go`、`cmd/root.go`、`cmd/start.go`
- 配置模型：`config/config.go`、`config/detect.go`
- Agent 抽象和运行时：`agent/agent.go`、`agent/acp_agent.go`、`agent/cli_agent.go`、`agent/http_agent.go`、`agent/companion_agent.go`
- 跨平台消息模型：`platform/platform.go`、`platform/message.go`、`platform/reply.go`、`platform/registry.go`
- 消息业务层：`messaging/handler.go`、`messaging/progress.go`、`messaging/codex_sessions.go`、`messaging/claude_sessions.go`
- 平台接入：`wechat/`、`ilink/`、`feishu/`
- 本机 API：`api/server.go`
- Web 配置界面：`web/`
- 发布和更新：`scripts/release.sh`、`cmd/update.go`、`cmd/restart_safety.go`

## Key facts

### 仓库形态

- 这是单一 Go 仓库，不是 coordination root；所有常规验证命令从仓库根目录执行。
- `go.mod` 声明模块为 `github.com/fastclaw-ai/weclaw`。
- 主要运行入口是 `cmd.Execute()`，默认命令会走 `runStart`。

### 启动数据流

- `cmd/start.go` 的 `runStart` 读取 `config.Load()`，必要时触发微信登录，然后创建 `messaging.Handler`。
- `buildPlatformRegistry` 根据配置启用微信和飞书平台；飞书默认需要显式启用并加载凭证。
- `platform.Registry.Run` 并发运行平台 adapter，并在分发前执行 `allowed_users` 访问控制。
- `api.NewServer` 和平台 registry 共享同一套 outbound replier；`/api/send` 用它主动发消息。
- `api.WithRuntimeStatusProvider(handler)` 让 CLI 在重启前能读取运行中任务数。

### 消息与会话

- 平台 adapter 把原始事件转换为 `platform.IncomingMessage`，再交给 `messaging.Handler.HandleMessage`。
- `platform.IncomingMessage.ConversationKey()` 用平台名前缀隔离不同 IM 平台的同名用户。
- 飞书真实发送者身份和 session routing 分离：真实用户用于访问控制与审计，`feishu_session_key` 只用于会话路由。
- Codex 推荐 remote-first：微信或飞书侧维护当前 workspace/thread，本地 Terminal 或 Codex App 只是接手入口。
- 运行中 Codex 长任务登记在 `Handler.activeTasks`，支持 `/ps` 查看和 `/stop` 取消。

### Agent runtime

- `agent.Agent` 是统一聊天接口，支持 `Chat`、`ResetSession`、`Info`、`SetCwd`。
- ACP runtime 适合 Claude、Codex、Gemini、Kimi、Cursor 等长驻 JSON-RPC 子进程。
- CLI runtime 适合每次消息启动命令的 agent，并可按具体实现恢复会话。
- HTTP runtime 走 OpenAI 兼容 Chat Completions API。
- Companion runtime 用于保持本地可见 CLI/App 与后台 bridge 连接。

### 配置与安全边界

- 配置文件由 `config.Config` 定义，默认路径由 `config.ConfigPath()` 决定。
- `allowed_workspace_roots` 为空时 `/cwd` 不限制工作目录；配置后必须限制在白名单内。
- `api_addr` 监听非 loopback 地址时必须配置 `api_token`，否则 `api.Server.Validate()` 拒绝启动。
- Agent 的 `permission_level` 会映射 Codex `approvalPolicy` 和 `sandboxMode`；高级字段 `approval_policy`、`sandbox_mode` 可覆盖。
- 发布后本机更新必须走 GitHub Release 资产和 `weclaw update` 校验，不要手工覆盖二进制。

### 发布与重启

- `scripts/release.sh` 当前 `TARGETS` 只包含 `darwin/arm64`。
- 正式发布会运行 `go test`、race 测试、`go vet` 和 `git diff --check`，然后创建 tag 与 GitHub Release。
- `weclaw update` 会下载 release 资产并校验 `checksums.txt`。
- `weclaw restart` 与 `weclaw update --restart` 会通过 `/api/runtime` 检查 active task；有运行中任务时默认拒绝重启，除非显式传 `--force`。

### 测试布局

- `agent/*_test.go` 覆盖 ACP、CLI、HTTP、Companion、Codex 配额、审批和进程隔离。
- `messaging/*_test.go` 覆盖命令路由、Codex/Claude 会话、进度、附件、审计、限流和工作目录白名单。
- `feishu/*_test.go` 覆盖事件解析、会话范围、卡片、审批按钮、去重和权限提示。
- `wechat/*_test.go` 与 `ilink/*_test.go` 覆盖微信 adapter、入站消息、replier 和 token/monitor 行为。
- `cmd/*_test.go` 覆盖启动、停止、更新、发布脚本、Companion 和诊断命令。

## How to verify

quick:

```bash
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

full:

```bash
go test ./... -count=1 -timeout 120s
go test -race ./agent ./cmd ./messaging -count=1 -timeout 60s
go vet ./...
```

release-side-effect:

```bash
scripts/release.sh --next-patch
```

## Stale when

- 新增平台目录、Agent 类型、配置字段、会话存储、命令入口或 API 端点。
- 飞书 session routing、审批卡片、Codex permission bridge 或 active task 生命周期改变。
- 发布目标不再只包含 `darwin/arm64`，或发布脚本验证命令变化。
- README 中的用户命令、配置示例或平台能力表更新。
