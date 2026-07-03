# 飞书审批面板聚合

## 目标

- 同一个 Codex 任务内，多次权限审批不再产生多张独立大卡片。
- 每个任务维护一张审批面板卡片，后续审批追加到同一张面板。
- 点击审批后更新面板状态，并继续回写主任务卡片。
- 处理完成后原审批入口保持极简，不影响审计。

## 非目标

- 不修改 PR-A 会话路由。
- 不修改 UserID / routeUserID 分离。
- 不修改 agent。
- 不修改微信。
- 不做 stop / queue / doctor。
- 不重构完整 CardKit。

## 当前事实

- `feishu/replier.go:AskChoices` 会为审批发送独立选择卡片。
- `feishu/replier.go:attachTaskCardID` 会把当前任务卡片 ID 写入审批按钮 metadata。
- `feishu/adapter.go:handleApprovalCardAction` 会处理审批点击，并尝试回写主任务卡片。
- `feishu/task_card.go:addApproval` 已支持把审批结果追加到主任务卡片。
- 当前问题是每个审批请求仍占一张独立卡片，审批多时会把原问题卡片刷上去。

## 决策日志

- 采用“每任务一张审批面板卡片”方案。
- 审批面板只作用于 Codex 审批，不影响普通 AskChoices。
- 如果没有任务卡片 ID 或 CardKit 不可用，保留现有独立审批卡兜底。
- 面板更新失败不伪装成功，保留当前独立卡片路径。

## 执行计划

- [x] P1 串行：在 `feishu/task_card.go` 增加审批面板状态注册与快照。
- [x] P2 串行：在 `feishu/choice.go` 增加审批面板卡片渲染与按钮 payload。
- [x] P3 串行：在 `feishu/replier.go` 接入审批面板创建 / 更新。
- [x] P4 串行：在 `feishu/adapter.go` 点击审批后更新面板与主任务卡片。
- [x] P5 串行：补充 `feishu/*_test.go` 覆盖多审批聚合、点击状态更新、普通 AskChoices 不受影响。
- [x] P6 串行：运行 `go test ./...` 和 `git diff --check`。

## 验证结果

- `go test ./feishu -run 'ApprovalPanel|AskChoices|CardActionEvent' -count=1 -timeout 60s`：通过。
- `go test ./...`：通过。
- `git diff --check`：通过。

## Review 小结

- 审批面板只在 Codex 审批且存在任务卡片 ID 时启用。
- 普通 AskChoices 仍走原独立卡片路径。
- 审批回调仍先走幂等记录，只有首个 decision dispatch 给业务层。
- 如果 CardKit 面板创建或更新失败，保留原独立审批卡路径，避免审批请求不可见。
