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
- 发布目标：`scripts/release.sh` 构建 `darwin/arm64`、`darwin/amd64`、`linux/arm64`、`linux/amd64`，本机安装必须走 `weclaw update`；发布门禁同时验证一键安装脚本，测试只能使用隔离的伪命令环境。发布入口先统一配置持久化 `GOCACHE`：`WECLAW_GOCACHE` 优先，其次保留调用方显式导出的 `GOCACHE`；本机 Darwin 数据盘共享根目录存在时使用项目专属缓存，其他主机回退到 `go env GOCACHE`。
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
- 修改 Codex 或 Claude 行为：先读 `agent/acp_agent.go`、`agent/codex_app_server_host.go`、`agent/codex_runtime_lease.go`、`agent/acp_sessions.go`、`agent/acp_session_catalog.go`、`agent/claude_quota.go`、`agent/claude_quota_oauth.go`、`messaging/codex_sessions.go`、`messaging/codex_remote_selection_store.go`、`messaging/codex_session_status.go`、`messaging/codex_browser.go`、`messaging/claude_sessions.go`、`messaging/claude_quota.go`、`messaging/agent_task.go`。
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
- `/progress` 从飞书入口触发时必须按当前 `account_id` 写入账号级配置，广播、Codex 会话切换和共享 host 任务 watcher 也必须读取账号级进度配置。
- 飞书 `/cx ls`、`/cc ls` 的工作空间按钮只携带服务端生成的 5 分钟一次性 opaque token，并绑定 Agent、真实点击者和窗口 route；旧版数字索引卡片必须提示过期，用户手工输入数字命令仍保持兼容。分页使用绑定机器人账号、点击者、窗口、Agent 和列表层级的 5 分钟服务端快照，翻页只读取快照；卡片回调去重优先使用飞书 `event_id`，缺失时使用每次卡片渲染生成的 revision，不能仅用“原消息 ID + 页码命令”判断重复点击。
- `/api/send` 在同一平台存在多个可主动发送账号时必须要求 `account_id`，不能静默选择第一个账号。
- 飞书审批必须只发给任务发起人，并在回调写入幂等记录前校验点击者。
- 飞书推荐使用 7.22+ 悬浮菜单，菜单项通过“发送文字消息”触发命令，只保留 `/help`、状态/任务控制、Codex/Claude 列表与新建、模型/推理/确认模式等高频入口；可切换菜单受 3 个主菜单限制时移除“设置”主菜单。`/cc cli`、`/cancel` 等低频命令由飞书 `/help` 二级分类卡片承载，管理员分类只对管理员显示。普通计划确认仍回复“确认”，Codex 运行中的暂存消息未被 `/guide`、`/cancel` 或 `/stop` 消费时会在上一任务结束后自动执行。
- 命令入口只保留当前主路径：远程更新使用 `/update`，Codex 会话使用 `/cx ...`，Claude 会话使用 `/cc ...`；不要重新引入 `/info`、`/clear`、`/upgrade`、`/codex ...` 会话入口、`/claude ...` 会话入口、`/cx open-app` 或 `/cx attach app` 这类兼容路由。
- Codex 运行拓扑是单一 app-server、多前端客户端。`agent/codex_app_server_host.go` 通过稳定 Unix socket 复用或启动唯一 host，并按上游标准 HTTP Upgrade 建立 WebSocket-over-UDS；禁止把该 socket 当裸 JSONL `net.Conn`。默认路径超过 `sockaddr_un` 限制时会稳定哈希到真实系统临时目录下的用户私有 `weclaw-<uid>` 目录；macOS 必须解析 `/tmp` 到 `/private/tmp`，因为 Codex 拒绝 socket 目录链中的软链接。host 生命周期独立于单次前端请求，普通客户端断开和恢复不得终止其他前端正在使用的 host。
- Codex 窗口只持久化 frontend binding，不持有独占 writer owner。`messaging/codex_remote_selection_store.go` v4 只保存 route 到 workspace/thread 的绑定；v1-v3 owner/control 字段仅用于读取迁移，加载后必须丢弃并重写。多个飞书或微信窗口可同时绑定同一 thread，不能互相释放或覆盖绑定。
- Codex 的绑定入口包括 `/cx switch`、会话短编号、仅含一个会话的 `/cx cd`、飞书会话卡片、`/cx new` 和默认 Agent 为 Codex 时的全局 `/new`；这些入口最终复用 `messaging/codex_session_acquire.go:acquireCodexSessionWithBindingLocked`，新建入口先经过 `messaging/codex_session_new.go:createAndAcquireCodexSessionWithBindingLocked`。
- `messaging/codex_session_command_dispatch.go:prepareCodexSessionCommand` 在 route binding 锁内准备 `/cx` 命令；默认 Codex 的全局 `/new` 由 `messaging/default_session.go:resetDefaultCodexSessionForRoute` 持有相同 binding 执行锁。事务内部通过 `messaging/codex_session_locks.go:lockCodexSessionThreads` 按去重排序后的 thread ID 加锁，禁止反向获取 binding 锁。
- `messaging/codex_remote_selection_store.go:commitRemoteSelection` 使用 copy-on-write 候选副本，一次提交当前 route 的 selected thread 和 active workspace；持久化失败时不替换内存 maps，也不影响其他 route 的绑定。
- `messaging/codex_session_acquire.go:acquireCodexSessionWithBindingLocked` 先提交当前窗口 binding，再持久化窗口 Agent，最后把该 frontend conversation 映射到共享 app-server thread。Agent 选择落盘失败时只在 after-image 仍匹配时回滚；runtime 失败保留 durable binding 并明确报告通道暂不可用。
- `agent/codex_runtime_lease.go` 只把权威运行态 `weclaw_runtime` 视为可写，同一 thread 同时只允许一个 writer lease；route、窗口或旧 owner revision 不参与共享 host 写入授权。turn 接受后客户端断线必须保留 uncertain lease，只有 rollout 或重连后的 `thread/read` 确认同一 turn 终态才释放；晚到 probe、Desktop IPC 和旧 watcher 不能生成 conflict 或清除 binding。
- `/cx` 是 Codex 会话命令专用命名空间，未知子命令统一返回 `/cx help`；Codex 消息使用 `/codex <内容>` 或 `@cx <内容>`。`/cx owner` 及其旧参数已删除，binding、共享 host、writer 和任务状态统一由 `/cx status` 展示。`/cx app`、`/cx cli`、`/cx attach`、`/cx detach` 统一拒绝，旧 Codex Companion 配置无论 `auto_launch` 是否为 true 都迁移到 ACP 共享 host，禁止重新引入第二 app-server。
- Claude 远程后端是 ACP-only；`session/list` 是目录事实源，`claudeSessionStore` 同时持久化 route binding 和 session control intent（`remote`、`local`、`unclaimed`），后者是 WeClaw 远程写入事实源；`ACPAgent.sessions` 只保存可重建运行态。`/cc new` 后尚未进入 ACP 目录的已接管空会话，只能由 `/cc ls` 从同一 route 的 ready binding + remote intent 暂态投影展示，不得进入 `/cc switch` 候选或绕过 `session/list` 校验。禁止重新引入 Claude CLI 聊天后端或 `~/.claude` transcript 扫描。
- Claude 的 `/cc switch`、`/cc new`、飞书会话卡片、默认 Claude 的全局 `/new` 和 `/cc owner remote` 最终复用 owner-first acquire：先原子提交 route binding、目标 `remote` owner 和同 route 旧 session 的 `local` owner，再持久化窗口 Agent，最后执行 `session/resume`。恢复失败保留新选择和 owner，并把 binding 记为 `resume_failed` 以禁止普通写入；`/cc owner local` 只释放控制并保留选择，普通消息不会隐式重新接管。
- `/cc cli` 只允许通过 `AgentInfo.LocalCommand` 交接空闲 session，且必须先把 owner 持久化为 `local` 并清理 ACP runtime；本地 CLI 结束前不要重新接管。独立 CLI 任务不属于 WeClaw active task，不能远程观察、回传进度、`/guide` 或停止。`/cc quota` 优先兼容 Claude Code 旧版 `Claude Code-credentials` Keychain 或 `CLAUDE_CONFIG_DIR/.credentials.json`（兼容 `claudeAiOauth`、`claude.ai_oauth`），只把内存中的 access token 发送到固定的 Anthropic OAuth usage 地址；禁止记录/持久化 token、回显响应体、跟随重定向、实现登录/刷新或复制新版 secure storage 解密逻辑。凭据不可读或请求失败时，通过 `AgentInfo.LocalCommand` 启动无会话持久化、禁用项目设置和 MCP 的短生命周期 Claude 原生进程，只发送 `initialize`、`get_usage` 两个控制消息。两条路径都不得发送模型提示词或扫描 transcript；相关上游契约可能变化，API key、第三方 provider 或缺少 profile 权限时必须明确展示订阅额度不可用。
- Claude 绑定切换、任务登记和本地交接必须共用 `claudeBindingExecutionKey`，并通过 session 有序锁、owner tuple 和 revision 双重门禁避免双写。`local`、`unclaimed`、其他 route 的 `remote` 或 revision 变化都拒绝普通写入；v2 多 binding 冲突迁移为 `unclaimed`，不得静默选择赢家。
- Claude ACP 复用通用后台任务队列：每个活动任务最多暂存一条消息，失败后仍自动续跑；`/cancel` 撤回暂存，`/stop` 按当前窗口 Agent 停止，`/guide` 对 Claude 明确不支持。
- 微信 / 飞书显式绑定到共享 host 中正在运行的会话后，WeClaw 会读取 thread 状态、登记 active task observer，并在当前 turn 完成后回推结果。
- 运行中 Codex 长任务登记在 `Handler.activeTasks`；`restart` 和 `update --restart` 默认不能中断 active task。
- 微信 / 飞书远程管理命令由 `messaging/admin_commands.go` 执行 WeClaw 自身命令，不应进入 Codex / Claude；必须先校验顶层 `admin_users`，且管理员也必须在平台 `allowed_users` 内。飞书管理员身份判断必须同时检查 `open_id/user_id/union_id`。管理命令先明确回复后台受理，结束后另发最终结果；`/update` 摘要必须识别 CLI 当前使用的中文版本输出。
- 微信同一条入站消息触发多次回复时，`wechat.Replier` 必须为后续 `SendText` 生成新的 `client_id`，避免微信端把结果消息按重复消息去重。
- Agent `permission_level` 省略时等同 `default`；显式配置只接受 `default`、`auto_review`、`full_access`，分别映射 Codex `approvalPolicy`、`sandboxMode` 与 `approvalsReviewer`；旧值必须 fail-fast。
- Web 配置保存必须保留 Agent 的 Codex 权限字段和共享 socket：`permission_level`、`approval_policy`、`approval_reviewer`、`sandbox_mode`、`app_server_socket`。
- `api_addr` 监听非 loopback 地址时必须配置 `api_token`。
- 发布后本机更新必须走 GitHub Release 资产和 `weclaw update` 校验，不要手工覆盖二进制。普通 `weclaw update` 在已是最新版时不得启动 Claude ACP 预检；实际安装新版本或显式 `update --restart` 才执行启动预检。

## Validation Commands

- quick: `python3 scripts/validate_docs.py . --profile generic`、`git diff --check`
- full: `go test ./... -count=1 -timeout 120s`
- full: `go test -race ./... -count=1 -timeout 180s`
- full: `go vet ./...`、`go mod tidy -diff`
- full: `go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`
- full: `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`
- release-side-effect: `scripts/release.sh --next-patch`

## Stale when

- 新增平台目录、Agent 类型、配置字段、会话存储、命令入口、API 端点，或飞书 session routing、审批卡片、Codex permission bridge、active task 生命周期改变。
- 正式发布资产矩阵、发布脚本验证命令，或 README 中的用户命令、配置示例、平台能力表更新。
