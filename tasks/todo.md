# 当前任务记录

## 2026-07-23 审批兜底与 outbox 运维

- [x] 审批卡生成绑定操作者、窗口和过期时间的文本短码，支持 `/approve`、`/deny` 幂等兜底。
- [x] 增加脱敏 outbox 状态、在线 redrive、本地 API、CLI 与 doctor 检查，不改变 at-least-once 和无限重试语义。
- [x] 完成定向测试、全仓门禁和独立交付复核。

## 目标

在既有结构化进展和终态 outbox 之上，补齐可脱敏查询的端到端 Trace、Codex 双轨协议诊断与统一任务视图 reducer。

## 任务清单

- [x] Task 1：从平台入站、任务接纳、Agent turn、结构化进展、回复到终态 outbox 传播同一 Trace，并使用固定字段和脱敏摘要落盘。
- [x] Task 2：提供只允许真实 loopback 且复用 API token 的 `/api/traces`，以及在线失败关闭、离线只读的 `weclaw trace`。
- [x] Task 3：在 Codex ACP 线边界记录 raw protocol metadata 与归一化事件；协议正文必须显式启用并递归脱敏。
- [x] Task 4：以纯 reducer 统一任务卡和 `/ps` 的运行态快照，并让终态压过晚到进展。
- [x] Task 5：完成全仓测试、race、vet、staticcheck、govulncheck、依赖与文档门禁。
- [x] Task 6：执行独立交付复核并收敛发现。

## 当前状态

Trace、协议诊断和统一任务视图已实现；全仓门禁、正式目标交叉构建和独立复核均已通过。协议正文默认不记录；即使显式启用脱敏正文，也只适合短期本机诊断。独立复核发现的 symlink 竞态、超长协议关联 ID 和广播分支 Trace 缺口均已修正并回归。
