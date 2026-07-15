# Claude 远程会话唯一所有权与本地交接设计

## 1. 背景

WeClaw 当前通过 `claude-agent-acp` 列出、恢复、新建和继续 Claude Code session，
并通过 `/cc cli` 打开原生 Claude CLI 接手当前 session。现有实现已经具备 route 级绑定、
会话恢复、失败回滚、后台任务和绑定执行锁，但没有 session 级控制意图：

- 两个远程窗口可以分别把同一个 session 绑定到自己的 route。
- ACP 进程只能在两个 prompt 真正重叠时拒绝后来的调用，不能阻止不同窗口交替写入。
- `/cc cli` 只在启动临界区串行化本地交接；打开 Terminal 后仍保留远程绑定和写入资格。
- 独立 Claude CLI 的活动状态、事件和终态不属于当前 ACP 事实源，WeClaw 无法可靠观察或停止。

Codex 已完成“选择即接管”治理。本设计只复用其中适用于 Claude 的所有权不变量、原子状态和
并发门禁，不复制 Codex Desktop IPC、rollout watcher、外部任务观察或 `/guide` 能力。

## 2. 目标

1. 同一个 Claude session 同时最多由一个远程窗口控制。
2. 同一个远程窗口同时最多控制一个 Claude session。
3. `/cc switch`、飞书会话卡片、`/cc new` 和默认 Claude 的全局 `/new` 成功后即取得远程控制权。
4. 从 A 切换到 B 时，A 的远程控制权与 B 的远程接管在同一事务中完成。
5. `/cc cli` 在打开原生 Claude CLI 前显式释放远程控制权；释放后普通消息 fail-closed。
6. 提供 `/cc owner`、`/cc owner remote` 和 `/cc owner local`，让用户查看、重新取得或显式释放控制权。
7. 状态写入、ACP 绑定、任务准入和本地交接失败时不留下可继续双写的半提交状态。
8. 迁移旧状态时尽量保留单一绑定用户的使用连续性，对历史多窗口冲突 fail-closed。

## 3. 非目标

- 不实时接管、观察、引导或停止独立 Claude CLI 中正在执行的任务。
- 不读取 `~/.claude` transcript 判断活动状态、控制权或任务终态。
- 不扫描进程、锁文件或文件更新时间来猜测本地 Claude CLI 是否仍在使用 session。
- 不重新引入 Claude CLI 远程聊天后端；远程执行仍只使用 `claude-agent-acp`。
- 不为 Claude 实现 `/guide`。
- 不修改 Codex 的所有权、Desktop runtime、Companion 或 rollout 观察链路。
- 不承诺从操作系统层面阻止用户在本地另开 Claude CLI；本地与远程切换属于显式协作协议。

## 4. 方案对比

### 4.1 方案 A：只增加远程窗口互斥

为 session 增加唯一 remote route，但保持 `/cc cli` 现有行为。

- 优点：改动最小，能阻止微信与飞书同时控制同一 session。
- 缺点：Terminal 打开后仍可能与远程 ACP 双写，无法闭合“本地交接”语义。
- 结论：不采用。它只解决一半根因。

### 4.2 方案 B：显式控制意图、session 级事务与协作式本地交接

在 Claude 状态文件中同时持久化 route 绑定和 session 控制意图；选择即远程接管，`/cc cli`
先释放为 `local` 再打开 Terminal，远程重新选择或 `/cc owner remote` 才恢复写入。

- 优点：能完整约束 WeClaw 内的多窗口写入，并把本地交接变成清晰、可恢复、可测试的状态变化。
- 缺点：WeClaw 无法证明用户已经关闭独立 Claude CLI；重新接管依赖用户明确遵守交接约定。
- 结论：采用。它在当前 ACP 能力边界内提供最强的一致性保证。

### 4.3 方案 C：通过 transcript、进程或文件锁推断本地活动状态

