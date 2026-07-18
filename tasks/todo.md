# 当前任务记录

## 目标

将 Codex 收敛为单一 app-server、多前端客户端：飞书和微信窗口只持久化 workspace/thread binding，
共享同一运行 host，并按 thread 串行写 turn；Claude 保持现有 owner-first ACP/CLI 交接模型。

## 任务清单

- [x] Task 1：实现稳定 Unix socket 的共享 Codex app-server host 和多客户端连接。
- [x] Task 2：将 Codex 状态文件迁移到 v4 binding-only，丢弃旧 owner/control 状态。
- [x] Task 3：重写选择、新建、普通消息、外部任务和 runtime 恢复路径。
- [x] Task 4：停用 `/cx app|cli|attach|detach`，迁移旧 Codex Companion 配置并删除第二 writer 实现。
- [x] Task 5：补齐长 socket 路径、host 生命周期、多前端绑定和单 thread writer lease 回归。
- [x] Task 6：完成权威文档同步、全仓门禁和发布级独立复核。
- [x] Task 7：修复真实 Codex Unix socket 的 WebSocket Upgrade，并完成真实 Codex 烟测与全仓门禁。
- [x] Task 8：发布入口统一复用持久化项目 Go 构建缓存，不再创建任务级 `/private/tmp` 缓存。
- [x] Task 9：形成 `v0.1.205` 热修复发布候选；远端 Release 与本机加载状态由发布流程独立核验。

## 当前状态

`v0.1.204` 实机 `/cx ls` 暴露真实 Unix socket 需要 WebSocket Upgrade，而原回归 fake 错用了裸 JSONL。`v0.1.205`
候选已将传输层改为 WebSocket-over-UDS，补齐真实 Upgrade、分片写入、断线清理和 macOS 长路径回归，并通过真实 Codex
`Start → initialize → Stop` 烟测、全仓测试、race、vet、staticcheck、依赖与文档门禁。远端 Release 是否成功、
本机是否经 `weclaw update` + restart 加载新版本，必须分别核验，不能由仓库任务文件代替。当前 Codex Desktop
仍不是共享 host 客户端，后续本地 UI 必须连接同一 app-server，不能重新启动第二 writer。发布脚本现已统一配置持久化
`GOCACHE`，本机 Darwin 会选中 `/Volumes/Data/AppData/BuildCaches/weclaw`，Linux/CI 保持可移植回退。
