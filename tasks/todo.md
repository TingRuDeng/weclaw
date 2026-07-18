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

## 当前状态

重构、权威文档同步、全仓门禁和发布级独立复核均已完成，未发现遗留阻断项。当前 Codex Desktop 不是共享
host 客户端，独立启动的 Desktop 不在 WeClaw 管理边界内；后续本地 UI 必须连接同一 app-server，不能重新
启动第二 writer。正式发布必须在 PR 合并到 clean `main` 后通过 `scripts/release.sh --next-patch` 执行。
