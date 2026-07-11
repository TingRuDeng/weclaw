# Codex Desktop 实时接管 Spec

## 1. 背景

WeClaw 当前可以发现 Codex App 历史 thread、读取共享 rollout、镜像外部任务进度并回传最终结果，但它仍通过自己启动的 `codex app-server` 恢复会话。Codex App 与 WeClaw 因此分别持有独立的 app-server 进程，只共享磁盘上的 rollout，不共享正在运行的内存上下文、事件流和审批请求。

这与产品目标存在根本偏差。WeClaw 的目标不是另起一个 Agent 进程复用历史，而是让飞书、微信成为本地 Agent 运行时的远程控制端：用户在 Codex App 发起任务后离开电脑，仍可从飞书或微信查看实时状态、继续同一 thread、引导或停止当前 turn，并处理权限审批。

## 2. 目标

- 切换到 Codex App 已加载的 thread 后，飞书和微信绑定该 Desktop 运行时，而不是让 WeClaw app-server 再次加载同一 thread。
- 后续普通消息通过 Codex Desktop IPC 在原 thread 上执行 `turn/start`，完整继承 App 的内存上下文。
- 运行中的 thread 支持远程引导、停止、权限审批和用户问答。
- Codex App 的实时状态、增量文本、终态和待处理请求继续复用现有平台进度与任务模型。
- Desktop 连接中断时不静默切换运行时；只有确认 Desktop 不再持有 thread 后，才允许从完整 rollout 恢复到 WeClaw app-server。
- 保持飞书和微信相同的会话语义，不在平台 adapter 中复制 Agent 状态机。

## 3. 非目标

- 本 Spec 不实现 Claude Channels；Claude 采用相同产品语义，但另行设计和验收。
- 不把 Codex Desktop IPC 暴露为网络端口，也不改变现有飞书、微信鉴权模型。
- 不复制 Remodex 的完整移动端、relay、加密配对或 Git UI。
- 不以 PTY 字节流替代结构化 Codex 事件。
- 不把 rollout 文件升级为控制通道；rollout 仍只负责发现、断线观察和持久化恢复。
- 不支持两个运行时同时向同一 thread 写入。

## 4. 已验证事实

### 4.1 WeClaw 当前行为

- `messaging/codex_session_switch.go` 的 `handleCodexSwitchForRouteWithOptions` 在判断外部任务前调用 `CodexThreadAgent.UseCodexThread`。
- `agent/acp_threads.go` 的 `UseCodexThread` 通过 WeClaw 自己的 app-server 执行 `thread/resume`。
- `messaging/codex_external_task.go` 的 `resolveExternalCodexTask` 在独立进程场景下退回共享 rollout，只能镜像任务，不能控制 Codex App 内存中的 turn。
- `messaging/codex_external_task.go` 的 `finishExternalCodexTask` 回推终态后直接执行暂存消息，没有 live runtime 所有权交接。

### 4.2 Codex 协议事实

- 官方 app-server 协议把 Thread 定义为多轮对话，持久化 item 会成为后续 turn 的上下文。
- `thread/resume` 对未加载 thread 从磁盘恢复；对当前 app-server 已加载的 thread 只重新加入，不会合并另一个 app-server 进程新增的内存状态。
- Codex App 当前在 macOS 用户临时目录暴露本地 IPC socket，使用 4 字节小端长度前缀和 JSON envelope。
- 本机只读探测已验证 IPC `initialize` 成功，并收到 `thread-stream-state-changed` 广播。

### 4.3 同类实现证据

- Remodex 通过 Codex Desktop IPC 路由 `turn/start`、`turn/steer`、`turn/interrupt`、审批和用户问答；JSONL 只作为恢复和兼容兜底。
- Happy 和 MobileCLI 都从启动时持有唯一 Agent/PTY 运行时，再把移动端作为控制端。
- OpenAI Codex Remote Control 采用长驻本机运行时和安全 relay，同步 live state，而不是为手机启动第二个 app-server。

## 5. 核心决策

### 5.1 单一运行时所有权

每个 thread 在任一时刻只能处于一种所有权状态：

