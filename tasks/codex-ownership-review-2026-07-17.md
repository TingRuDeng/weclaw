# Codex 会话与所有权 · 需求符合度审查

> 日期：2026-07-17（当晚复审更新）· 基线：HEAD `1807819`（修复 Codex 切换后仍写旧会话）· 范围：Codex 会话与所有权，个人远程接管场景

## 复审记录（基线 caaa137 → 1807819）

初审基线 `caaa137` 之后新增一个提交 `1807819 修复 Codex 切换后仍写旧会话`，修复了一个**初审遗漏的真实缺陷**：

- **缺陷**：`HandoffCodexRuntime` 的快速复用路径（目标 thread 的 binding 已是 WeClaw runtime 时直接返回，`agent/codex_runtime_probe.go:72-76`）没有同步 ACP 的 `conversationID → threadID` 映射。`/cx switch` 到该类 thread 后，owner/workspace 已指向新 thread，但下一条普通消息经 `requireThread` 仍读旧映射、resume 已归档的旧 thread，报 `session is archived`。实机用户日志暴露。该缺陷直接违背需求①的隐含约束——写入必须落到显式选中的会话。
- **修复核查（本次复审确认闭环）**：
  - 全部激活 WeClaw runtime 的路径现已同步映射 + 清 `resumeOnFirstUse` + 落盘：快速复用与恢复路径走新增 `bindCodexAppServerThread`（`agent/codex_runtime_probe.go:76`、`:180`）；`createThread`（`agent/acp_threads.go:127-136`）与 `UseCodexThread`（`:51-60`）原本就同步。`a.threads` 的写入点经穷举仅此四处 + 状态加载 + 显式清除。
  - 写入侧只信任该映射：`chatCodexAppServerControlledTurn` → `requireThread`（`agent/codex_app_server_turn.go:64`），且 turn/start 遇 missing-thread 会先 resume 再重试（`:86-93`）。
  - `probeCodexRuntime` 不会凭空产生 WeClaw binding（仅在原本已是 WeClaw 时返回 WeClaw），故不存在绕过同步的第五条激活路径。
  - 回归测试 `TestHandoffCodexRuntimeRemoteRebindsConversationWhenReusingKnownWeClaw` 同时断言内存映射、`resumeOnFirstUse`、持久化 state 文件与零 Desktop 探测，符合 lessons 要求。
- **初审教训**：初审把快速复用路径当作纯延迟优化核对，未把「ACP conversation 指针与 owner 选择一致」纳入写入授权链完整性检查。后续审查该链路时应同时核对 owner registry 与 ACP thread map 两套状态。
- 复审验证：`go test ./messaging ./agent -count=1` 在 `1807819` 上通过；差距清单 G1–G5 状态逐项复核未变化。

## 评估基准（用户确认的需求）

1. **显式选择才接管**：普通消息不隐式创建/接管会话。
2. **重启后自动恢复、直接可写**：WeClaw 服务重启后，已接管的飞书窗口发普通消息应自动恢复运行通道并直接执行，无需重新 `/cx` 或 `/cx owner remote`。
3. **飞书绝对优先**：只要飞书持有 owner，任何技术性异常（Desktop 探测失败、checkpoint/rollout 问题）都不得阻止写入；仅当飞书显式 `/cx owner desktop` 释放时才让位。真并发保护（writer lease、真实 turn 冲突、跨窗口 owner）保留。

## 总结论

**当前实现与三条需求一致，未发现阻断性偏差。** 需求 ② ③ 由 `fda32c1 确立飞书远程写入优先级`、`caaa137 移除旧会话首次写入猜测` 达成；`1807819` 进一步修复了切换后写入落点错误（见复审记录），需求①的"写入必须落到所选会话"随之闭环。2026-07-18 后续推进已同步滞后文档、修正恢复日志文案并清理 checkpoint 死代码；主要剩余为一个刻意保守的边界（旧版空会话不自动补建）。

验证：`go test ./messaging ./agent -count=1` 在 `caaa137` 与 `1807819` 上均全部通过。后续 `v0.1.198` 发布流程已在 `1807819` 上完成，覆盖全仓测试、race、vet、staticcheck、govulncheck、文档校验、diff 空白检查、四平台资产构建与校验。

## 逐项结论

### ① 显式选择才接管 —— 符合

- 无绑定普通消息只提示、不隐式创建/接管：`messaging/agent_conversation.go:104`、`:110`。
- 接管入口收敛为显式动作，统一走 `acquireCodexSessionWithBindingLocked`。
- 显式释放给 Desktop 后普通消息不抢回：`ensureCodexRouteOwnsControl`（`messaging/codex_runtime_binding.go:194-208`）对 `desktop`/`unclaimed`/其他窗口一律拒绝；飞书端弹所有权选择卡（`messaging/codex_task_start.go:46-65`）。

### ② 重启后自动恢复、直接可写 —— 代码符合（本日新达成），文档未更新

恢复链路（逐环节核实）：

