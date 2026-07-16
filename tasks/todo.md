# 当前任务记录

## 目标

让 Codex 与 Claude 都以微信/飞书窗口持久化绑定和显式释放作为所有权事实源，
将运行通道恢复从所有权提交中解耦。

## 任务清单

- [x] Task 1：Codex 选择先提交 binding/owner，运行失败保留选择并关闭写入。
- [x] Task 2：Codex 显式释放先提交 desktop owner，取消幂等选择的 Desktop 探测。
- [x] Task 3：Claude 选择先提交 binding/owner，恢复失败持久化 `resume_failed`。
- [x] Task 4：统一切换、新建、飞书卡片、owner 和普通消息提示。
- [x] Task 5：补齐持久化回滚、并发保护、Android 飞书入口和运行失败回归。
- [x] Task 6：完成全仓验证、独立复核和差异检查。

## 当前状态

Codex 与 Claude 的 owner-first 实现已完成。窗口选择和显式释放先落盘，runtime 恢复失败
只进入写入门禁，不再回滚 Agent、会话或 owner；全仓测试、messaging/agent 竞态检测、vet、
文档校验、独立复核和差异检查均已通过。