| 状态 | 权威来源 | 允许的操作 |
|---|---|---|
| `desktop_live` | Codex Desktop IPC | 读取状态、开始 turn、引导、停止、审批 |
| `weclaw_runtime` | WeClaw ACP app-server | 现有 ACP 全部能力 |
| `persisted_only` | 完整 rollout | 只读发现；可恢复为 `weclaw_runtime` |
| `desktop_disconnected` | 最近一次 Desktop 状态 + rollout | 只读观察，不允许启动新 turn |

禁止从 `desktop_live` 或 `desktop_disconnected` 静默切换到 `weclaw_runtime`。只有 Desktop 已释放所有权且 rollout 中没有 active turn，才能执行恢复。

### 5.2 Provider 原生连接器

Agent 层新增内部 live runtime 边界，消息层只依赖统一能力，不直接理解 IPC frame：

```go
type CodexLiveRuntime interface {
    ObserveThread(ctx context.Context, threadID string) (CodexLiveThread, error)
    StartTurn(ctx context.Context, req CodexLiveTurnRequest) (string, error)
    SteerTurn(ctx context.Context, threadID string, turnID string, text string) error
    InterruptTurn(ctx context.Context, threadID string, turnID string) error
}
```

实际接口可以按现有 `agent` 契约拆分，避免超过参数和函数复杂度限制；这里描述的是职责边界，不是最终函数签名。

### 5.3 Desktop IPC 优先级

切换 thread 时按以下顺序决策：

1. 查询 Desktop IPC 当前已加载 thread 快照。
2. 命中目标 thread 时绑定 `desktop_live`，不调用 `UseCodexThread`。
3. Desktop 未持有且 rollout 有 active turn 时进入 `desktop_disconnected`，继续只读镜像并等待所有权明确。
4. Desktop 未持有且 rollout 已终态时，才调用现有 ACP `UseCodexThread`，进入 `weclaw_runtime`。

## 6. Desktop IPC 协议边界

### 6.1 连接与 framing

- macOS 默认地址：`filepath.Join(os.TempDir(), "codex-ipc", fmt.Sprintf("ipc-%d.sock", os.Getuid()))`。
- frame 使用 4 字节小端无符号长度，后接 UTF-8 JSON。
- 建立连接后发送 `initialize`，客户端类型使用 `weclaw`。
- 设置具名 frame 上限、请求超时和重连退避，拒绝超长、截断或非法 JSON。
- socket 不存在表示 Desktop 能力不可用，不视为 WeClaw 启动失败。

### 6.2 首期方法版本

首期只实现已验证的最小集合：

| IPC method | 版本 | 用途 |
|---|---:|---|
| `initialize` | 1 | 建立 IPC 客户端 |
| `thread-stream-state-changed` | 11 | 接收 conversation snapshot/patch |
| `thread-follower-load-complete-history` | 1 | 请求完整历史基线 |
| `thread-follower-start-turn` | 1 | 在 Desktop-owned thread 开始新 turn |
| `thread-follower-steer-turn` | 1 | 引导 active turn |
| `thread-follower-interrupt-turn` | 2 | 停止 active turn |
| `thread-follower-command-approval-decision` | 1 | 回复命令审批 |
| `thread-follower-file-approval-decision` | 1 | 回复文件审批 |
| `thread-follower-permissions-request-approval-response` | 1 | 回复权限申请 |
| `thread-follower-submit-user-input` | 1 | 回复结构化用户问答 |

版本表集中维护，未知版本或明确拒绝时将该能力标记为不兼容，并向用户返回可操作错误。

### 6.3 所有权发现

- IPC snapshot 是已加载 thread 的直接所有权证据。
- snapshot 尚未到达时，使用 `client-discovery-request` 携带目标 follower method、对应版本和 thread 参数，等待 Desktop client 返回 `canHandle`。
- discovery 请求设置短超时；超时只表示所有权未知，不能据此把请求转发给 WeClaw app-server。
- 收到其他 client 的 discovery 请求时，WeClaw 仅对自己明确持有的 `weclaw_runtime` thread 返回 `canHandle=true`，避免抢占 Desktop thread。
- 目标 thread 的第一条远程消息在所有权探测期间有界暂存，不能同时向 Desktop IPC 和 WeClaw app-server 发送。

### 6.4 状态投影

