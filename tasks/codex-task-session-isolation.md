# Codex 后台任务会话隔离执行记录

## 目标

修复 Codex A 会话后台任务运行时，用户切换到 B 会话后，A 任务完成记录污染 B 会话状态或误用 B 工作目录的风险。

## 非目标

- 不新增 `/tasks`、任务编号或跨会话 `/guide <任务编号>`。
- 不改变任务最终回复的接收方式，仍发送给原微信用户。
- 不重构 Claude 会话链路。

## 当前事实

- `messaging/handler.go` 原先在后台任务完成时重新读取当前 active workspace 记录 Codex thread。
- `agent/ACPAgent` 和 `agent/CLIAgent` 原先只有 Agent 级全局 cwd，用户切换会话会改写后续调用共享的 cwd。
- `/guide`、`/cancel`、`/run` 当前按 active Codex 会话定位任务，本轮保持该产品语义。

## 决策日志

- 采用 conversation 级 cwd：Handler 在解析 Codex 会话时把 workspace 绑定到 conversation。
- 后台任务启动时冻结 `workspaceRoot` 与 `conversationID`，运行和完成记录都使用冻结 route。
- 后台任务完成只更新冻结 workspace 的 thread，不更新 active workspace；显式 `/cx switch`、`/cx cd`、`/cx new` 仍负责切换 active workspace。

## 执行计划

- [x] 串行：补 Agent conversation cwd 红灯测试。
- [x] 串行：补 A 任务运行中切 B 后 thread 归属不污染的 Handler 回归测试。
- [x] 串行：实现 `agent.ConversationWorkspaceAgent` 可选接口。
- [x] 串行：让 `ACPAgent` 和 `CLIAgent` 优先使用 conversation 级 cwd。
- [x] 串行：冻结 Codex 后台任务 route，并按冻结 workspace 记录 thread。
- [x] 串行：运行定向回归验证。

## 进度记录

- 2026-06-18：已确认红灯测试失败原因：缺少 `SetConversationCwd`，且旧 Handler 不会为 conversation 绑定 cwd。
- 2026-06-18：已完成最小实现，定向回归集合通过。

## 验证结果

- `go test ./agent ./messaging -run 'ConversationCwd|CodexBackgroundTaskRecordsFrozenWorkspaceAfterSwitch' -count=1 -timeout 60s`：通过。
- `go test ./agent ./messaging -run 'ConversationCwd|Codex.*Workspace|Guide|Switch' -count=1 -timeout 60s`：通过。
- `go test ./agent ./messaging ./cmd -count=1 -timeout 60s`：通过。
- `go test -race ./agent ./cmd ./messaging -count=1 -timeout 60s`：通过。
- `go vet ./...`：通过。
- `go build -o /tmp/weclaw-codex-task-session-isolation .`：通过。
- `go test ./... -count=1 -timeout 120s`：通过。
- `git diff --check`：通过。

## Review 小结

- 已完成 Codex 后台任务会话隔离：后台任务启动时冻结 workspace 和 conversation；Agent 使用 conversation 级 cwd；后台任务完成只更新冻结 workspace 的 thread，不改写 active workspace。剩余产品边界：`/guide`、`/cancel`、`/run` 仍按当前 active Codex 会话作用，不支持跨会话任务编号。
