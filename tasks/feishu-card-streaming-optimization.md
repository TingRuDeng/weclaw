# 飞书卡片流式体验优化计划

## 目标

- 参考 `larksuite/openclaw-lark`，把飞书端回复体验从“轻量流式卡片”升级为更稳定的任务状态卡片。
- 在卡片中表达思考中、生成中、完成、失败状态。
- 保留 WeClaw 当前最终回复策略，不把 Codex 收纳的过程内容重新外露。
- 为后续敏感操作确认按钮建立清晰接口边界。

## 非目标

- 不复制 `openclaw-lark` 的工具体系、OAuth、文档/任务/日历能力。
- 不改变微信侧回复策略。
- 不在本轮默认开放 Codex 危险操作审批；审批链路需要单独确认策略后再启用。

## 当前事实

- `feishu/card.go:buildCardV2` 已能构建 CardKit 2.0 卡片，包含 `thinking`、`streaming`、`done`、`error` 状态。
- `feishu/stream.go:openCardKitStream` 已能创建 CardKit 卡片、发送卡片、开启 streaming mode。
- `feishu/stream.go:Update` 已用 `CardElement.Content` 更新稳定 `element_id`，但状态机较轻，缺少 stale 更新防护。
- `messaging/progress.go:startProgressSession` 已按平台能力打开流式进度，但终态固定为“任务已完成，正在发送最终结果。”，最终结果仍另发文本。
- `feishu/choice.go:buildChoiceCard` 已支持按钮卡片，`feishu/adapter.go:handleCardActionEvent` 可把按钮回调转为 `RawCommand`。
- `agent/acp_agent.go:handlePermissionRequest` 当前自动允许 Codex 权限请求，尚未接入平台确认按钮。
- 参考项目 `openclaw-lark/src/card/streaming-card-controller.ts:ensureCardCreated` 使用显式 phase、epoch 和创建 promise 管理异步建卡。
- 参考项目 `openclaw-lark/src/tools/ask-user-question.ts` 使用 pending registry、TTL 和回调消费机制处理用户确认/回答。

## 设计原则

- 简单优先：先增强 WeClaw 已有 CardKit 流，不引入 openclaw-lark 的完整控制器复杂度。
- 明确状态：卡片状态只由 stream 生命周期驱动，避免中间文本和最终文本重复。
- 不静默失败：CardKit 创建、更新、权限错误必须记录日志；只有已知非致命更新错误才忽略。
- 审批分层：确认按钮能力先抽象到平台层，Codex ACP 审批接入单独作为后续可控变更。

## 方案对比

### 方案 A：最小增强现有流式卡片

- 改 `feishu/stream.go` 增加状态字段、终态幂等、最后内容缓存、stale 更新防护。
- 改 `messaging/progress.go` 让飞书 stream 完成时直接用最终结果更新卡片，减少额外文本。
- 保持 `platform.Stream` 接口基本不变，必要时增加 `Complete` 的调用时机。

优点：改动小，风险低，适合当前 WeClaw 架构。  
缺点：敏感操作确认只能沿用现有按钮卡片，不能完整覆盖 Codex ACP 审批。

### 方案 B：引入独立 `FeishuTaskCardController`

- 新增 `feishu/task_card.go` 管理 phase、sequence、pending 更新、终态收敛。
- `Replier.OpenStream` 返回 controller 包装的 stream。
- 后续审批按钮也复用 controller。

优点：结构接近参考项目，后续扩展审批/工具状态更清晰。  
缺点：新增抽象和测试较多，本轮可能超出最小必要范围。

### 方案 C：完整复制 openclaw-lark 控制器模式

- 迁移 phase、flush controller、fallback patch、tool-use display、reasoning lane。

优点：能力完整。  
缺点：与 WeClaw Go 架构差异大，复杂度明显过高，不符合当前最小影响原则。

## 推荐方案

推荐先执行方案 A，并为方案 B 预留接口边界。

原因：
- 当前 WeClaw 已经有 CardKit 流式和按钮卡片，不需要从零重构。
- 用户当前诉求是飞书端交互体验，不是完整工具调用可视化。
- 先解决“状态卡片稳定、流式更新、终态不重复”这三个直接问题。

## 执行计划

- [x] 串行：调整 `messaging/progress.go`，让 stream session 能在拿到最终回复时用最终回复收尾卡片。
- [x] 串行：调整 `messaging/handler.go` 的 agent 回复路径，把最终回复交给 progress session 收尾，飞书流式卡片不再额外发送重复文本。
- [x] 串行：增强 `feishu/stream.go`，增加终态幂等、最后内容缓存、状态切换约束和更新错误日志。
- [x] 串行：增强 `feishu/card.go` 卡片文案和结构，明确“思考中 / 生成中 / 已完成 / 失败”状态。
- [x] 串行：补 `feishu/stream_test.go`、`messaging/progress_test.go`、`messaging/handler_test.go` 覆盖流式终态、重复发送、失败收尾。
- [x] 串行：评估 Codex ACP `turn/approval/request` 接口，输出是否进入下一轮“敏感操作确认按钮”改造。

## 验证矩阵

- `go test ./feishu -run 'Stream|Card|Choice' -count=1 -timeout 60s`
- `go test ./messaging -run 'Progress|Feishu|Choice|SendToNamedAgent' -count=1 -timeout 60s`
- `go test ./agent ./messaging ./feishu -count=1 -timeout 60s`
- `go vet ./...`
- `git diff --check`

## 风险

- 飞书 CardKit 权限不完整时，卡片创建或更新会失败，需要日志暴露真实错误。
- 若直接把最终回复写入卡片，长文本可能受卡片限制影响，需要保留必要的文本回退策略。
- Codex 审批默认自动允许属于安全风险；是否改为用户确认需要单独产品决策。

## 执行记录

- 已新增 `messaging/progress.go:startProgressSessionWithFinal`，原生流式平台可用最终结果完成同一张卡片。
- 已调整普通 agent、Codex 后台任务、广播任务路径；当卡片终态收敛成功时，不再额外发送重复文本。
- 已增强 `feishu/stream.go`，完成/失败后忽略迟到更新，重复完成保持幂等，重复内容不刷新卡片。
- 已调整 `feishu/card.go` 文案为“思考中 / 生成中 / 已完成 / 执行失败”。
- 已评估 `agent/acp_agent.go:handlePermissionRequest`：当前 Codex ACP 权限请求仍自动允许，敏感操作确认按钮需要下一轮单独改 agent 审批接口和平台 pending registry。

## 验证结果

- `go test ./feishu -run 'Stream|Card|Choice' -count=1 -timeout 60s`：通过。
- `go test ./messaging -run 'Progress|Feishu|Choice|SendToNamedAgent' -count=1 -timeout 60s`：通过。
- `go test ./agent ./messaging ./feishu -count=1 -timeout 60s`：通过。
- `go test ./cmd -count=1 -timeout 60s`：通过。
- `go vet ./...`：通过。
- `git diff --check`：通过。

## Review 小结

- Spec 符合度：已实现飞书流式卡片运行时展示过程态、完成后收敛为最终结果；微信侧语义未改。
- 安全检查：未新增密钥、凭证或外部输入执行路径；附件类最终回复不会被卡片消费，保留原附件发送与失败改写路径。
- 复杂度检查：未引入完整 `openclaw-lark` 控制器，保持在现有 `progressSession` 和 `feishuStream` 内最小扩展。
- 剩余风险：CardKit 权限或长文本限制仍依赖飞书侧能力；Codex 敏感操作确认按钮未在本轮启用。

## HARD-GATE

用户确认本计划前，不修改业务代码。
