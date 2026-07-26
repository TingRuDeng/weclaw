# 当前任务记录

## 2026-07-26 Codex 官方 daemon 与账号恢复

## 任务清单

- [x] 区分 SQLite 状态库争用、损坏、明确版本不兼容和未知错误，禁止通用错误或 socket 超时触发 CLI 更新。
- [x] 无权威旧账号回滚点时记录可重试的 `external_sync_deferred`，避免错误进入永久 `rollback_failed`。
- [x] 增加 `codex_host_mode`，在官方 standalone 可用时通过 daemon 生命周期连接唯一 Host，且不回退第二 Host。
- [x] 验证 daemon backend、socket、PID、启动时间、受管路径和 generation，并让 Web 配置完整保留新字段。
- [x] 完成定向、全仓、race、vet、staticcheck、依赖差异、文档门禁、四目标交叉构建和独立交付复核。
- [ ] 安装官方 standalone 后执行真实 daemon start/version/stop 与账号切换 smoke；`govulncheck` 需在明确允许向外部漏洞库发送依赖元数据后执行。

## 当前状态

实现与本地可执行门禁已完成，独立复核为有条件通过。当前机器没有官方 standalone 和 daemon control socket，无法执行真实 daemon smoke；Codex Desktop 尚不连接本机 daemon control socket，因此不能把 App UI 描述成同一 Host 客户端。
