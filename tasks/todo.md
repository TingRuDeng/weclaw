# 当前任务记录

## 目标

把 WeClaw 从“通过共享 rollout 观察 Codex App，再由独立 app-server 恢复历史”升级为“通过 Codex Desktop IPC 实时接管同一个本地 thread”。

## 当前阶段

WeClaw 重启后的 owner 恢复已修复并通过自动化验收，等待发布后实机复测 `/cx switch`。

## 任务清单

- [x] P1 串行：定位当前上下文丢失根因。
- [x] P1 调研：核对 OpenAI app-server、Codex Remote Control、Remodex、Happy、MobileCLI 和 Claude Channels。
- [x] P1 本机验证：只读连接 Codex Desktop IPC，确认 initialize 和状态广播可用。
- [x] P2 串行：形成 Codex Desktop 实时接管架构设计。
- [x] P2 串行：提交并由用户审查 `tasks/codex-desktop-live-takeover.md`。
- [x] P3 并行只读：分析 Agent 接入点、Messaging 时序和 Remodex 最小 IPC 契约。
- [x] P3 串行：完成并自审 `tasks/codex-desktop-live-takeover-plan.md`。
- [x] P4.1：固化 Desktop IPC frame 与 envelope。
- [x] P4.2：建立安全的 IPC client 与 macOS endpoint。
- [x] P4.3：实现 snapshot、patch 与事件投影。
- [x] P4.4：映射 Desktop 操作、审批和结构化问答。
- [x] P4.5：建立 thread owner 状态机。
- [x] P4.6：让 ACPAgent 按 owner 路由 Chat 和控制。
- [x] P4.7：安全刷新 ACP 并恢复同一 thread。
- [x] P4.8：为 Messaging 注入审批和结构化问答。
- [x] P4.9：让会话切换先绑定 owner。
- [x] P4.10：重构 active task owner 与终态幂等。
- [x] P4.11：Desktop 断线时接续 rollout。
- [x] P4.12：按实时 owner 路由消息、引导和停止。
- [x] P5.1 自动验收：受影响包与全仓单测、race、vet、staticcheck、文档校验、差异检查。
- [x] P5.2a 实机验收：连接 Desktop IPC、切换同一会话、识别本地 active turn 并暂存飞书消息。
- [x] P5.2b 实机修复：兼容 discovery 嵌套未知方法、workspaceWrite writableRoots、长会话 turnHistory 和已知状态广播。
- [x] P5.2c 实机修复：观察器绑定原 turn ID，并通过状态复核补偿终态事件链断裂。
- [x] P5.2d 实机修复：过滤未接管 thread 广播，并等待 history 响应 revision 真正落入缓存。
- [x] P5.2e 实机验收：本地 turn 结束后自动回推结果，并自动执行暂存的飞书消息。
- [ ] P5.2 手工验收：真实飞书机器人与 Codex App 的接管、控制、审批、断线和恢复场景。
- [x] P5.3a 串行 TDD：在 `messaging/progress_test.go` 复现短任务未产生进度时仍创建空完成卡；修改 `messaging/progress.go`，将原生进度流延迟到首次真实进度或定时进度时创建。
- [x] P5.3b 串行 TDD：在 `messaging/handler_codex_live_message_control_test.go` 复现排队外部任务丢失账号级 `stream` 配置；修改 `messaging/codex_task_start.go` 与 `messaging/codex_external_task.go`，直接继承已解析的任务进度配置。
- [x] P5.3c 串行 TDD：更新 `messaging/task_commands.go` 的暂存提示为单行简洁文案，并调整相关断言，保留 `/guide`、`/cancel` 命令能力但不重复展示操作说明。
- [x] P5.3d 串行自动验收：运行 `go test ./messaging ./feishu`、全仓单测、race、vet、文档校验和 `git diff --check`。
- [ ] P5.3e 发布后实机复测：用飞书验证短任务无空完成卡、排队提示单行且自动续跑结果正常返回。
- [x] P5.4a 串行 TDD：复现普通消息沿旧 `desktop_live` 绑定发送时收到 `no-client-found`，却未恢复同一 thread 的问题。
- [x] P5.4b 串行实现：仅在 Desktop 明确返回 `no-client-found` 时确认 release，恢复到 WeClaw app-server 并单次重试原消息。
- [x] P5.4c 串行验证：断线与交付状态未知不得触发回退，并完成受影响测试、全仓测试、race、vet、staticcheck 和交付审查。
- [x] P5.5a 串行 TDD：复现 `weclaw_runtime` owner 在重启快照中被丢弃，导致原 thread 不可恢复的问题。
- [x] P5.5b 串行实现：把 WeClaw 已持有的 thread 持久化为已确认释放的 `persisted_only`，重启后仍恢复同一 thread。
- [x] P5.5c 串行验证：确认 Desktop live/disconnected 不会被错误标记为可恢复，并完成全仓自动验收与交付审查。
- [ ] P6 后续独立任务：为 Claude Channels 编写单独 Spec，不与本轮 Codex 改动混合。

