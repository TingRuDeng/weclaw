# PR-B 飞书审批卡片收口记录

## 背景

PR-B 的目标是优化飞书审批卡片体验。审批卡片在用户点击后不应继续保持待处理状态，避免大量待处理卡片堆积并把主问题或回答卡片顶走。

本 PR 只处理飞书审批卡片状态更新、compact 收纳、审批 action 幂等，以及可用时的主任务卡片回写尝试。不改变 PR-A 的 session/routing 规则，不影响微信，也不引入完整 CardKit 重构。

## 审批卡片最终行为

### 待处理状态

- 展示审批请求摘要。
- 展示可选择的审批按钮。
- command 和 cwd 作为审批上下文展示给用户判断。

### 已授权 compact 状态

- 点击 allow 后，原审批卡片原地更新为 compact 状态。
- 状态显示为：已授权。
- 保留用户选择的 label。
- command/cwd 只显示一行摘要，过长内容截断。
- 按钮消失，避免再次展示为待处理审批。

### 已拒绝 compact 状态

- 点击 deny 后，原审批卡片原地更新为 compact 状态。
- 状态显示为：已拒绝。
- 保留用户选择的 label。
- command/cwd 只显示一行摘要，过长内容截断。
- 按钮消失，避免用户误判该审批仍未处理。

## 幂等模型

审批 action 必须按卡片级审批身份做幂等，而不是按点击用户做幂等。

- 优先使用 `approval_key` 作为稳定审批身份。
- 兼容 `approval_id` / `action_key` fallback。
- 最后使用 `message_id` fallback。
- 幂等 key 不应拼接 `UserID`，避免同一审批卡被不同用户点击时分裂成多个 decision。
- `recordApprovalAction` 必须在同一把锁内完成 check + write。
- agent dispatch 只允许发生在 `first=true` 路径。
- 重复点击、并发点击、飞书重复 callback 都不能重复 dispatch 同一个 approval decision。
- 第二次点击不能覆盖第一次 decision，例如第一次 allow 后，后续 deny 不应覆盖 allow。
- 卡片 update 失败不应导致 decision 再次 dispatch。

## 普通 AskChoices 不受影响

PR-B 的 compact 状态更新和幂等控制只作用于审批类 action。普通 AskChoices 仍按原交互处理，不应被替换成审批状态，也不应套用审批 decision 幂等模型。

## 主任务卡片回写现状

- 当 callback payload 中存在 `task_card_id` 时，会尝试把审批结果回写到主任务卡片。
- 当前生产链路暂未建立稳定的 `approval_id -> task_card_id` mapping。
- 因此第一版真实运行主要依赖审批卡片自身的 compact 收纳。
- 主任务卡片回写失败不影响审批 decision 继续返回给 agent。

## 后续 PR 不应破坏的约束

- 审批卡片点击后必须原地变为已处理 compact 状态。
- allow 和 deny 都必须移除按钮。
- command/cwd 不应恢复为大段完整展示。
- 普通 AskChoices 不应被审批 compact 逻辑影响。
- 同一个审批 action 只能 dispatch 一次。
- 幂等 key 不应包含 `UserID`。
- `recordApprovalAction` 的 check + write 必须保持原子性。
- dispatch 必须只发生在 `first=true` 路径。
- 主任务卡片回写失败不能阻断审批 decision。

## 已知后续优化项

- empty fallback key robustness。
- `message_id` fallback 追加 `chat_id`。
- approval store 持久化。
- 稳定建立 `approval_id -> task_card_id` mapping。
