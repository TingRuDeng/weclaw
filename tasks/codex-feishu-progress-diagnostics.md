# Codex 飞书进度与失败诊断修复

## 目标

- Codex app-server 的关键运行事件要进入 WeClaw 进度通道，让飞书任务卡片持续更新真实阶段。
- Codex 返回空泛错误时，要附带本轮 turn 最近关键事件，便于判断失败发生在哪个阶段。

## 非目标

- 不做失败后自动重试。
- 不自动新建会话重跑任务。
- 不扩大到发布流程、Git 权限或其他 Agent 的行为变更。

## 当前事实

- `agent/acp_read_loop.go` 当前忽略 `item/commandExecution/outputDelta`、`turn/diff/updated`、`item/autoApprovalReview/*`、`guardianWarning`。
- `agent/codex_events.go` 解析不到错误详情时会返回 `Codex 返回未知错误`。
- `messaging/progress.go` 已有平台进度会话和飞书 native stream，适合复用。

## 决策日志

- 选择把 Codex 关键事件转成内部 `progress` 事件，不拼入最终回复。
- 选择在 turn loop 内保存最近关键事件，用于增强未知错误。
- 不处理失败后迟到事件的卡片补写，避免引入跨 turn 后台状态和重复副作用；本轮先解决可观测性与实时状态。

## 执行计划

- [x] RED：补 ACP 进度事件与未知错误诊断测试。
- [x] RED：补消息层 native stream 多次状态更新测试。
- [x] GREEN：实现 Codex 关键事件到 progress 事件桥接。
- [x] GREEN：实现 turn 诊断缓存与未知错误增强。
- [x] GREEN：调整进度渲染，保证状态事件不被当作“实时片段”正文。
- [x] 验证：运行 agent、messaging、feishu 相关测试和 diff 检查。

## 进度记录

- 2026-07-05：用户确认方案后开始执行。
- 2026-07-05：目标测试先失败后转绿，已完成事件桥接和状态渲染修复。
- 2026-07-05：`go test ./... -count=1 -timeout 120s`、`python3 scripts/validate_docs.py . --profile generic`、`git diff --check` 均通过。

## 验证结果

- `go test ./agent -count=1 -timeout 60s`：通过。
- `go test ./messaging -count=1 -timeout 60s`：通过。
- `go test ./feishu -count=1 -timeout 60s`：通过。
- `go test ./... -count=1 -timeout 120s`：通过。
- `python3 scripts/validate_docs.py . --profile generic`：通过。
- `git diff --check`：通过。

## Review 小结

- 改动范围限定在 Codex app-server 事件桥接、turn 诊断和进度状态渲染。
- 未引入失败自动重试或新建会话，避免发布、推送类任务重复执行。
- 仍存在剩余限制：如果 Codex 在 WeClaw 已结束 turn 后才补发事件，本轮只避免继续打未处理日志，不做失败卡片补写。