扫描 `~/.claude`、进程参数或文件变化，尝试自动判断本地 writer 和活动任务。

- 优点：表面上更接近 Codex Desktop 实时接管。
- 缺点：这些信号不是 Claude ACP 的权威状态，存在漏报、误报、权限差异和版本漂移；也违反当前
  ACP-only 与禁止 transcript 作为事实源的架构边界。
- 结论：不采用。不能用启发式信号伪装所有权确认。

## 5. 产品规则

### 5.1 选择即远程接管

以下入口只要成功选中或创建真实 session，就必须复用同一套“选择并接管”事务：

- `/cc switch <编号|sessionId>`
- 飞书 Claude 会话卡片按钮
- `/cc new`
- 当前默认 Agent 为 Claude 时的全局 `/new`
- `/cc owner remote`

`/cc ls`、`/cc pwd`、`/cc status`、`/cc model status|ls` 和 `/cc owner` 是只读入口。

### 5.2 单窗口单所有权

当前窗口拥有 A，再选择 B 时：

- A 的控制意图变为 `local`。
- B 的控制意图变为 `remote`，记录当前 binding key 和 conversation ID。
- 当前 route 绑定切换为 B。
- 任一步失败都恢复 A、B 和 route 绑定的原状态。

历史状态若让同一 route 关联多个 remote session，下一次所有权变化必须归一化为仅拥有目标 session。
任一待释放 session 仍有当前进程内活动远程任务时，切换拒绝执行。

### 5.3 跨窗口冲突

目标 session 已由其他远程窗口控制时直接拒绝。错误只说明“其他远程窗口正在控制”，不得展示
用户 ID、平台、route、binding key 或 conversation ID。

两个窗口并发选择同一个未认领或本地 session 时，只允许一个事务提交成功。另一个事务收到所有权
revision 冲突或“其他远程窗口”错误，且不能覆盖赢家。

### 5.4 本地释放与重新接管

- `/cc owner local`：保留当前 session 选择，释放远程写入权，不打开 Terminal。
- `/cc cli`：只有当前远程 owner 且没有活动任务或暂存消息时可执行；先释放为 `local`，再打开
  `claude --resume <sessionId>`。
- `/cc owner remote`：重新恢复当前选择并取得远程控制权。
- 重新发送 `/cc switch <当前 session>` 也属于显式重新接管。
- `local` 控制意图不会因空闲、时间经过或 WeClaw 重启自动变回 `remote`。

`local` 表示 WeClaw 不再允许远程写入，不证明本地 CLI 当前一定加载该 session。用户执行重新接管
前必须先结束本地 Claude CLI 对该 session 的写入；WeClaw 会在帮助和状态文本中明确这一边界。

### 5.5 工作空间导航

保持当前 `/cc cd <工作空间>` 行为：进入工作空间会清除当前 session 选择，要求用户继续选择或新建。
治理后，该操作必须在同一事务中把旧 session 从 `remote` 释放为 `local`，不能留下“窗口已无选择但仍
持有旧 session”的隐藏所有权。

### 5.6 普通消息与任务控制

普通消息只有同时满足以下条件才允许登记后台任务：

1. route 存在 ready 或可恢复的 session 绑定。
2. 该 session 的控制意图为当前 binding key 的 `remote`。
3. 当前控制 revision 与任务准入时读取的 revision 一致。

`local`、`unclaimed`、其他远程窗口控制、revision 变化或控制状态未知时均拒绝执行。当前 WeClaw
任务运行期间继续支持 `/stop`、`/cancel` 和单条暂存消息；`/guide` 对 Claude 保持不支持。

## 6. 状态模型

### 6.1 持久化结构

`claude-sessions.json` 从 v2 升级为 v3，在现有 `bindings` 旁增加 session 级 `controls`：

