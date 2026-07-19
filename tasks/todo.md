# 当前任务记录

## 目标

为单一共享 Codex app-server 增加主机级 ChatGPT OAuth profile 管理。切换只替换共享运行时身份，
保留全部 workspace、thread 与 frontend binding，并在目标验证或回滚不确定时 fail-closed。

## 任务清单

- [x] Task 1：实现按 `CODEX_HOME + socket` 隔离的 `codexauth` 存储、Keyring/Secret Service 优先后端与显式文件降级。
- [x] Task 2：补齐受管 Host PID/UID/启动时间/命令/generation 元数据和安全生命周期控制。
- [x] Task 3：实现 task、writer lease、全量 thread 门禁下的在线切换、目标验证、完整回滚和旧 wire epoch 隔离。
- [x] Task 4：实现 `weclaw codex account` CLI、本机控制 API，以及服务在线/离线的 fail-closed 分流。
- [x] Task 5：实现 `/cx account` 管理员私聊、飞书分页选择、二次确认、进展与原卡终态更新。
- [x] Task 6：同步中英文帮助、架构上下文与长期经验。
- [x] Task 7：执行全仓测试、race、vet、staticcheck、govulncheck、依赖、文档与独立交付复核。

## 当前状态

功能实现、自动化门禁和独立交付复核已完成；复核发现的在线保存账户未强制验证受管 Host 问题已修复并补回归测试。
尚未执行本机真实 OAuth 账号切换，以免在自动验证中修改当前用户认证；真实 Keychain/Secret Service 与两账号 Host 重启切换仍列为人工验收项。
