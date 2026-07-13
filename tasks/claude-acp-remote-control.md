# Claude ACP 远程接管实施计划

> **执行要求：** 实现阶段必须使用 `seq-execute`，可结合 `subagent-driven-development` 按文件所有权并行；每个阶段先写失败测试，再写最小实现。

**目标：** 将 Claude 远程能力收敛为 ACP 单后端，使飞书和微信可以列出、选择、新建、恢复并继续真实 Claude Code session，同时支持实时进度、审批、停止、排队续跑和当前 session 配置切换。

**架构：** `claude-agent-acp` 是唯一远程后端和会话目录来源。消息层持久化用户选择的 workspace/session，ACPAgent 只持有可重建的运行时映射；原生 `claude` 仅用于空闲 session 的本地可见交接。

**技术栈：** Go、ACP JSON-RPC/NDJSON、Cobra、飞书交互卡片、微信文本消息、Go 单元/集成测试。

---

## 1. 目标与非目标

### 目标

- Claude 远程对话只使用 `claude-agent-acp`，不保留 CLI 后端 fallback。
- `/cc ls` 只使用 ACP `session/list`，按 cwd 聚合为工作空间和会话。
- `/cc switch` 使用 `session/resume` 验证后提交绑定；失败保留原绑定。
- `/cc new` 立即调用 `session/new`，成功后才提交绑定。
- 重启后首次消息前恢复同一 session；失败后提示重新选择或 `/cc new`。
- ACP 进度映射到现有飞书卡片和微信进度链路，最终正文只发送一次。
- Claude 支持审批、`/stop`、排队、`/cancel`、当前 session 模型与推理强度切换。
- `/cc cli` 仅通过配置的原生 `claude --resume <sessionId>` 交接空闲 session。

### 非目标

- 不实时接管正被独立 Claude CLI 进程执行的活跃任务。
- 不实现 Claude `/guide`；ACP 当前没有等价的运行中 steer 能力。
- 不使用 `session/load` 作为 `session/resume` 失败后的回退。
- 不读取 `~/.claude/projects/*.jsonl` 作为会话、模型或进度事实源。
- 不删除通用 `CLIAgent`；它继续服务其他 CLI Agent。

## 2. 当前事实与证据

- `agent/acp_process.go:initializeACPSubprocess()` 发送 `initialize` 后仅返回原始 JSON，没有保存或验证能力。
- `agent/acp_threads.go:createSession()` 只实现标准 ACP `session/new`，没有 list/resume。
- `agent/acp_chat.go:chatLegacyACP()` 只把 `agent_message_chunk` 汇总为最终正文，没有输出 Claude 工具和计划进度。
- `agent/agent.go:ClaudeSessionAgent` 由 `CLIAgent` 实现，`ACPAgent` 尚未实现 Claude session 控制。
- `messaging/claude_local_handler.go:claudeSwitchTargets()` 合并本地 transcript 扫描与 WeClaw 状态，不符合 ACP 单一事实源。
- `messaging/claude_workspace_handler.go:handleClaudeNew()` 只设置 pending，新 session 延迟到普通消息创建。
- `messaging/agent_execution.go:sendToDefaultAgentForAccount()` 对非 Codex Agent 使用同步执行，不具备后台排队续跑。
- `messaging/task_commands.go:handleStopActiveTask()` 通过 `codexGuideTargetForRoute()` 固定定位 Codex。
- `config/detect.go:agentCandidates` 在 Claude ACP 后仍保留 Claude CLI fallback。
- `messaging/claude_cli_handler.go:handleClaudeCLI()` 使用 `Agent.Info().Command`，ACP 下会误把 adapter 当原生 CLI。

## 3. 已确认设计

### 3.1 Agent 接口

```go
type ClaudeSession struct {
	ID, Cwd, Title, UpdatedAt string
	Config ClaudeSessionConfig
}

type ClaudeSessionCatalogAgent interface {
	ListClaudeSessions(ctx context.Context) ([]ClaudeSession, error)
}

type ClaudeSessionAgent interface {
	CurrentClaudeSession(conversationID string) (string, bool)
	UseClaudeSession(ctx context.Context, conversationID, sessionID string) error
	ClearClaudeSession(conversationID string)
}

type ClaudeSessionConfigUpdate struct {
	ConversationID, Model, Effort string
}

type ClaudeSessionConfigAgent interface {
	ClaudeSessionConfig(conversationID string) (ClaudeSessionConfig, bool)
	SetClaudeSessionConfig(ctx context.Context, update ClaudeSessionConfigUpdate) error
}
```