```go
type claudeControlOwner string

const (
    claudeOwnerUnclaimed claudeControlOwner = "unclaimed"
    claudeOwnerLocal     claudeControlOwner = "local"
    claudeOwnerRemote    claudeControlOwner = "remote"
)

type claudeControlIntent struct {
    Owner          claudeControlOwner `json:"owner"`
    BindingKey     string             `json:"binding_key,omitempty"`
    ConversationID string             `json:"conversation_id,omitempty"`
    Revision       uint64             `json:"revision"`
    UpdatedAt      string             `json:"updated_at"`
}

type claudeSessionState struct {
    Version  int                                `json:"version"`
    Bindings map[string]claudeSessionBinding    `json:"bindings"`
    Controls map[string]claudeControlIntent     `json:"controls"`
    Updated  string                             `json:"updated"`
}
```

`controls` 以 session ID 为键。`remote` 必须同时包含非空 binding key 和 conversation ID；`local`
与 `unclaimed` 不保存远程身份。所有权 revision 只能通过 compare-and-swap 递增。

### 6.2 核心不变量

1. 一个 session 同时最多有一个 remote binding key。
2. 一个 binding key 同时最多拥有一个 remote session。
3. remote 控制项必须与该 route 当前选中的 session 一致。
4. A 到 B 成功后，A 不再由当前 route 远程控制。
5. 所有 touched binding/control 必须一次持久化，不能让读者看到半提交状态。
6. 文件写入失败时不发布候选内存状态。
7. 无法确认补偿或 revision 已变化时 fail-closed，不猜测当前 owner。

### 6.3 v2 迁移

- session 只被一个有效 binding 引用时，迁移为该 binding 的 `remote`，保留既有远程使用连续性。
- session 被多个 binding 引用时，迁移为 `unclaimed`；各 route 保留最近选择，但普通消息必须先显式
  重新选择，首个成功事务取得所有权。
- 没有 session 的 workspace binding 不生成控制项。
- 非法、空白或自相矛盾的控制数据按 `unclaimed` 处理并记录诊断，不自动选择赢家。
- `local` 状态跨重启保留；重启不会自动恢复远程控制。

## 7. 锁与事务边界

### 7.1 锁顺序

新增 session 级锁，锁顺序固定为：

1. route 的 `claudeBindingExecutionKey(bindingKey)`。
2. 对旧 session、目标 session 去重排序后依次取得 session 锁。

事务内部禁止反向获取 binding 锁。所有 session 锁保持到提交或补偿完成，避免两个 route 执行
A→B 与 B→A 时产生 ABBA 死锁。

### 7.2 Store 原子提交

Store 提供 copy-on-write 事务接口，输入包含：

- 当前 binding 的 expected 快照。
- 目标 workspace/session。
- 当前 route 需要释放的全部旧 session 及 expected control revision。
- 目标 session 的 expected control revision。
- 新的 owner、binding key 和 conversation ID。

提交时持有 `saveMu`，在 `mu` 内重新校验所有 expected 快照，构造完整候选状态，只持久化一次。
持久化成功后才替换内存 maps；冲突或写盘失败保持原状态。

### 7.3 选择接管 saga

正常 A→B 流程：

1. 在 binding 锁内读取当前绑定、A/B controls 和当前 route 的历史 remote sessions。
2. 取得所有 touched session 锁并重新校验快照。
3. 校验 A 没有活动任务，B 没有被其他远程窗口控制。
4. 通过 ACP `session/list` 与 `session/resume` 验证并恢复 B。
5. 一次性提交 route 选择、B remote intent 和全部旧 session local intents。
6. 保存当前窗口 Agent 为 Claude。
7. 清理当前 route 已释放 session 的 ACP conversation runtime 映射。
8. 只有全部完成后回复“已切换并接管”。

ACP 恢复、状态文件或 Agent 选择保存失败时，按逆序恢复旧 runtime、绑定和 controls。补偿失败时保留
不允许远程消息继续执行的状态，并返回明确错误。

### 7.4 新建接管 saga

