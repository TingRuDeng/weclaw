# 当前任务记录

## 目标

把 WeClaw 从“通过共享 rollout 观察 Codex App，再由独立 app-server 恢复历史”升级为“通过 Codex Desktop IPC 实时接管同一个本地 thread”。

## 当前阶段

书面 Spec 与函数级实施计划均已确认，正在隔离分支按 TDD 和双重审查执行。

## 任务清单

- [x] P1 串行：定位当前上下文丢失根因。
- [x] P1 调研：核对 OpenAI app-server、Codex Remote Control、Remodex、Happy、MobileCLI 和 Claude Channels。
- [x] P1 本机验证：只读连接 Codex Desktop IPC，确认 initialize 和状态广播可用。
- [x] P2 串行：形成 Codex Desktop 实时接管架构设计。
- [x] P2 串行：提交并由用户审查 `tasks/codex-desktop-live-takeover.md`。
- [x] P3 并行只读：分析 Agent 接入点、Messaging 时序和 Remodex 最小 IPC 契约。
- [x] P3 串行：完成并自审 `tasks/codex-desktop-live-takeover-plan.md`。
- [x] P4.1：固化 Desktop IPC frame 与 envelope。
- [x] P4.2：建立安全的 IPC client 与 macOS endpoint。
- [x] P4.3：实现 snapshot、patch 与事件投影。
- [x] P4.4：映射 Desktop 操作、审批和结构化问答。
- [x] P4.5：建立 thread owner 状态机。
- [x] P4.6：让 ACPAgent 按 owner 路由 Chat 和控制。
- [x] P4.7：安全刷新 ACP 并恢复同一 thread。
- [x] P4.8：为 Messaging 注入审批和结构化问答。
- [x] P4.9：让会话切换先绑定 owner。
- [x] P4.10：重构 active task owner 与终态幂等。
- [x] P4.11：Desktop 断线时接续 rollout。
- [x] P4.12：按实时 owner 路由消息、引导和停止。
- [ ] P5 验收：执行单测、race、静态检查、全仓验证和真实 Codex App 手工验收。
- [ ] P6 后续独立任务：为 Claude Channels 编写单独 Spec，不与本轮 Codex 改动混合。

## 并行说明

实施计划阶段使用 3 个只读 subagent，并行分析 Agent/ACP、Messaging 和 Remodex IPC；主流程统一整合。核心 owner、ACP lifecycle、active task 与 route binding 仍必须串行实现，只有无写冲突的测试夹具和验证任务可并行。

## Review 小结

基线 `go test ./... -count=1 -timeout 120s` 已通过。计划共 13 个 TDD Task、64 个可勾选步骤；每个实现 Task 都必须依次通过 Spec 审查和代码质量审查。
