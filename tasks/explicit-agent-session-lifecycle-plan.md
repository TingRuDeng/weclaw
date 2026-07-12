# Agent 显式会话生命周期实施计划

> **执行要求：** 使用 `seq-execute` 在当前会话串行实施；每个任务先写失败测试，再写最小实现并更新 `tasks/todo.md`。

**目标：** 普通消息只能继续已有 Codex thread 或 Claude session，任何错误不得隐式创建会话，只有 `/new` 或明确的新建入口可以创建。

**架构：** 把 ACP 内部混合的 `getOrCreate*` 拆成 `require*` 与 `create*` 两组职责。聊天路径只调用 `require*`，显式重置路径只调用 `create*`；底层恢复错误保留原映射，由消息层转换为可操作提示。

**技术栈：** Go、ACP JSON-RPC、Codex app-server、Claude legacy ACP、Go testing。

---

### 任务 1：建立未绑定错误与显式会话 API

**文件：**
- 修改：`agent/agent.go`
- 修改：`agent/acp_threads.go`
- 测试：`agent/acp_thread_test.go`
- 测试：`agent/acp_chat_test.go`

- [ ] 新增失败测试：无 Codex 映射时调用聊天路径返回 `ErrAgentSessionNotBound`，且 `thread/start` 调用次数为 0。
- [ ] 新增失败测试：无 Claude 映射时调用聊天路径返回 `ErrAgentSessionNotBound`，且 `session/new` 调用次数为 0。
- [ ] 运行：`go test ./agent -run 'TestACPAgentChatRequires|TestLegacyACPChatRequires' -count=1 -timeout 60s`；预期测试失败，证明当前仍会隐式创建。
- [ ] 在 `agent/agent.go` 定义可用 `errors.Is` 判断的 `ErrAgentSessionNotBound`。
- [ ] 在 `agent/acp_threads.go` 实现以下职责分离：

```go
func (a *ACPAgent) requireSession(conversationID string) (string, error)
func (a *ACPAgent) createSession(ctx context.Context, conversationID string) (string, error)
func (a *ACPAgent) requireThread(ctx context.Context, conversationID string) (string, error)
func (a *ACPAgent) createThread(ctx context.Context, conversationID string) (string, error)
```

- [ ] `require*` 不调用创建 RPC；映射不存在时包装 `ErrAgentSessionNotBound`。`requireThread` 仅在 `resumeOnFirstUse` 为真时调用 `thread/resume`，失败后保留映射和恢复标记。
- [ ] `create*` 只负责调用 `session/new` 或 `thread/start`、校验响应、保存映射。
- [ ] 运行任务 1 测试；预期通过。
- [ ] 更新 `tasks/todo.md`：勾选 P1。

### 任务 2：Codex 错误保留原 thread

**文件：**
- 修改：`agent/codex_app_server_turn.go`
- 修改：`agent/acp_agent.go`
- 修改：`agent/acp_constructor.go`
- 测试：`agent/acp_codex_event_test.go`
- 测试：`agent/acp_recovery_test.go`

- [ ] 将 `TestACPAgentRefreshesRuntimeOnNextTurnAfterUsageLimit` 改为：第二次消息仍向 `old-thread` 执行，`thread/start` 为 0，持久化映射仍为 `old-thread`。
- [ ] 将空响应测试改为：返回 `agent returned empty response`，`thread/start` 为 0，持久化映射仍为 `old-thread`。
- [ ] 新增 stale thread 与认证错误回归测试：返回原错误，映射仍为 `old-thread`，不得调用 `thread/start`。
- [ ] 运行：`go test ./agent -run 'TestACPAgent.*(UsageLimit|EmptyResponse|MissingThread|AuthState)' -count=1 -timeout 60s`；预期旧实现失败。
- [ ] 从 `chatCodexAppServerWithRetry` 删除 `allowFreshRetry` 参数、`retryWithFreshThread` 调用和自动恢复为新 thread 的分支。
- [ ] 删除 `refreshCodexRuntimeAfterUsageLimit`、`markCodexUsageLimitRefresh`、`takeCodexUsageLimitRefresh`、`invalidateCodexRuntime` 及 `usageLimitRefreshOnNextTurn` 状态。
- [ ] 额度错误只返回额度详情；认证、missing thread 和空响应均保留映射并显式返回错误。
- [ ] 保留 `turn/start` 发现 missing thread 后对同一 thread 执行一次 `thread/resume` 的恢复尝试；resume 失败不得创建新 thread。
- [ ] 运行任务 2 测试；预期通过。
- [ ] 更新 `tasks/todo.md`：勾选 P2。

