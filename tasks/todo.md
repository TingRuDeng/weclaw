# 当前任务记录

## 目标

把 Agent 会话改为显式生命周期：普通消息只能继续已经绑定的 Codex thread 或 Claude session；任何错误和恢复失败都不得隐式创建新会话。只有用户发送 `/new` 或明确点击新建入口时，系统才允许创建会话。

## 已确认行为

- 未绑定：普通消息不执行 Agent，只提示选择已有会话或发送 `/new`。
- 已绑定：额度耗尽、限流、网络中断、超时、认证错误、空响应和其他执行错误均保留原绑定。
- 恢复失败：`thread not found`、session 失效或无法 resume 时显式报错，用户自行切换会话或发送 `/new`。
- 运行时重启：Codex 或 Claude 子进程可以重启，但重启不等于新建会话；后续只能恢复原绑定。
- 显式新建：`/new` 与明确的新建卡片操作可以调用 `thread/start` 或 `session/new`。
- 消息处理：无有效绑定时不自动执行，也不自动排队原消息。
- 适用范围：Codex 与 Claude 使用相同规则，不保留旧的隐式创建兼容路径。

## 设计决策

- 采用显式会话状态模型：未绑定、已绑定、绑定失效。
- 分离“获取或恢复已有会话”和“显式创建新会话”，不再使用 `getOrCreate*` 混合职责。
- 保留失效绑定作为恢复和诊断依据，直到用户明确切换或新建。
- 删除 Codex stale thread、空响应和 Claude stale session 的自动新建重试。
- 删除额度错误后清理 thread 的行为；额度恢复后的下一条消息继续原 thread。
- 用户选择权优先于自动降级；真实失败必须显式暴露。

## 任务清单

实施细节见 `tasks/explicit-agent-session-lifecycle-plan.md`。

- [x] P1 串行：在 `agent/acp_threads.go` 拆分已有会话读取与显式创建函数。
- [x] P2 串行：在 `agent/codex_app_server_turn.go` 删除 Codex 隐式新建和额度清理路径。
- [x] P3 串行：在 `agent/acp_chat.go` 删除 Claude session 失效后的自动重建。
- [x] P4 串行：在 `agent/acp_session_control.go` 保证只有 `ResetSession` 走显式创建。
- [x] P5 串行：在 `messaging/progress_errors.go` 统一未绑定和恢复失败提示。
- [x] P6 串行：补充 Codex、Claude、`/new` 和消息提示回归测试。
- [x] P7 可并行验证：运行受影响包测试、全仓测试、vet、staticcheck 和差异检查。
- [x] P8 串行：执行 review gate，并补充 Review 小结。

## 预计修改范围

- `agent/acp_threads.go`
- `agent/acp_chat.go`
- `agent/codex_app_server_turn.go`
- `agent/acp_session_control.go`
- `messaging/progress_errors.go`
- 对应 `agent/*_test.go` 与 `messaging/*_test.go`

## 验证命令

```bash
go test ./agent ./messaging -count=1 -timeout 60s
go test ./... -count=1 -timeout 120s
go vet ./...
staticcheck ./...
git diff --check
```

## 并行说明

本次不并行修改业务代码。核心状态转换集中在 ACP 会话函数，并行写入存在职责交叉和语义冲突；最终可并行执行互不写文件的验证命令，由主流程统一审阅结果。

## Review 小结

2026-07-12 完成显式会话生命周期改造：Codex 与 Claude 普通消息只获取或恢复已有绑定，未绑定时不启动 runtime、不调用任何 RPC；额度、认证、网络、空响应和 missing thread/session 错误均不再清理绑定或自动创建。`thread/start` 与 `session/new` 仅保留在显式创建函数，并由 `/new` 的 `ResetSession` 路径调用。

自动验收通过：`go test ./agent ./messaging -count=1 -timeout 60s`、`go test ./... -count=1 -timeout 120s`、`go vet ./...`、`staticcheck ./...`、`python3 scripts/validate_docs.py . --profile generic` 和 `git diff --check` 均为退出码 0。Review gate 未发现阻塞性安全、行为或静态检查问题；剩余风险是尚未在真实飞书和微信窗口复测“额度恢复后继续原会话”与“未绑定提示”交互。