- `AgentInfo.LocalCommand` 保存原生 Claude CLI；`Command` 始终保存 ACP adapter。
- `initialize` 必须声明 `sessionCapabilities.list` 和 `sessionCapabilities.resume`。
- `session/new`、`session/prompt`、`session/cancel` 属于 ACP 基础调用，调用失败直接暴露。

### 3.2 状态权威

- ACP `session/list`：会话目录、cwd、标题、更新时间。
- `claudeSessionStore`：route 当前选择的 workspace/session 及恢复状态。
- `ACPAgent.sessions`：仅运行时缓存；Claude 不写入通用 ACP state 文件。
- 持久化恢复状态使用 `pending_resume`、`ready`、`resume_failed`；加载文件时统一转为 `pending_resume`。
- 原 v1 `claude-sessions.json` 只迁移 active workspace，丢弃旧 CLI session ID。

### 3.3 失败与事务

- switch/new 顺序：校验或创建 -> ACP 绑定成功 -> 原子保存 Claude 绑定 -> 保存窗口 Agent。
- 保存失败时恢复旧 session 和旧 workspace；回滚失败必须组合返回两个错误。
- 新建后保存失败产生的孤立 session 不删除，保留在 ACP 目录供用户重新选择。
- resume 失败保留 session ID，但标记 `resume_failed`；普通消息只提示 `/cc ls` 或 `/cc new`。
- 无有效绑定时普通消息不得隐式创建 session。

## 4. 实施任务

### 任务 1：锁定 ACP 能力契约

**文件：**
- 修改：`agent/agent.go`、`agent/acp_agent.go`、`agent/acp_constructor.go`、`agent/acp_process.go`、`agent/acp_types.go`、`cmd/start_agent.go`
- 新建：`agent/acp_capabilities.go`、`agent/acp_capabilities_test.go`

- [x] 写 `TestClaudeACPStartupRequiresListAndResume`、`TestACPInitializeCachesAgentInfo`、`TestNonClaudeACPDoesNotRequireClaudeCapabilities`。
- [x] 运行 `go test ./agent -run 'Test(ClaudeACPStartup|ACPInitialize|NonClaudeACP)' -count=1 -timeout 60s`，确认因缺少解析和门禁失败。
- [x] 增加上述接口、`AgentInfo.LocalCommand`、能力解析和 Claude 专属门禁；Claude 身份以配置 Agent 名称为主、ACP `agentInfo` 为补充，禁止从可执行文件名推断。
- [x] 重跑同一命令，确认全部通过。
- [x] 提交：`测试：锁定 Claude ACP 能力契约`。

### 任务 2：实现 ACP 会话目录与恢复

**文件：**
- 修改：`agent/acp_agent.go`、`agent/acp_constructor.go`、`agent/acp_capabilities.go`、`agent/acp_threads.go`、`agent/acp_chat.go`、`agent/acp_state.go`
- 新建：`agent/acp_sessions.go`、`agent/acp_session_catalog.go`、`agent/acp_session_catalog_test.go`、`agent/acp_session_resume_test.go`

- [x] 写 list 分页、重复游标、空 ID、非法 cwd、非法游标与 resume 结果、resume 成功、resume 失败保留旧绑定测试。
- [x] 运行 `go test ./agent -run 'TestClaudeACP(List|Resume)' -count=1 -timeout 60s`，确认失败。
- [x] 将标准 ACP session 逻辑移入 `acp_sessions.go`；实现 list/resume 和只在成功后更新运行时映射。
- [x] Claude session 从 `acpPersistedState.Sessions` 排除；持久化 session 在握手身份确认前进入独立缓存，确认非 Claude 后一次性恢复，避免首次写回丢失其他标准 ACP 会话。
- [x] 重跑定向测试和 `go test ./agent -count=1 -timeout 60s`。
- [x] 提交：`功能：实现 Claude ACP 会话目录与恢复`。

