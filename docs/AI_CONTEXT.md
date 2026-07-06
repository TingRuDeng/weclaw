---
ai_summary:
  purpose: "提供 WeClaw 当前仓库形态、核心目录、文档路由、高风险区域和验证命令的精简上下文地图。"
  read_when:
    - "需要修改 WeClaw 功能、命令、平台 adapter、Agent runtime、配置或发布流程时。"
    - "排查飞书、微信、Codex、Claude、主动发送 API 或更新发布问题时。"
  source_of_truth:
    - "cmd/start.go"
    - "messaging/handler.go"
    - "messaging/codex_session_status.go"
    - "messaging/codex_browser.go"
    - "agent/agent.go"
    - "platform/platform.go"
    - "platform/message.go"
    - "config/config.go"
    - "scripts/release.sh"
  verify_with:
    - "python3 scripts/validate_docs.py . --profile generic"
    - "git diff --check"
  stale_when:
    - "顶层模块职责、消息数据流、配置字段、命令列表、发布流程或验证命令变化。"
---

# WeClaw AI 上下文

## Project Snapshot

- 仓库类型：单一 Go 仓库，不是 coordination directory。
- Go 模块：`github.com/fastclaw-ai/weclaw`，以 `main.go` 和 `cmd/root.go` 进入 CLI。
- 产品定位：把微信个人号和飞书消息接入 AI Agent，业务层通过 `platform` 抽象隔离平台差异。
- 发布目标：`scripts/release.sh` 当前只构建 `darwin/arm64`，本机安装必须走 `weclaw update`。
- Android profile：未检测到 Gradle 或 `AndroidManifest.xml`，当前只适用 `generic` profile。

## Core Directories

- `cmd/`：CLI 命令、启动、停止、更新、重启保护、Companion 和发布相关入口。
- `config/`：配置结构、默认值、Agent 探测、工作目录白名单、管理员白名单和 API 安全校验。
- `agent/`：统一 Agent 接口，以及 ACP、CLI、HTTP、Companion 等 runtime。
- `platform/`：跨平台消息、回复、注册表和访问控制抽象。
- `messaging/`：命令路由、会话、审批、进度、任务状态和 Agent 调用主业务。
- `feishu/`：飞书事件解析、会话范围、卡片、按钮、审批和权限提示。
- `wechat/`、`ilink/`：微信个人号接入、入站消息、replier、token 和 monitor。
- `api/`：本机 HTTP API，包含主动发送和 runtime 状态查询。
- `web/`：Web 配置界面。
- `docs/`、`tasks/`：上下文索引、当前任务记录和长期 lessons。

## Documentation Map

- `AGENTS.md`：可移植代理入口，只放路由、约束和验证边界。
- `docs/README.md`：文档索引，说明 authority docs、任务记录和验证命令。
- `docs/AI_CONTEXT.md`：当前代码地图和高风险路径摘要。
- `README_CN.md`、`README.md`：产品级使用、配置和命令说明。
- `tasks/todo.md`：当前或正在执行的任务记录，不长期累积已完成流水账。
- `tasks/lessons.md`：发布、重启、飞书审批、Codex 会话等踩坑规则。

## Common Task Reading Paths

- 修改启动、停止、更新、远程管理命令或发布：先读 `cmd/start.go`、`cmd/update.go`、`cmd/restart_safety.go`、`messaging/admin_commands.go`、`scripts/release.sh`。
- 修改消息命令或任务状态：先读 `messaging/handler.go`、`messaging/progress.go`、`messaging/codex_sessions.go`。
- 修改 Codex 或 Claude 行为：先读 `agent/acp_agent.go`、`agent/cli_agent.go`、`messaging/codex_sessions.go`、`messaging/codex_session_status.go`、`messaging/codex_browser.go`、`messaging/claude_sessions.go`。
- 修改飞书体验：先读 `feishu/adapter.go`、`feishu/session_scope.go`、`feishu/choice.go`、`feishu/approval_panel.go`。
- 修改微信体验：先读 `wechat/`、`ilink/`、`messaging/progress.go`。
- 修改配置：先读 `config/config.go`、`config/detect.go` 和相关测试。

## High-Risk Areas

- 飞书真实发送者身份和 session routing 必须分离；`feishu_session_key` 只用于会话路由。
- 飞书 DM 子会话使用 `dm_thread` route 独立保存 Codex active workspace；子会话 `/cx cd`、`/cx switch`、`/cx app`、`/cx cli`、`/new` 只能更新当前 route，不能改写真实用户 owner workspace 或 Agent 全局 cwd。
- 飞书审批必须只发给任务发起人，并在回调写入幂等记录前校验点击者。
- Codex 推荐 remote-first；本地 Terminal 或 Codex App 是接手入口，不是权威状态源。
- 微信 / 飞书显式切换到 Codex App 正在运行的会话后，WeClaw 会通过 app-server 读取 thread 状态、登记外部 active task，并在当前 turn 完成后回推结果。
- 运行中 Codex 长任务登记在 `Handler.activeTasks`；`restart` 和 `update --restart` 默认不能中断 active task。
- 微信 / 飞书远程管理命令由 `messaging/admin_commands.go` 执行 WeClaw 自身命令，不应进入 Codex / Claude；必须先校验顶层 `admin_users`，且管理员也必须在平台 `allowed_users` 内。
- 微信同一条入站消息触发多次回复时，`wechat.Replier` 必须为后续 `SendText` 生成新的 `client_id`，避免微信端把结果消息按重复消息去重。
- Agent `permission_level` 省略时等同 `default`；显式配置只接受 `default`、`auto_review`、`full_access`，分别映射 Codex `approvalPolicy`、`sandboxMode` 与 `approvalsReviewer`；旧值必须 fail-fast。
- `api_addr` 监听非 loopback 地址时必须配置 `api_token`。
- 发布后本机更新必须走 GitHub Release 资产和 `weclaw update` 校验，不要手工覆盖二进制。

## Validation Commands

- quick: `python3 scripts/validate_docs.py . --profile generic`
- quick: `git diff --check`
- full: `go test ./... -count=1 -timeout 120s`
- full: `go test -race ./agent ./cmd ./messaging -count=1 -timeout 60s`
- full: `go vet ./...`
- release-side-effect: `scripts/release.sh --next-patch`

## Stale when

- 新增平台目录、Agent 类型、配置字段、会话存储、命令入口或 API 端点。
- 飞书 session routing、审批卡片、Codex permission bridge 或 active task 生命周期改变。
- 发布目标不再只包含 `darwin/arm64`，或发布脚本验证命令变化。
- README 中的用户命令、配置示例或平台能力表更新。
