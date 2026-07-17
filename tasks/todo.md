# 当前任务记录

## 目标

让 Codex 与 Claude 都以微信/飞书窗口持久化绑定和显式释放作为所有权事实源，
将运行通道恢复从所有权提交中解耦。

## 任务清单

- [x] Task 1：Codex 选择先提交 binding/owner，显式接管在 Desktop 不可确认时恢复 WeClaw。
- [x] Task 2：Codex 显式释放先提交 desktop owner；幂等选择复用 ready runtime，并恢复 unknown/conflict。
- [x] Task 3：Claude 选择先提交 binding/owner，恢复失败持久化 `resume_failed`。
- [x] Task 4：统一切换、新建、飞书卡片、owner 和普通消息提示。
- [x] Task 5：补齐持久化回滚、并发保护、Android 飞书入口和运行失败回归。
- [x] Task 6：完成全仓验证、独立复核和差异检查。

## 当前状态

Codex 与 Claude 的 owner-first 实现已完成。Codex 的显式 remote 选择可在 Desktop timeout、断线、
重启后 unknown 或旧 conflict 时恢复 WeClaw；Desktop 与 WeClaw turn 并存不再锁死会话，跨远程
窗口 owner、WeClaw 单 thread lease 和活动任务门禁保持不变。Claude 继续使用 `resume_failed` 门禁。