`/cc new` 与默认 Claude 的全局 `/new` 先通过 ACP `session/new` 创建真实 session，再复用选择接管
事务提交新 session。提交失败产生的孤立 session 不删除，保留在 ACP 目录；它不得获得 remote owner，
旧 session 与旧 route 绑定必须恢复。

### 7.5 本地交接 saga

`/cc cli` 流程：

1. 在 binding 与当前 session 锁内确认当前 route 是 remote owner。
2. 确认没有活动任务或暂存消息，并校验 workspace 与 session ID。
3. 原子提交 owner 为 `local`，保留 route 的最近 session 选择。
4. 清理当前 conversation 的 ACP runtime 绑定。
5. 打开原生 Claude CLI。

若 CLI opener 明确失败，先恢复 ACP runtime，再通过 expected revision 把 owner 恢复为当前 route 的
`remote`。任一补偿失败都保持 `local` 或 `unclaimed` 的 fail-closed 状态，不在无法确认本地进程状态时
自动恢复远程写入。

CLI opener 成功只表示 Terminal 启动请求已接受，不证明 Claude CLI 已完成 session 恢复；状态仍保持
`local`，由用户决定何时重新接管。

## 8. Agent 与消息层职责

### 8.1 Messaging

- `claudeSessionStore` 是 route 选择与 session 控制意图的持久化事实源。
- 统一选择接管编排负责锁、ACP 调用、Store 提交、Agent 选择和补偿。
- 普通消息在任务登记前检查 remote owner；任务执行前再次校验 revision。
- `/cc owner` 和 `/cc status` 展示当前选择、控制方与恢复状态。
- 列表可以标记“当前窗口”“本地”或“其他远程窗口”，但不得展示其他窗口身份。

### 8.2 ACPAgent

- `ACPAgent.sessions` 继续只保存可重建的 conversation→session runtime 映射，不成为所有权事实源。
- `UseClaudeSession` 继续负责 catalog 校验、`session/resume` 和 conversation 级 revision。
- `ClearClaudeSession` 用于释放 route runtime 映射；它不能独立改变持久化 owner。
- 已有按 session ID 的 prompt channel 注册继续作为最后一道进程内并发保护，但不代替所有权门禁。

### 8.3 本地 Claude CLI

- `AgentInfo.LocalCommand` 仍是唯一允许的本地交接命令来源。
- 本地 CLI 不接入 WeClaw active task、进度、审批或停止通道。
- WeClaw 不通过杀进程、文件扫描或 transcript 推断补齐缺失能力。

## 9. 错误与安全边界

- 其他远程窗口控制：拒绝，不泄露对方身份。
- 当前任务运行或存在暂存消息：拒绝切换、释放和本地交接。
- ACP list/resume/new 失败：保留原选择和 owner。
- Store revision 冲突：不覆盖并发赢家，重新查询后再操作。
- Store 持久化失败：候选状态不发布，执行 runtime 补偿。
- CLI opener 失败：尝试恢复 remote；补偿不完整时保持 fail-closed。
- 重启后 controls 与 bindings 不一致：拒绝普通消息，要求显式重新选择。
- 用户未关闭本地 Claude CLI 就主动重新接管：这是无法由当前 ACP 权威验证的剩余风险；状态与帮助
  必须明确提示用户先结束本地写入。
- session ID、workspace 和 local command 继续沿用现有输入校验与 shell/AppleScript 转义。

## 10. 用户反馈

成功选择或新建至少显示：

- 已切换并接管或已创建并接管。
- workspace 与 session 摘要。
- 控制方：当前远程窗口。
- 恢复状态与当前模型配置。

`/cc owner` 至少显示：session、控制方、恢复状态、活动任务状态，以及 `local` 状态下“结束本地 CLI
后再重新接管”的提示。

`/cc cli` 成功回复改为“已释放远程控制并打开 Claude CLI”。释放后普通消息提示使用 `/cc switch`
重新选择，或在确认本地 CLI 已结束后使用 `/cc owner remote`。

