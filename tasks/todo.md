# 当前任务记录

## 目标

将 Claude 远程后端收敛为 ACP，并补齐真实 session 列表、恢复、实时进度、审批、停止、排队续跑、模型配置和本地空闲交接。

完整 Spec 与执行计划：`tasks/claude-acp-remote-control.md`。

## 任务清单

- [x] P0 并行只读：分析 Agent ACP 能力、消息会话链路和配置迁移边界。
- [x] P0 并行只读：核对 `claude-agent-acp` 官方 session 能力。
- [x] P1 串行：确认现状分析与目标边界。
- [x] P1 串行：确认功能点与验收标准。
- [x] P1 串行：确认风险、架构决策和失败语义。
- [x] P1 串行：确认文件级执行计划与验证矩阵。
- [x] P2 串行：写入 Spec、TDD 计划和并行文件所有权。
- [x] P3 串行：最终 HARD-GATE 已批准。
- [x] P4 串行：建立 ACP 能力与 Claude session 接口。
- [ ] P5 并行（进行中）：实现 ACP session、配置、进度和配置迁移。
- [ ] P6 并行：实现 Claude 会话导航与通用任务控制。
- [ ] P7 串行：整合、删除死路径并同步文档。
- [ ] P8 并行验证：定向测试、全量测试、race、vet、staticcheck、构建和文档门禁。
- [ ] P9 串行：执行 `review-gate` 并记录 Review 小结。

## 当前状态

任务 1 已通过规格复审与代码质量复审；正在按 TDD 执行任务 2“实现 ACP 会话目录与恢复”。

## Review 小结

任务 1 已验证：定向测试、Race、`agent` 与 `cmd` 全包测试、Vet、`git diff --check` 全部通过。最终 Review Gate 将在全部实现和验证完成后统一执行。
