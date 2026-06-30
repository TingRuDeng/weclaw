# Implementation Plan — 安全治理与体验增强

## Overview

按用户选定顺序推进 5 项改进，分两组交付：
- **安全治理组（PR-A）**：#1 `/cwd` 工作目录白名单、#4 并发/速率限制、#2 审计日志。围绕"收紧 shell 暴露面"。
- **能力增强组（PR-B）**：#5 agent 产物回传、#6 `weclaw doctor` 增强 + Web 配置面板。

约束：不破坏平台抽象与微信/飞书行为；每步 `go build/vet/test ./...`（含 `-race`）全绿。

## Tasks

### 安全治理组（PR-A）

- [x] 1. `/cwd` 工作目录白名单
  - config 新增 `allowed_workspace_roots`；`/cwd` 仅允许切到白名单根及子目录，空=不限制(启动日志+doctor 告警)
  - doctor 增加 workspace confinement 检查
  - 测试：白名单内允许、外拒绝、空不限制、doctor 告警

- [ ] 4. 并发 / 速率限制
  - 每用户(routeUser)并发任务上限 + 简单速率限制，防跑飞/滥用/烧 token
  - 复用 activeTasks 计数；超限给出友好提示
  - 测试：超并发拒绝、限流窗口

- [ ] 2. 审计日志
  - 结构化记录 who(user/platform)/when/agent/消息摘要/动作到独立审计文件
  - 不含密钥；可配置开关与路径
  - 测试：审计条目格式与脱敏

### 能力增强组（PR-B）

- [ ] 5. agent 产物回传（本地文件→聊天）
  - 约定 agent 输出本地文件路径 → 在白名单根内则作为附件回传
  - 复用 isAllowedAttachmentPath 限制可发送路径
  - 测试：白名单内回传、外拒绝

- [ ] 6. `weclaw doctor` 增强 + Web 配置面板
  - doctor：补充更多检查项（workspace 已加）
  - Web 面板 `weclaw web`：可视化配置 platforms/凭证/allowed_users（评估后实现）

## Notes

- `/cwd` 白名单默认空=不限制以兼容老用户，但启动与 doctor 均显著告警，引导配置。
- 速率限制默认宽松，避免误伤正常使用；可配置。
- 审计日志默认开启、仅本地文件、绝不含 secret。
