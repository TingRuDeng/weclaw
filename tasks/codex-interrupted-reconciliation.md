# Codex 中断终态核对

## 目标

- 将 Codex app-server 的 `interrupted` 从立即失败调整为待确认终态。
- 仅通过同一 `threadID + turnID` 的 rollout 判断继续、完成或真实中止。
- 保持原任务卡片和队列生命周期，确保最终结果只发送一次。

## 非目标

- 不自动重试或创建新的 Codex turn。
- 不跟随其他 turn，不修改用户配置的任务超时。
- 不调整 Claude 或其他 Agent 的终态规则。

## 当前事实

- `agent/codex_events.go` 当前把 `interrupted` 直接映射为错误事件。
- `messaging/codex_external_watch.go` 已支持 Desktop 断线后接续 rollout，但飞书发起的 Codex 任务尚未复用。
- 实机日志显示 app-server 报告中断后，同一 rollout turn 仍持续执行。

## 决策日志

- 采用“结构化中断 + Messaging rollout 核对”，不忽略中断，也不重试 turn。
- 用户取消、明确 `turn_aborted`、换 turn 和读取失败均显式结束，不做静默回退。
- 目标文件已有未提交改动，本轮串行整合并保留现有行为。

## 执行计划

- [x] P1 串行：为结构化中断补失败测试。
- [x] P2 串行：实现 Agent 层结构化中断。
- [x] P3 串行：实现 Messaging 同 turn rollout 接续。
- [x] P4 串行：覆盖真实停止、换 turn 和单一终态。
- [x] P5 可并行验证：测试、race、vet、staticcheck、文档校验与差异检查。

## 进度记录

- 2026-07-14：用户已确认现状、功能点、风险决策和执行计划。
- 2026-07-14：完成 Agent 结构化中断红绿测试，保留 thread 与 turn 身份。
- 2026-07-14：完成同 turn rollout 接续及中止、换 turn、延迟写入和单一终态测试。

## 验证结果

- `go test ./... -count=1 -timeout 60s`：通过。
- `go test -race ./agent ./messaging -count=1 -timeout 60s`：通过。
- `go vet ./...`：通过。
- `staticcheck ./...`：通过。
- `python3 scripts/validate_docs.py . --profile generic`：通过。
- `git diff --check`：通过。

## Review 小结

- 终态：finished。
- Spec 符合度：结构化中断、同 turn 接续、真实中止、换 turn、延迟落盘和单一回复均已覆盖。
- 安全检查：未引入凭据、外部命令或未校验的动态执行。
- 复杂度检查：新增生产文件低于 300 行，新增函数均低于 50 行。
- Document-refresh: not-needed。
- 原因：本次属于内部终态修复，不改变用户命令和配置契约。
- 剩余风险：`chatCodexAppServerTurn` 是既有超长函数，本轮仅增加结构化分支，后续可独立重构。
