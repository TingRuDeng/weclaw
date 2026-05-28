# Lessons

## 2026-04-28 微信绝对路径误判

- 微信消息中以 `/` 开头的内容不一定是命令，也可能是 macOS / Linux 绝对路径。
- Agent 命令解析只能接受独立 token，例如 `/codex 任务` 或 `@codex 任务`；不能按路径分隔符 `/` 连续拆 token。
- 新增命令解析规则时，必须补“绝对路径作为普通文本转发给默认 Agent”的回归测试。

## 2026-04-28 微信换行展示

- 微信气泡会弱化或折叠单个逻辑换行，命令回复和 Agent 最终回复都不能只依赖单个 `\n` 保证视觉换行。
- 微信出站文本应在发送层统一做展示格式化，把逻辑换行转换为空行分隔；不要只修某个命令渲染函数。
- 修改发送层换行策略时，必须同时覆盖普通回复和长回复分段测试。

## 2026-04-28 微信进度反馈

- 微信客户端已经有“对方正在输入”可视化提示时，默认不要再额外发送“收到”“处理中”“进展”等中间文字气泡。
- 进度文字适合作为显式 `summary` / `stream` / `debug` 模式，不适合作为默认体验。
- 改默认进度体验时，必须覆盖“默认不发文字、仍发 typing 状态”的回归测试。

## 2026-04-28 Codex 会话切换

- `/codex switch <threadId>` 不能只切 thread；如果该 thread 已记录在某个 workspace 下，也必须同步切换 Codex Agent 的 workspace。
- Codex 会话状态涉及 thread 和 cwd 两个状态源，修改其中一个时必须检查另一个是否需要同步。
- 新增 Codex 会话命令时，要覆盖“跨 workspace 历史 thread 切换”的回归测试。

## 2026-04-28 Codex 重启恢复

- 只持久化 workspace -> thread 列表不够，还必须持久化用户当前 active workspace；否则重启后会回到配置里的默认 cwd。
- Codex 普通聊天、`/codex switch`、`/codex new` 和 `/cwd` 都可能改变 active workspace，必须同步写入 session store。
- 重启恢复类问题必须用“新 Handler 加载同一个 session 文件”的测试覆盖。

## 2026-04-28 Codex 同会话并发

- 同一微信用户、同一 Codex Agent、同一 workspace 不能并发进入 Codex turn；否则本地 Codex 队列和 WeClaw 进度会话可能出现结果归属错乱。
- 串行化边界应包住进度会话、Agent 调用和最终回复，确保第二条任务不会在第一条任务完成前启动自己的 typing/progress 生命周期。
- 并发类 bug 必须补“第二条消息等待第一条完成”的回归测试，不能只验证单次调用成功。

## 2026-04-28 Codex thread 归属

- Codex thread 必须稳定归属于创建它的 workspace；不能因为当前 Handler cwd 不同就在 `recordCodexThread` 中把旧 thread 写到新 workspace。
- session store 写入 thread 时必须保证同一个 thread 只出现在一个 workspace 下，否则 `/codex switch` 会按错误 workspace 恢复。
- 已记录 thread 恢复失败时必须显式报错，不允许继续走 `thread/start` 静默新建会话。

## 2026-04-28 Codex 引导对话

- Codex 执行中收到第二条普通消息时不能直接排队，也不能直接发送；必须先暂存并让用户明确选择 `/guide` 或 `/cancel`。
- `/guide` 表示用户放弃第一条微信侧最终回复，只关心引导后的结果；实现时要取消第一条监听并抑制第一条最终回复。
- `/cancel` 在该语境下只撤回暂存消息，不取消正在执行的 Codex 任务。
- 微信消息入口可以按用户串行分发，但 Codex 长任务不能同步阻塞该入口；必须先登记 active task，再后台执行，保证运行中还能处理 `/guide` 和 `/cancel`。
- 用户不处理运行中的暂存消息时，任务结束后不能静默丢弃；应转为待执行状态，并要求用户用 `/run` 明确确认执行。
- 自动打开 Companion 可见终端时必须可关闭，并且命令参数要做 shell/AppleScript 双层转义，避免工作目录包含空格或单引号时启动失败。

## 2026-04-28 Codex 会话命令

- 会话列表必须带稳定编号，`/codex switch <编号>` 要使用与 `/codex ls` 相同的排序，避免用户复制长 threadId。
- 修改命令命名时必须同步更新 `isCodexSessionCommand`、帮助文本、命令处理分支和回归测试。
- 旧命令如果不再作为用户入口，就不要继续出现在 `/help`，防止微信侧形成两套说法。

## 2026-04-28 Codex 额度错误

- `usageLimitExceeded` 只是额度耗尽，不代表登录态或工作区失效，不能自动 Stop Codex 进程或清理 thread。
- 用户需要手动切换 Codex 账号时，WeClaw 必须保留当前进程和 thread 映射，避免切账号后 `/codex switch` 遇到已关闭 stdin。
- 只有 `deactivated_workspace` 这类真实工作区/登录态异常才允许触发 runtime invalidation。

## 2026-05-28 Codex Companion attach/detach

- Codex Companion 默认应弱绑定本地终端，保持微信 remote 可独立使用；本地可见终端只能通过 `/cx attach` 或显式 `auto_launch: true` 打开。
- `/cx detach` 只能断开当前可见 Companion 连接，不能清理后台 endpoint 或停止 WeClaw，否则微信 remote 会被误伤。
- 调整 Codex 会话命令时必须同步覆盖命令识别、Handler 分支、帮助文本和不支持该能力的 Agent 提示。
- `/cx attach` 打开的可见终端不能只停在 `weclaw companion` 等待态；Companion 握手成功后必须立即启动对应 CLI 可见运行时，避免用户误判为卡死。
- Codex App 当前公开入口是 `codex app <workspace>`，未确认可 deep link 到指定 thread；App 接手命令应先打开当前 workspace 并回显 threadID，而不是假装能精确选中会话。
- Codex 的最终产品语义是 remote-first：微信普通任务应走 app-server，不依赖本地 Companion；`/cx attach` 应使用当前 thread 执行本地 `codex resume`，而不是重新打开一个独立 bridge 会话。
- App 和 CLI 是两种不同接手入口：用户可见命令应使用 `/cx app` 和 `/cx cli` 明确区分；`/cx attach` 只作为兼容入口，避免形成两套主要说法。
- Codex App 不可用时不要静默降级到 CLI；必须暴露 App 打开失败原因，并提示用户显式发送 `/cx cli` 接手当前 thread。
- 本地 Terminal / Codex App 是临时接手入口，不是微信 remote 的权威状态源；状态查询只记录 WeClaw 最近成功打开动作，不实时同步手动关闭，也不自动关闭本地窗口。
- WeClaw 主帮助只展示当前推荐路径；`/sw`、Companion、实时进度和主动推送 API 这类高级能力可保留，但不要继续挤进默认 `/help`，避免产品语义变散。