### 任务 3：Claude 错误保留原 session

**文件：**
- 修改：`agent/acp_chat.go`
- 测试：`agent/acp_recovery_test.go`

- [ ] 将 `TestACPAgentLegacySessionNotFoundRetriesWithFreshSession` 改为：只向 `session-old` prompt 一次，`session/new` 为 0，返回 session not found，持久化映射仍为 `session-old`。
- [ ] 运行：`go test ./agent -run TestACPAgentLegacySessionNotFound -count=1 -timeout 60s`；预期旧实现失败。
- [ ] 从 `chatLegacyACP` 删除 `allowSessionRetry` 参数和 stale session 自动清理、递归重试分支。
- [ ] 聊天路径改用 `requireSession`，session/prompt 错误直接返回并保留映射。
- [ ] 更新调用方和现有测试签名，运行 `go test ./agent -count=1 -timeout 60s`；预期通过。
- [ ] 更新 `tasks/todo.md`：勾选 P3。

### 任务 4：限定显式创建入口

**文件：**
- 修改：`agent/acp_session_control.go`
- 修改：`agent/acp_threads.go`
- 测试：`agent/acp_recovery_test.go`
- 测试：`agent/acp_thread_test.go`
- 测试：`agent/claude_acp_model_test.go`

- [ ] 更新 `/new` 测试，验证 `ResetSession` 对 Codex 调用一次 `thread/start`，对 Claude 调用一次 `session/new`。
- [ ] `ResetSession` 清理旧映射和历史后直接调用 `createThread` 或 `createSession`。
- [ ] `createResetCodexThread` 在 stdin 失效后可以重启 runtime 并再次调用 `createThread`，该路径只由 `ResetSession` 使用。
- [ ] 删除或重命名所有 `getOrCreateThread`、`getOrCreateSession` 调用，确保普通聊天路径无法触达创建函数。
- [ ] 修正 `ClearCodexThread` 注释，不再承诺“下一条消息自动创建”。
- [ ] 运行：`rg -n 'getOrCreate(Thread|Session)' agent`；预期无结果。
- [ ] 运行：`go test ./agent -run 'TestACPAgentResetSession|TestClaudeACP' -count=1 -timeout 60s`；预期通过。
- [ ] 更新 `tasks/todo.md`：勾选 P4。

### 任务 5：统一消息层提示

**文件：**
- 修改：`messaging/progress_errors.go`
- 测试：`messaging/progress_test.go`

- [ ] 新增测试：`ErrAgentSessionNotBound` 显示“当前窗口尚未绑定会话，请选择已有会话或发送 /new”。
- [ ] 更新 session/thread not found 测试：提示“原会话无法恢复，请切换其他会话或发送 /new”，不声称系统会自动恢复或创建。
- [ ] 在 `friendlyAgentError` 首先使用 `errors.Is(err, agent.ErrAgentSessionNotBound)` 处理未绑定错误，再处理字符串分类。
- [ ] 扩展恢复失败分类，同时识别 Codex thread not found 与 Claude session not found。
- [ ] 运行：`go test ./messaging -run 'TestRenderFinalFailure.*(NotBound|NotFound)' -count=1 -timeout 60s`；预期通过。
- [ ] 更新 `tasks/todo.md`：勾选 P5、P6。

### 任务 6：全量验证与交付审查

**文件：**
- 修改：`tasks/todo.md`

- [ ] 运行：`gofmt -w` 仅格式化本次修改的 Go 文件。
- [ ] 运行：`go test ./agent ./messaging -count=1 -timeout 60s`；预期退出码 0。
- [ ] 运行：`go test ./... -count=1 -timeout 120s`；预期退出码 0。
- [ ] 运行：`go vet ./...`；预期退出码 0。
- [ ] 运行：`staticcheck ./...`；预期退出码 0。
- [ ] 运行：`python3 scripts/validate_docs.py . --profile generic`；预期退出码 0。
- [ ] 运行：`git diff --check`；预期退出码 0。
- [ ] 按 `review-gate` 检查 Spec、安全、测试、复杂度、文档刷新和剩余风险。
- [ ] 更新 `tasks/todo.md`：勾选 P7、P8，并填写 Review 小结。

