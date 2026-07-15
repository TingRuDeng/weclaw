# 当前任务记录

## 目标

按 `tasks/codex-session-control-timeout.md` 修复 Codex 会话切换卡住后，普通消息和 `/cx owner remote` 永久等待的问题。

## 任务清单

- [x] P0 串行：完成运行日志、部署版本、会话状态和锁链只读诊断。
- [x] P1 串行：完成方案比较与修复 Spec。
- [x] HARD-GATE：用户已显式确认修复计划。
- [x] P2 串行：测试先行实现 context-aware keyed lock。
- [x] P3 串行：接入 Codex 控制命令总时限与锁等待时限。
- [x] P4 串行：补 switch/owner 超时语义和确定性并发测试。
- [x] P5 串行：完成定向、全量、race、vet、构建和文档门禁验证。
- [x] P6 串行：完成 Review Gate 并回填验证记录。

## 当前状态

修复、验证与 Review Gate 已完成；发布状态以 GitHub Release 和版本 tag 为准。

## Review 小结

- 消除了 Codex 会话控制命令的永久锁等待，并保留原有 binding/thread 状态顺序。
- switch 超时明确保留已提交选择；owner 超时不提交控制意图、不误报成功。
- 全仓测试、messaging race、vet、build、文档校验与差异门禁均通过。