### 任务 3：实现 session 配置与结构化进度

**文件：**
- 修改：`agent/acp_agent.go`、`agent/acp_constructor.go`、`agent/acp_types.go`、`agent/acp_capabilities.go`、`agent/acp_process.go`、`agent/acp_read_loop.go`、`agent/acp_rpc.go`、`agent/acp_chat.go`、`agent/acp_session_update.go`、`agent/acp_session_catalog.go`、`agent/acp_sessions.go`、`agent/claude_acp_model.go`
- 新建：`agent/acp_claude_progress.go`、`agent/claude_acp_session_config.go`、`agent/acp_claude_progress_test.go`、`agent/claude_acp_session_config_test.go`

- [x] 写 thought/tool/tool_update/plan 映射、最终正文去重、当前 session 配置切换测试。
- [x] 运行 `go test ./agent -run 'TestClaudeACP(Progress|SessionConfig)' -count=1 -timeout 60s`，确认失败。
- [x] 实现安全摘要映射；不把 `agent_message_chunk` 作为进度，不输出原始 ACP JSON。
- [x] 通过 `session/set_config_option` 更新当前 session；按 session 缓存完整配置，resume 回填配置；配置文件值只应用于 `session/new`。
- [x] 使用入站消息序号防止旧快照覆盖新通知；串行配置写入，并显式报告模型成功、推理强度失败的部分完成状态。
- [x] 将 thought 缓冲限制为 4096 字符、结构化进度历史限制为最近 128 条。
- [x] 重跑定向测试、权限测试和取消测试。
- [x] 提交：`功能：补齐 Claude ACP 配置与实时进度`。

### 任务 4：收敛配置、迁移与诊断

**文件：**
- 修改：`config/config.go`、`config/detect.go`、`cmd/config.go`、`cmd/start.go`、`cmd/start_agent.go`、`cmd/doctor.go`
- 修改：`agent/acp_agent.go`、`agent/acp_constructor.go`、`agent/acp_info.go`，将 `local_command` 传入运行时元数据
- 新建：`cmd/config_agent.go`、`cmd/config_agent_test.go`、`cmd/doctor_agent.go`
- 修改：`web/view.go`、`web/config_service.go`、`web/status.go`、`web/static/app.js`

- [x] 写 ACP-only 检测、旧 CLI 启动阻断、迁移命令、doctor capability probe、Web 往返测试。
- [x] 运行 `go test ./config ./cmd ./web -count=1 -timeout 60s`，确认新增测试失败。
- [x] 增加 `local_command`；删除 Claude CLI candidate；实现 `weclaw config agent`。
- [x] 启动和 Web 保存调用 `ValidateClaudeACPAgents()`；doctor 握手后立即停止，不创建 session。
- [x] 重跑定向测试，并验证迁移命令保留 env/model/cwd/progress、清空旧 CLI args。
- [x] 提交：`配置：收敛 Claude ACP 启动与诊断`。

### 任务 5：重写 Claude 会话选择链路

**文件：**
- 重写：`messaging/claude_sessions.go`、`messaging/claude_local_handler.go`、`messaging/claude_workspace_handler.go`
- 新建：`messaging/claude_session_persistence.go`
- 修改：`messaging/agent_conversation.go`、`messaging/default_session.go`、`messaging/cwd_command.go`、`messaging/claude_render.go`、`messaging/claude_feishu_cards.go`、`messaging/session_stores.go`、`messaging/handler_agent_fakes_test.go`、`cmd/start.go`
- 删除：`messaging/claude_local_sessions.go`、`messaging/claude_session_model.go`

- [ ] 写 ACP 目录卡片、权限过滤、switch/new 原子提交、恢复失败、无绑定拒绝和 v1 active workspace 迁移测试。
- [ ] 运行 `go test ./messaging -run 'Test(ClaudeACP|FeishuClaude|ClaudeSession)' -count=1 -timeout 60s`，确认失败。
- [ ] 改为 ACP list 唯一目录来源；删除 pending-new 和 transcript 扫描。
- [ ] 实现 `commitClaudeSelection(request)` 与显式回滚，失败不覆盖窗口 Agent。
- [ ] 切换反馈和 `/cc status` 展示当前 session 模型、推理强度和恢复状态。
- [ ] 重跑 `go test ./messaging -count=1 -timeout 60s`。
- [ ] 提交：`功能：接入 Claude ACP 会话导航`。