## 并行说明

实施计划阶段使用 3 个只读 subagent，并行分析 Agent/ACP、Messaging 和 Remodex IPC；主流程统一整合。核心 owner、ACP lifecycle、active task 与 route binding 仍必须串行实现，只有无写冲突的测试夹具和验证任务可并行。

## Review 小结

2026-07-11 自动验收通过：`go test ./...`、`go test -race ./...`、`go vet ./...`、`staticcheck ./...`、文档校验和 `git diff --check` 均为退出码 0。Review gate 未发现阻塞性代码问题；真实 App 手工验收尚未执行。

2026-07-12 实机发现 Desktop 长会话会按 `inProgress -> 暂时移除 -> completed` 两次修订归档 turn，旧投影器因此遗漏终态。已增加跨修订活动 turn 指纹并完成 RED/GREEN；`go test ./...`、`go test -race ./...`、`go vet ./...`、文档校验和 `git diff --check` 通过。当前环境缺少 `staticcheck` 可执行文件，本轮未重复执行；飞书自动回推仍等待当前实机 turn 结束验证。

2026-07-12 第二次实机验收确认仅修复投影仍不充分：Desktop turn 已完成，但纯事件观察器没有复核权威状态，`active_tasks` 继续保持 1。已增加原 turn ID 隔离和每两秒状态复核测试；第五版临时服务已启动，等待再次实机确认自动回推。

2026-07-12 第三次实机验收确认缓存 revision 落后于 Desktop：缓存为 `4340`，实时状态已到 `4533`，并把已完成 turn 当成 active。第六版已过滤未接管 thread 的初始化广播，解析 `load-complete-history` 返回 revision，并等待同一连接代次的状态缓存达到该 revision 后才完成绑定；全仓测试和 race 通过。直接飞书消息已成功返回，真正的 active task 暂存续跑仍待最终确认。

2026-07-12 第四次实机验收确认本地 turn 结束后，飞书暂存消息于 `09:19:12` 自动出队，`09:19:21` 返回最终结果，随后 `active_tasks=0`；用户截图确认结果已到达飞书。展示优化进一步修复排队路径丢失账号级 `stream` 配置、短任务提前创建空完成卡和重复操作提示。发布前全仓单测、race、vet、文档校验和差异检查通过；当前环境未安装 `staticcheck`。

2026-07-12 发布后实机日志确认 Android thread 的旧 `desktop_live` 绑定在 Desktop 返回 `no-client-found` 后仍被普通消息复用。已增加确定性 release 转移、同 thread app-server 恢复和原消息单次重试；断线与交付未知负向测试通过。`go test ./...`、`go test -race ./...`、`go vet ./...`、`staticcheck ./...`、文档校验和 `git diff --check` 均为退出码 0。

2026-07-12 重启后 `/cx switch` 失败的根因是 `persistedBindings` 丢弃 `weclaw_runtime` owner，conversation 虽保留 thread 映射，却失去可恢复证据。现将进程内 owner 跨重启转换为 `persisted_only + ReleaseConfirmed`，并保留 Desktop live/disconnected 的保守边界；写盘、重建 Agent、重启 app-server、`thread/resume` 原 thread 的回归测试通过。`go test ./...`、`go test -race ./...`、`go vet ./...`、`staticcheck ./...`、文档校验和 `git diff --check` 均为退出码 0。