- `snapshot` 替换某个 thread 的原始 conversation state。
- `patches` 只在已有基线时应用；缺失基线时请求完整历史，不凭空猜测状态。
- 投影结果只保留 WeClaw 需要的 thread、turn、文本 item、状态和待处理请求。
- 每个 thread 使用单调 revision 和 turn/item 标识去重，避免 snapshot 与 delta 重复回推。
- 缓存设置线程数量、单 thread turn 数和待应用 patch 数上限；达到边界时淘汰空闲 thread，不淘汰 active 或待审批 thread。

## 7. 消息与任务流

### 7.1 切换会话

1. 飞书或微信选择 thread。
2. Handler 解析 workspace 与 thread ID。
3. Agent 查询 live runtime 所有权。
4. 命中 Desktop 时保存 route/thread/owner 绑定并订阅状态。
5. 返回当前模型、推理强度、任务、进展和可用控制能力。

### 7.2 Desktop 空闲时发送消息

1. Handler 沿用当前 conversation route。
2. Agent 识别 owner 为 `desktop_live`。
3. 通过 `thread-follower-start-turn` 把消息发送到原 Desktop thread。
4. IPC 广播驱动进度、审批和最终结果。
5. 新 turn 保持相同 thread ID，Codex App 和飞书/微信看到同一上下文。

### 7.3 Desktop 运行中发送消息

- 普通消息继续沿用当前暂存语义，前一任务完成后自动执行。
- `/guide` 通过 `thread-follower-steer-turn` 进入当前 turn。
- `/stop` 通过 `thread-follower-interrupt-turn` 停止当前 turn。
- 暂存消息执行前重新确认 owner 和 active turn，避免把消息发送到已释放的 Desktop runtime。

### 7.4 审批和用户问答

- 从 conversation state 的未完成 requests 投影为现有 `agent.ApprovalRequest` 或用户问答。
- 回复按 request ID 和 thread ID 路由回 Desktop IPC。
- Desktop 本地和远端可能同时展示请求；第一个有效回复生效，resolved 广播清理其他入口。
- 非授权用户不能查看请求详情或提交决定。

## 8. 断线与恢复

### 8.1 IPC 短暂断开

- owner 进入 `desktop_disconnected`。
- 保留最近状态并继续读取 rollout 进度和终态。
- 禁止 `turn/start`、`steer`、`interrupt` 和审批回复，明确提示实时连接已断开。
- 使用退避重连；重连后先请求 snapshot，再恢复控制能力。

### 8.2 Desktop 退出

- rollout active 时继续只读等待终态，不抢占 thread。
- 只有以下任一条件成立，才视为 Desktop 已释放所有权：IPC 连接正常且 owner 明确拒绝该 thread；收到 owner release/移除广播；或 Desktop IPC socket 与对应进程均已消失。
- 暂时没有广播、discovery 超时或网络抖动都不是释放证据。
- rollout 已终态且 Desktop 已释放所有权后，可恢复到 WeClaw app-server。
- 如果目标 thread 曾被当前 WeClaw app-server 加载且之后由 Desktop 修改，必须在无 WeClaw active turn 时刷新 ACP runtime，再从完整 rollout 恢复；不得对旧内存 thread 再次 `resume`。

### 8.3 不兼容版本

- 握手成功但必要方法版本不兼容时，展示“当前 Codex App 版本暂不支持实时接管”。
- rollout 仍提供任务、进度和最终结果，只读能力不受影响。
- 不把不兼容伪装成“未运行任务”，也不自动分叉 thread。

## 9. 安全边界

- 只连接当前 OS 用户拥有的本地 IPC socket，不接受聊天消息提供 socket 路径。
- 继续复用平台 allowed users、管理员和 route ownership 校验。
- IPC payload、thread ID、request ID 和 patch path 都在 Agent 边界校验。
- 日志只记录 method、thread/turn 标识摘要、状态和错误，不记录完整 prompt、输出或审批正文。
- 不开放公网监听，不新增 relay 凭证，不把 Codex token 交给飞书或微信。
- IPC 发送失败不本地重试同一 turn；无法确认 Desktop 是否已接收时返回不确定错误，避免重复执行。

## 10. 代码边界

预计涉及以下职责，不在 Spec 阶段创建实现文件：

