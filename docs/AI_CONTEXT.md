---
ai_summary:
  purpose: "提供 WeClaw 当前仓库形态、核心目录、文档路由、高风险区域和验证命令的精简上下文地图。"
  read_when:
    - "需要修改 WeClaw 功能、命令、平台 adapter、Agent runtime、配置或发布流程时。"
    - "排查飞书、微信、Codex、Claude、主动发送 API 或更新发布问题时。"
  source_of_truth:
    - "cmd/start.go"
    - "messaging/handler.go"
    - "messaging/progress_command.go"
    - "messaging/codex_session_status.go"
    - "messaging/codex_browser.go"
    - "agent/agent.go"
    - "platform/platform.go"
    - "platform/message.go"
    - "config/config.go"
    - "api/server.go"
    - "web/view.go"
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
- 发布目标：`scripts/release.sh` 当前只构建 `darwin/arm64`，本机安装必须走 `weclaw update`；发布门禁同时验证一键安装脚本，测试只能使用隔离的伪命令环境。
- Android profile：未检测到 Gradle 或 `AndroidManifest.xml`，当前只适用 `generic` profile。

## Core Directories

- `cmd/`：CLI 命令、启动、停止、更新、重启保护、Companion 和发布相关入口；更新后重启与手动重启必须在停止旧服务前复用启动预检。
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
- 修改 Codex 或 Claude 行为：先读 `agent/acp_agent.go`、`agent/acp_sessions.go`、`agent/acp_session_catalog.go`、`messaging/codex_sessions.go`、`messaging/codex_session_status.go`、`messaging/codex_browser.go`、`messaging/claude_sessions.go`、`messaging/agent_task.go`。
- 修改飞书体验：先读 `feishu/adapter.go`、`feishu/session_scope.go`、`feishu/choice.go`、`feishu/approval_panel.go`。
- 修改微信体验：先读 `wechat/`、`ilink/`、`messaging/progress.go`。
- 修改配置：先读 `config/config.go`、`config/detect.go`、`web/view.go` 和相关测试。
- 修改主动发送 API：先读 `api/server.go`、`platform/registry.go` 和 `api/server_test.go`。

## High-Risk Areas

- 飞书真实发送者身份和 session routing 必须分离；`feishu_session_key` 只用于会话路由。
- 飞书会话按聊天窗口聚合：DM 使用 `feishu:<tenant>:dm:<chatID>:<senderOpenID>`，群聊使用 `feishu:<tenant>:group:<chatID>`；回复串 / 话题不再生成独立 route。
- 飞书多项目入口通过 `platforms.feishu.bots[]` 配置多个机器人；每个 bot 的凭证按 `weclaw feishu login --name <bot>` 或 `weclaw feishu bootstrap --name <bot>` 保存，`app_secret` 不得写入 `config.json`。
- `weclaw feishu bootstrap` 只负责首次配置向导：保存凭证、更新 `bots[]`、提示可用 `lark-cli` 做权限/事件诊断；飞书运行时仍使用内置 SDK 长连接，不依赖 `lark-cli`。
- 飞书显式启用时必须配置非空 `bots[]`；缺失 bot 应在 `config.Validate` 阶段 fail-fast，不应等到平台启动阶段才失败。
- 飞书最小权限应覆盖单聊入站、群聊 @ 入站、发消息、资源、会话、CardKit 和机器人菜单：`im:message.p2p_msg:readonly`、`im:message.group_at_msg:readonly`、`im:message.group_at_msg.include_bot:readonly`、`im:message:send_as_bot`、`im:resource`、`im:chat`、`cardkit:card:read`、`cardkit:card:write`、`application:bot.basic_info:read`、`application:bot.menu:write`；`user` scopes 可为空。
- 飞书 bot 的 `allowed_users`、`default_agent` 和 `progress` 按 `app_id` 隔离；`allowed_users` 支持应用级 `open_id` 和同开发商下稳定的 `union_id`，多机器人优先配置 `union_id`；新增、删除 bot 或修改 `app_id` 属于平台拓扑变化，需要重启。
- 飞书未授权入站身份会在访问控制前写入 `~/.weclaw/feishu-identities.json`，但不会自动放行；管理员通过 `/feishu users pending/list/approve` 确认后才写入 `config.json`，本地只读查看使用 `weclaw feishu users pending/list`。
- `/progress` 从飞书入口触发时必须按当前 `account_id` 写入账号级配置，广播、Codex 会话切换和 Codex App 外部任务 watcher 也必须读取账号级进度配置。
- `/api/send` 在同一平台存在多个可主动发送账号时必须要求 `account_id`，不能静默选择第一个账号。
- 飞书审批必须只发给任务发起人，并在回调写入幂等记录前校验点击者。
- 飞书推荐菜单以 Codex / Claude 高频命令为主，默认不把 `/cx app`、`/cc cli`、`/cancel` 放到常用菜单；普通计划确认仍回复“确认”，Codex 运行中的暂存消息未被 `/guide`、`/cancel` 或 `/stop` 消费时会在上一任务结束后自动执行。
- 命令入口只保留当前主路径：远程更新使用 `/update`，Codex 会话使用 `/cx ...`，Claude 会话使用 `/cc ...`；不要重新引入 `/info`、`/clear`、`/upgrade`、`/codex ...` 会话入口、`/claude ...` 会话入口、`/cx open-app` 或 `/cx attach app` 这类兼容路由。
- Codex 推荐 remote-first；本地 Terminal 或 Codex App 是接手入口，不是权威状态源。
- Claude 远程后端是 ACP-only；`session/list` 是目录事实源，`claudeSessionStore` 保存 route 绑定，`ACPAgent.sessions` 只保存可重建运行态。禁止重新引入 Claude CLI 聊天后端或 `~/.claude` transcript 扫描。
- `/cc cli` 只允许通过 `AgentInfo.LocalCommand` 交接空闲 session；绑定切换、任务登记和本地交接必须共用 `claudeBindingExecutionKey`，避免 workspace/session 快照与任务启动发生竞态。
- Claude ACP 复用通用后台任务队列：每个活动任务最多暂存一条消息，失败后仍自动续跑；`/cancel` 撤回暂存，`/stop` 按当前窗口 Agent 停止，`/guide` 对 Claude 明确不支持。
- 微信 / 飞书显式切换到 Codex App 正在运行的会话后，WeClaw 会通过 app-server 读取 thread 状态、登记外部 active task，并在当前 turn 完成后回推结果。
- 运行中 Codex 长任务登记在 `Handler.activeTasks`；`restart` 和 `update --restart` 默认不能中断 active task。
- 微信 / 飞书远程管理命令由 `messaging/admin_commands.go` 执行 WeClaw 自身命令，不应进入 Codex / Claude；必须先校验顶层 `admin_users`，且管理员也必须在平台 `allowed_users` 内。飞书管理员身份判断必须同时检查 `open_id/user_id/union_id`。
- 微信同一条入站消息触发多次回复时，`wechat.Replier` 必须为后续 `SendText` 生成新的 `client_id`，避免微信端把结果消息按重复消息去重。
- Agent `permission_level` 省略时等同 `default`；显式配置只接受 `default`、`auto_review`、`full_access`，分别映射 Codex `approvalPolicy`、`sandboxMode` 与 `approvalsReviewer`；旧值必须 fail-fast。
- Web 配置保存必须保留 Agent 的 Codex 权限字段：`permission_level`、`approval_policy`、`approval_reviewer`、`sandbox_mode`。
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