## 11. 测试设计

### 11.1 Store 与迁移

- v2 单 binding 迁移为 remote。
- v2 多 binding 指向同一 session 时迁移为 unclaimed。
- A remote、B local 的原子交换。
- 全部 touched controls 的 revision 校验与递增。
- 任一 CAS 冲突时零状态变化。
- 持久化失败时内存与磁盘完整回滚。
- 重启后 remote/local 选择与控制状态一致。

### 11.2 消息层状态机

- A→B 成功后 A local、B remote、route 选择 B。
- 其他 route 已拥有 B 时拒绝且原状态不变。
- 两个 route 并发争用 B 只有一个成功。
- 当前任务或暂存消息阻止切换、释放和 `/cc cli`。
- 同 route 重复选择当前 session 幂等。
- 普通消息在 local、unclaimed、其他 remote 或 revision 变化时不调用 Agent。
- ACP、Store、Agent 选择和补偿各失败点均保持 fail-closed。

### 11.3 入口矩阵

- 微信与飞书文本入口语义一致。
- `/cc switch` 与飞书卡片复用同一接管事务。
- `/cc new` 与默认 Claude 的全局 `/new` 创建后取得 remote owner。
- `/cc cd` 清除选择时同步释放旧 owner。
- `/cc owner local` 释放后普通消息拒绝。
- `/cc owner remote` 与重新选择复用同一接管事务。
- `/cc cli` 成功释放并打开，opener 失败按规则补偿。
- `/cc ls`、`/cc pwd`、`/cc status` 和模型只读查询不改变 owner。

### 11.4 并发与回归

- binding 锁与有序 session 锁不存在 ABBA 死锁，失败后锁可复用。
- 任务准入与 owner release 串行，不能在检查后越过释放开始 prompt。
- 现有 ACP active prompt guard、进度、审批、停止、排队和配置切换回归通过。
- Codex 所有权、外部任务与 Desktop runtime 测试不受影响。

实现阶段至少执行：

```bash
go test ./agent ./messaging -count=1 -timeout 120s
go test -race ./agent ./messaging -count=1 -timeout 120s
go test ./... -count=1 -timeout 120s
go vet ./...
staticcheck ./...
go build ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## 12. 文档同步

实现完成后同步：

- `README_CN.md`、`README.md`：Claude 选择即接管、本地释放与剩余边界。
- `messaging/claude_render.go`：`/cc help` 与 `/cc status`。
- `docs/AI_CONTEXT.md`：状态事实源、统一事务、锁顺序和 ACP/local 边界。
- `tasks/lessons.md`：Claude session 唯一远程所有权与协作式本地交接规则。
- `tasks/todo.md`：只记录本轮实施状态，完成后收口。

## 13. 实施边界与验收标准

所有权事务同时涉及 route binding、多个 session、ACP runtime、活动任务和同一个状态文件。核心实现必须
串行完成；验证可以按 `agent`、`messaging` 和文档门禁独立运行，但最终统一复核。

验收标准：

1. 选择或新建 Claude session 后无需第二条命令即可由当前远程窗口控制。
2. 从 A 切换到 B 后，A 不再由当前窗口远程控制。
3. 其他远程窗口不能抢占已被控制的 session。
4. `/cc cli` 成功后远程普通消息被拒绝，直到显式重新接管。
5. `/cc owner local|remote` 与选择入口遵守同一所有权状态机。
6. 并发、持久化或补偿失败不会留下两个 WeClaw 远程 writer。
7. 重启后 remote/local 意图不被 ACP runtime 缓存覆盖。
8. 不新增 transcript 扫描、进程猜测或独立 CLI 活动任务伪观察。
9. 全量测试、race、vet、staticcheck、build、文档门禁和差异检查通过。
10. Review Gate 没有阻止交付的问题，剩余“本地 CLI 无权威活动探测”风险被明确记录。
