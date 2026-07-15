# 当前任务记录

## 目标

实现 Codex 远程窗口“选择即接管”：切换 A→B 时原子释放 A、接管 B，
活动 Desktop 任务继续在原进程执行并立即回传，当前窗口可使用 `/guide`、`/stop`。

## 任务清单

- [x] P0 并行：只读核对 switch/new/owner/导航入口、store/锁和 active-task/runtime 边界。
- [x] P1 串行：完成并确认“单窗口单所有权、其他窗口不可抢占”设计。
- [x] P2 串行：补全当前默认 Agent 为 Codex 时的全局 `/new` 入口并形成文件级实施计划。
- [x] P3 串行（5/5）：Task 1–5 已完成并通过独立审查，原子状态、锁、观察屏障与统一 saga 已闭合。
- [ ] P4 串行（1/3）：Task 6 已完成；继续 Task 7–8，接入两个 new 入口和 owner/消息门禁。
- [ ] P5 串行：Task 9，补齐平台、路由、并发与重启行为矩阵。
- [ ] P6 串行：Task 10，同步公开语义，执行全量测试、race、vet、文档门禁和 Review Gate。

## 并行说明

只读分析阶段已拆分三个互不冲突的 subagent：入口地图、store/锁、runtime/observer。
实施会共享 session/control/runtime/active-task 状态与测试 fixture，写冲突风险高，
因此按 Task 1–10 串行推进；只允许独立验证命令并行。

## 当前状态

用户已通过 HARD-GATE 并选择 Subagent-Driven 执行；全仓基线测试已通过，
当前 Task 1–6 已完成并通过任务级审查；Task 6 最终提交为 `bf63a88`、
`86c03b7`，正按严格 TDD 进入 Task 7。

## Review 小结

- 计划覆盖所有已证实的真实入口，包括原设计遗漏的 Codex 默认 Agent 全局 `/new`。
- 核心事务明确外层 binding 锁前提，内部只按 thread ID 有序持锁，避免自锁与 ABBA。
- 运行时变更、observer 预留、copy-on-write 持久化和失败补偿均有对应自动化验证任务。
- Task 1 已证明显式 thread 不会被 stale ACP 映射覆盖，空状态仍可正常回填。
- Task 2 已实现选择、目标所有权和同 route 旧所有权的单次 copy-on-write 持久化，store/race 验证通过。
- Task 3 已实现共享等待预算下的多 thread 去重排序、逆序释放与幂等解锁，并补齐确定性 ABBA 回归测试。
- Task 4 已实现外部活动任务 prepare/reserve/activate/cancel 成功屏障、reserved 控制隔离与本进程任务兼容排队。
- Task 5 已实现统一选择接管 saga、持续 conflict/fail-closed、全错误单次校准与共享 cleanup 预算。
- Task 6 已让 switch、短编号、`/cx cd` 与飞书卡片共用统一事务，并保留 rollout-only 只读观察与控制边界。
- 已执行 Task 1 定向 RED/GREEN 与 `messaging` 全包回归；全仓 race、vet 和文档门禁仍按 Task 10 统一验收。