### 任务 6：统一后台任务、队列与停止

**文件：**
- 新建：`messaging/agent_task.go`、`messaging/agent_task_test.go`
- 修改：`messaging/agent_execution.go`、`messaging/task_state.go`、`messaging/task_commands.go`、`messaging/codex_agent_task.go`

- [ ] 写立即创建卡片、进度更新、单条排队、失败后续跑、撤回、停止和 Agent 隔离测试。
- [ ] 运行 `go test ./messaging -run 'Test(AgentTask|ClaudeTask)' -count=1 -timeout 60s`，确认失败。
- [ ] Claude ACP 改用后台执行器；复用 `activeAgentTask`，保留 Codex 外部 turn 专属流程。
- [ ] `/stop` 和 `/cancel` 按当前窗口 Agent 定位；Claude `/guide` 返回明确不支持。
- [ ] 重跑任务、队列、Codex live 回归测试。
- [ ] 提交：`功能：统一 Claude 任务进度与队列控制`。

### 任务 7：本地交接和死代码清理

**文件：**
- 修改：`messaging/claude_cli_handler.go`、`agent/cli_agent.go`
- 删除：`agent/cli_claude.go`、`agent/claude_model.go` 及对应 Claude CLI 专属测试

- [ ] 写 `local_command`、运行中拒绝、非法 session ID、越权 cwd 和原生命令参数测试。
- [ ] 运行对应测试确认失败，再实现严格校验和空闲交接。
- [ ] 删除 Claude CLI 后端分支，确认其他通用 CLI Agent 测试仍通过。
- [ ] 提交：`重构：删除 Claude CLI 远程后端`。

### 任务 8：文档、全量验证与 Review Gate

**文件：**
- 修改：`README_CN.md`、`README.md`、`docs/AI_CONTEXT.md`、`tasks/todo.md`

- [ ] 同步 ACP-only 配置、`local_command`、`/cc` 语义和独立 CLI 活跃任务边界。
- [ ] 执行验证矩阵中的全部命令并记录结果。
- [ ] 使用 `review-gate` 检查 Spec 覆盖、安全、复杂度、并发、状态一致性和剩余风险。
- [ ] 在本文件和 `tasks/todo.md` 写入 Review 小结。
- [ ] 提交：`文档：同步 Claude ACP 远程接管说明`。

## 5. 并行与写冲突

- A 先串行完成任务 1，锁定共享接口。
- A 继续负责任务 2、3 的 `agent/` 文件。
- B 在任务 1 后负责任务 4 的 `config/`、`cmd/`、`web/` 文件。
- C 在任务 2 后负责任务 5 的 `messaging/claude_*` 文件。
- D 在任务 2 后负责任务 6 的任务控制文件。
- `agent_conversation.go`、`cmd/start.go`、共享测试 fake、文档和最终整合只由主流程修改。
- 并行任务在独立 worktree 产出 patch；主流程按 A -> B -> C -> D 串行合并并逐轮验证。

## 6. 验证矩阵

```bash
go test ./agent ./messaging ./config ./cmd ./web -count=1 -timeout 60s
go test ./agent ./messaging -coverprofile=/tmp/claude-acp-cover.out -count=1 -timeout 60s
go test ./... -count=1 -timeout 120s
go test -race ./... -count=1 -timeout 120s
go vet ./...
staticcheck ./...
go build ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

- 新增 Claude ACP 会话、进度和任务控制核心逻辑覆盖率目标不低于 80%。
- fake ACP 集成进程必须覆盖 initialize/list/resume/new/prompt/update/permission/cancel。
- 实机验收覆盖飞书卡片、微信文本、重启恢复、配置切换、排队续跑、停止和空闲 `/cc cli`。

## 7. HARD-GATE

- 当前仅完成设计与规划，未修改实现代码。
- 用户再次明确批准后，才允许创建执行分支/worktree 并按任务 1 开始 TDD。
- 执行中若协议能力、上游响应或现有状态模型与本 Spec 不一致，必须先更新本文件并重新确认。
