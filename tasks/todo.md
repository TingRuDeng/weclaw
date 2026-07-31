# 当前任务记录

## 2026-07-26 Codex 官方 daemon 与账号恢复

## 任务清单

- [x] 区分 SQLite 状态库争用、损坏、明确版本不兼容和未知错误，禁止通用错误或 socket 超时触发 CLI 更新。
- [x] 无权威旧账号回滚点时记录可重试的 `external_sync_deferred`，避免错误进入永久 `rollback_failed`。
- [x] 增加 `codex_host_mode`，在官方 standalone 可用时通过 daemon 生命周期连接唯一 Host，且不回退第二 Host。
- [x] 验证 daemon backend、socket、PID、启动时间、受管路径和 generation，并让 Web 配置完整保留新字段。
- [x] 完成定向、全仓、race、vet、staticcheck、依赖差异、文档门禁、四目标交叉构建和独立交付复核。
- [ ] 在不影响活动任务的隔离窗口执行真实 daemon start/version/stop 与账号切换 smoke；GitHub CI 漏洞扫描已通过，本地 `govulncheck` 仍需在明确允许向外部漏洞库发送依赖元数据后执行。

## 当前状态

实现与本地可执行门禁已完成，独立复核为有条件通过。当前开发机已安装官方 standalone，且现有 WeClaw 后台服务运行正常；本轮没有为验证而中断活动服务，因此隔离的 daemon 生命周期与账号切换 smoke 仍待执行。Codex Desktop 与受管 daemon 的连接状态必须继续独立核验，不能只根据 App UI 推断。
