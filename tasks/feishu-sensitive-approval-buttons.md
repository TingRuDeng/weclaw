# 飞书敏感操作确认按钮

## 目标

- Codex 触发敏感操作审批时，通过飞书交互式按钮让用户确认。
- 用户点击允许/拒绝后，把选择回写给当前 Codex turn。
- 未配置审批处理器时保持旧行为，不改变普通本地 Codex 调用语义。

## 非目标

- 不新增任务列表或审批历史页。
- 不改变 Codex 权限模型本身，只把已有 approval request 接到平台交互。
- 不把工具调用完整日志作为长期消息保存。

## 当前事实

- `agent/acp_agent.go:handlePermissionRequest` 原先自动选择 allow。
- `agent/acp_agent.go:getOrCreateThread`、`resumeThread`、`chatCodexAppServerWithRetry` 原先固定 `approvalPolicy=never`。
- `feishu/choice.go:buildChoiceCard` 已支持按钮卡片，按钮回调会进入 `messaging/handler.go:handlePlatformMessage`。
- `platform.Replier.AskChoices` 在飞书端是按钮卡片，在微信端是编号文本。

## 执行记录

- [x] 新增 `agent/approval.go`，用 context 为单个 turn 注入审批处理器。
- [x] Codex app-server 在存在审批处理器时使用 `approvalPolicy=untrusted`，否则保持 `never`。
- [x] `handlePermissionRequest` 改为路由到当前 turn；无法路由时默认拒绝，避免静默放行。
- [x] `messaging.Handler` 新增 pending approval registry，优先消费飞书按钮回调。
- [x] 审批按钮文案固定为中文“允许本次 / 拒绝”。
- [x] 补充 agent 和 messaging 单元测试。
- [x] 补充 `turn/start` 参数测试，确认存在审批处理器时 `approvalPolicy=untrusted`。

## 验证结果

- `go test ./agent -run 'Approval|Permission|Codex.*Approval' -count=1 -timeout 60s`：通过。
- `go test ./messaging -run 'Approval|RawCommand|Choice' -count=1 -timeout 60s`：通过。
- `go test ./agent ./messaging ./feishu ./cmd -count=1 -timeout 60s`：通过。
- `go vet ./...`：通过。
- `git diff --check`：通过。

## 剩余风险

- 实际 `turn/approval/request` 的 toolCall 字段结构由 Codex app-server 决定；当前先展示截断后的原始 JSON，后续可按真实样例优化摘要。
- 多 agent 广播时同一用户同时出现多个审批请求可能互相覆盖；当前主路径是单 Codex 任务串行。
