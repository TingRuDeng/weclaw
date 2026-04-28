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

## 2026-04-28 Codex 会话命令

- 会话列表必须带稳定编号，`/codex switch <编号>` 要使用与 `/codex ls` 相同的排序，避免用户复制长 threadId。
- 修改命令命名时必须同步更新 `isCodexSessionCommand`、帮助文本、命令处理分支和回归测试。
- 旧命令如果不再作为用户入口，就不要继续出现在 `/help`，防止微信侧形成两套说法。

## 2026-04-28 Codex 额度错误

- `usageLimitExceeded` 只是额度耗尽，不代表登录态或工作区失效，不能自动 Stop Codex 进程或清理 thread。
- 用户需要手动切换 Codex 账号时，WeClaw 必须保留当前进程和 thread 映射，避免切账号后 `/codex switch` 遇到已关闭 stdin。
- 只有 `deactivated_workspace` 这类真实工作区/登录态异常才允许触发 runtime invalidation。
