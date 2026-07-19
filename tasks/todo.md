# 当前任务记录

## 目标

将 Codex 与 Claude 都收敛为单一运行 host、多前端 binding：窗口只持久化会话选择，
真正写入分别按 Codex thread 和 Claude session 获取唯一 writer lease，运行通道故障不反向清除 binding。

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
- [x] Task 10：将 Claude 状态迁移到 v4 binding-only，加载后丢弃旧 owner/control intent 并保留全部 route binding。
- [x] Task 11：让所有前端复用进程驻留 ClaudeHost，同一 session 在同一 host generation 只执行一次有效 `session/resume`。
- [x] Task 12：将 Claude 活动任务键收敛为 session writer lease；同 route 可排队一条，其他 route 明确 busy。
- [x] Task 13：同步 README、帮助卡、AI 上下文与长期经验，停用 `/cc owner`、`/cc cli`。
- [x] Task 14：完成 ClaudeHost 生命周期、多前端绑定、单 writer、迁移和失败补偿的全仓测试、race 与发布级复核。

## 当前状态

`v0.1.204` 实机 `/cx ls` 暴露真实 Unix socket 需要 WebSocket Upgrade，而原回归 fake 错用了裸 JSONL。`v0.1.205`
候选已将传输层改为 WebSocket-over-UDS，补齐真实 Upgrade、分片写入、断线清理和 macOS 长路径回归，并通过真实 Codex
`Start → initialize → Stop` 烟测、全仓测试、race、vet、staticcheck、依赖与文档门禁。远端 Release 是否成功、
本机是否经 `weclaw update` + restart 加载新版本，必须分别核验，不能由仓库任务文件代替。当前 Codex Desktop
仍不是共享 host 客户端，后续本地 UI 必须连接同一 app-server，不能重新启动第二 writer。发布脚本现已统一配置持久化
`GOCACHE`，本机 Darwin 会选中 `/Volumes/Data/AppData/BuildCaches/weclaw`，Linux/CI 保持可移植回退。

Claude 重构已完成 binding-only v4、进程驻留共享 ClaudeHost、host-side session 复用和 session writer lease，
并通过全仓测试、race、vet、staticcheck、govulncheck、依赖、文档与独立发布级复核。当前实现的 ClaudeHost 由 WeClaw 服务内唯一 ACP stdio 连接承载，尚未对外暴露
类似 Codex 的 Unix socket；任何本地 Claude UI 若要接入，后续必须复用该 host 协议，不能启动独立 `claude --resume` writer。