1. binding + remote owner + revision + `PendingFirstTurn` 全部落盘，重启即加载：`messaging/codex_session_persistence.go:13` 起 `load()`。
2. 重启后首条普通消息：`preflightCodexTaskStart` 只校验持久化 owner，runtime 快照错误显式忽略：`messaging/codex_task_start.go:32-38`。
3. `RunCodexTurn` 中 runtime 为 unknown/conflict 时自动 `HandoffCodexRuntime`：`agent/codex_runtime_turn.go:20-28` → Desktop 探测不确定被宽恕（`agent/codex_runtime_probe.go:78-81`）→ `recoverCodexRuntimeForRemote` 重启 app-server + `thread/resume`（`:163-183`），本条消息正常执行。
4. 空会话（`/cx new` 后未发首条消息）跨重启：resume 报 `no rollout found` 归类为 missing-thread（`agent/acp_errors.go:12`），`PendingFirstTurn` 成立时自动补建新 thread 并原子迁移 binding/owner（`agent/codex_runtime_turn.go:48-76` + `messaging/codex_first_turn_recovery.go`）。
5. 冲突态只存在于进程内存、不落盘，重启即消失（第一个后台穷举扫描确认），所有 binding 恢复为 unknown，由首条消息自动恢复。
6. 回归测试：`TestHandoffCodexRuntimeRemoteRecoversWhenDesktopOwnershipIsUnknown`、`...RecoversPendingFirstTurnWithoutCheckpoint`、`...DoesNotLetCheckpointVetoOwner`、`...IgnoresPartialCheckpoint`（`agent/codex_desktop_owner_test.go:272-389`）及 `codex_first_turn_recovery_test.go`、`agent/codex_runtime_first_turn_test.go`。

行为说明（非缺陷）：恢复是惰性的，重启后首条消息承担 app-server 重启 + resume 延迟，启动时不预热。

### ③ 飞书绝对优先 —— 符合（本日新达成）

- rollout/checkpoint 读取失败降级、不否决写入：`buildCodexRuntimeRequestForTurn`（`messaging/codex_runtime_binding.go:106-120`）。
- Desktop IPC 不可达、探测超时、遗留 conflict 均不阻断：`canRecoverCodexRuntimeForRemoteOwner`（`agent/codex_runtime_probe.go:98-102`）。
- 保留的写入拒绝全部基于真实写入证据或显式授权状态：WeClaw 同 thread writer lease 忙（`agent/codex_runtime_probe.go:64-66`）、turn 执行中 lease 被抢的取消信号（`agent/codex_runtime_turn.go:36,41`）、其他远程窗口持有 owner、已显式释放给 Desktop。
- 仅显式 `/cx owner desktop` 让位，要求当前窗口确实持有 owner + 任务空闲 + CAS 提交（`messaging/codex_owner_command.go:87-143`）；释放后即使 Desktop 同步失败也不回滚，明确回复"远程写入已关闭"。
- 穷举验证：全仓现存冲突标记点仅 3 个，2 个为真实 Desktop turn 身份冲突、1 个为 `/cx new` 失败且旧映射恢复失败的 fail-closed 不确定态；三者均有自动或显式恢复路径，不存在技术性异常导致的永久阻断。

## 差距清单

| # | 差距 | 影响 | 建议 |
|---|---|---|---|
| G1 | README_CN / README / AI_CONTEXT 已同步：已持久化 remote owner 的普通消息可惰性恢复 WeClaw app-server，rollout/checkpoint 读取失败不否决写入授权 | 已关闭 | 无需处理 |
| G2 | v3 之前创建的旧空会话跨重启不自动补建（`caaa137` 刻意移除猜测迁移），旧空会话 resume 失败需手动 `/cx new` | 低 — 一次性、窗口极小 | 接受；属"已有历史 thread 不得自动替换"的安全取舍 |
| G3 | 恢复日志文案已从"显式远程接管"改为 "remote owner"，覆盖显式接管与普通消息惰性恢复 | 已关闭 | 无需处理 |
| G4 | 初稿仅验证 `go test ./messaging ./agent`；后续 `v0.1.198` 发布流程已覆盖发布级门禁 | 已关闭 | 无需处理；保留为复审轨迹 |
| G5 | `ErrCodexCheckpointRequired`、`ErrCodexCheckpointChanged` 已清理 | 已关闭 | 无需处理 |

## 设计确认（非差距）

- 微信与飞书窗口互为"其他远程窗口"，跨窗口切换需显式重新接管 —— 与"显式选择才接管"一致，单人多端属预期摩擦。
- 已有历史的会话若 rollout 真丢失（非空会话遇 `no rollout found`），每条消息诚实报"恢复 Codex thread 失败"，不自动补建、不标冲突，需用户显式 `/cx new` 或切换 —— 防止静默丢上下文的有意设计。

## 建议下一步

G1、G3、G5 已完成；G4 已由 `v0.1.198` 发布流程关闭。当前仅剩 G2 为已接受的保守边界：旧版空会话跨重启不自动补建，如需改变该策略再单独设计迁移。
