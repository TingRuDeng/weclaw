# Implementation Plan — P1 聊天控制与会话治理

## Overview

落实代码审查后列出的 P1 改进（借鉴 cc-connect）。本 spec 分两批：
- **批次 A（本轮）聊天控制**：`/mode yolo|default` 权限模式、`/cancel` 中止当前轮、`/ps` 查看运行中任务。三者贴合现有 codex/审批/active task 结构，可完整测试。
- **批次 B（后续）会话治理**：`reset_on_idle_mins` 空闲自动重置会话、`/cron` 自然语言定时任务（较大子系统，单独推进）。

约束：不破坏平台抽象与微信/飞书行为；`go build/vet/test ./...`（含 `-race`）全绿。

## Tasks

### 批次 A — 聊天控制（本轮）

- [ ] 1. `/mode yolo|default` 权限模式
  - Handler 增加按 userID 的审批模式存储；`/mode` 查看、`/mode yolo` 自动放行、`/mode default` 走按钮确认
  - `approvalHandlerForUser` 在 yolo 模式下直接返回 allow 选项（不弹按钮），其余维持现有 fail-safe 默认拒绝
  - 测试：yolo 自动批准、default 走交互、未知子命令提示
  - _Requirements: P1-5_

- [ ] 2. `/stop` 中止当前运行轮
  - 新增 `/stop`：停止用户当前运行中的 codex 任务（复用 handleStopActiveTask，与飞书 stop 按钮一致）
  - `/cancel` 维持既有语义（撤回排队中的暂存引导/run 消息），不破坏既有契约
  - 测试：运行中→/stop 停止；既有 /cancel 撤回测试保持通过
  - _Requirements: P1-4_

- [ ] 3. `/ps` 查看运行中任务
  - `activeAgentTask` 记录 owner/agentName/preview/startedAt；`beginActiveTask` 登记这些元信息
  - `/ps` 列出当前用户运行中的任务（agent、已运行时长、消息预览）
  - 测试：有/无运行任务的输出
  - _Requirements: P1-4_

- [ ] 4. 帮助与回归
  - `/help` 文案补充 `/mode`、`/cancel`、`/ps`
  - 全量 `go build/vet/test ./...` + `-race` 全绿
  - _Requirements: P1-4, P1-5_

### 批次 B — 会话治理（后续）

- [ ] 5.* `reset_on_idle_mins` 空闲自动重置会话
  - 每会话空闲超阈值自动开新 session，旧会话仍可 `/cc switch` 找回，避免上下文漂移
  - _Requirements: P1-3_

- [ ] 6.* `/cron` 自然语言定时任务
  - cron 调度器 + 持久化 + 到点跑 agent 并经 Registry 主动推送
  - _Requirements: P1-6_

## Notes

- `/cancel` 的可达性：微信按用户串行投递，仅 codex 的异步 active task 在运行中可被另一条 `/cancel` 中止；其余 agent 为同步轮次，跑完才会处理下一条，无需中途取消。
- **`/cancel` vs `/stop`**：`/cancel` 保持既有语义=撤回排队中的暂存消息(不动运行任务)；新增 `/stop`=停止当前运行的任务(等同飞书 stop 按钮)。两者分工明确，避免破坏既有 guide/run 契约。
- yolo 模式按 userID（真实发送者）维度，与审批回调用户一致。
- 批次 B 的 cron 是独立子系统，体量大，单独 PR。
