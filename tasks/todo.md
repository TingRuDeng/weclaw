# 当前任务记录

## 目标

把 WeClaw 从“通过共享 rollout 观察 Codex App，再由独立 app-server 恢复历史”升级为“通过 Codex Desktop IPC 实时接管同一个本地 thread”。

## 当前阶段

Spec 设计与审查。用户已确认架构方向，尚未批准书面 Spec，禁止修改生产代码。

## 任务清单

- [x] P1 串行：定位当前上下文丢失根因。
- [x] P1 调研：核对 OpenAI app-server、Codex Remote Control、Remodex、Happy、MobileCLI 和 Claude Channels。
- [x] P1 本机验证：只读连接 Codex Desktop IPC，确认 initialize 和状态广播可用。
- [x] P2 串行：形成 Codex Desktop 实时接管架构设计。
- [ ] P2 串行：提交并由用户审查 `tasks/codex-desktop-live-takeover.md`。
- [ ] P3 串行：书面 Spec 确认后编写函数级实施计划。
- [ ] P4 执行：按批准计划先写失败测试，再实现 Desktop IPC 接管。
- [ ] P5 验收：执行单测、race、静态检查、全仓验证和真实 Codex App 手工验收。
- [ ] P6 后续独立任务：为 Claude Channels 编写单独 Spec，不与本轮 Codex 改动混合。

## 并行说明

当前不使用 subagent。IPC owner、ACP owner、active task 与 route binding 共享同一状态链，设计与核心实现必须串行；实施计划确认后仅把无写冲突的测试夹具或协议解析任务并行拆分。

## Review 小结

待书面 Spec 审查后补充。