- `agent/`：Desktop IPC 连接、framing、请求关联、状态投影、live owner 路由和能力接口。
- `messaging/codex_session_switch.go`：切换前先解析 live owner，避免提前 `UseCodexThread`。
- `messaging/codex_external_task.go`：把 Desktop live watcher 与 rollout watcher统一到现有 active task 生命周期。
- `messaging/task_commands.go`：按 owner 路由 `/guide` 和 `/stop`。
- `messaging/agent_conversation.go`：发送消息前校验 owner，避免恢复到错误 runtime。
- 测试文件按职责拆分，禁止继续扩充已超过 300 行的历史测试文件。

## 11. 测试与验收

### 11.1 Agent 单元测试

- frame 拆包、粘包、半包、超限和非法 JSON。
- initialize 成功、超时、socket 不存在和版本不兼容。
- snapshot、patch、缺失基线恢复、revision 去重和缓存淘汰。
- Desktop `start / steer / interrupt` 请求映射及响应关联。
- 审批、用户问答、已 resolved 请求和重复回复。
- 断线重连、持有请求时断线和不确定发送结果不重试。

### 11.2 Messaging 回归测试

- 切换 Desktop active/idle thread 均不调用 WeClaw `UseCodexThread`。
- 切换后下一条飞书/微信消息通过 Desktop IPC 续写同一 thread。
- 普通消息暂存后自动执行，`/guide` 和 `/stop` 路由 Desktop active turn。
- Desktop 最终结果只发送一次，卡片和文本流不重复。
- IPC 断开时拒绝新控制，不静默恢复或创建 thread。
- Desktop 释放且 rollout 终态后，可从完整上下文恢复到 WeClaw runtime。
- 未授权用户不能接管、查看审批或控制 thread。

### 11.3 验证命令

```bash
go test ./agent ./messaging -count=1 -timeout 60s
go test -race ./agent ./messaging -count=1 -timeout 60s
go test ./... -count=1 -timeout 120s
go test -race ./... -count=1 -timeout 180s
go vet ./...
staticcheck ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

### 11.4 手工验收

1. 在 Codex App 打开 thread 并开始长任务。
2. 从飞书切换该 thread，确认立即显示同一任务和实时状态。
3. 从飞书发送 `/guide`，确认 Codex App 当前 turn 收到引导。
4. 从飞书发送 `/stop`，确认 Codex App 当前 turn 中断。
5. 重新发消息，确认 Codex App 原 thread 出现新 turn，并能回答此前本地上下文问题。
6. 触发权限审批，从飞书完成审批并确认本地任务继续。
7. 模拟 IPC 断线，确认 WeClaw 不分叉 thread；恢复连接后继续同一会话。

## 12. 并行策略

设计和状态机整合必须串行，因为 IPC owner、ACP owner、active task 和 route binding 共享同一会话状态。实现阶段可以并行执行互不写冲突的测试夹具与协议解析子任务，但以下文件必须由单一执行者串行修改：

- Desktop IPC owner 状态模型。
- `messaging/codex_session_switch.go`。
- `messaging/codex_external_task.go`。
- `messaging/task_state.go` 与控制命令路由。

## 13. 风险与取舍

- Codex Desktop IPC 是内部版本化接口，稳定性低于公开 app-server。采用能力探测、集中版本表、契约测试和显式只读降级控制风险。
- 完整 conversation snapshot/patch 投影比 rollout tail 复杂，但这是实时审批、引导和上下文一致性的必要成本。
- 通用 PTY wrapper 更稳定但会牺牲 Codex App 结构化体验；本项目优先保持 App 与聊天端的同 thread 体验。
- 首期只覆盖当前发布目标 macOS；Windows pipe 支持在后续独立任务中实现。

## 14. 参考实现与来源

- OpenAI Codex app-server：<https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md>
- OpenAI Codex Remote Control：<https://openai.com/index/work-with-codex-from-anywhere/>
- Remodex：<https://github.com/Emanuele-web04/remodex>
- Remodex Desktop IPC follower：<https://github.com/Emanuele-web04/remodex/blob/main/phodex-bridge/src/desktop-ipc-action-follower.js>
- Happy：<https://github.com/slopus/happy>
- MobileCLI：<https://github.com/MobileCLI/mobilecli>
- Claude Channels：<https://code.claude.com/docs/en/channels-reference>

## 15. HARD-GATE

- 架构方向已由用户于 2026-07-11 确认。
- 本 Spec 提交后仍需用户确认书面范围，再进入函数级实施计划和编码。
