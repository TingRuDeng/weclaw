# Codex 原生客户端接入设计

日期：2026-10-15。状态：用户已授权按原生协作方向重新设计并实施；下文定义本轮范围。

## 目标

WeClaw 是 Codex 的客户端，不再默认管理所有本地 Codex 前端的启动与生命周期。用户可以随时在 App、CLI、飞书和微信之间输入，不增加 owner、抢占或释放前置步骤。

## 已核实的边界

- 官方 App Server 提供 `thread/resume`、`thread/read`、`turn/start`、带 `expectedTurnId` 的 `turn/steer` 和按连接的 `thread/unsubscribe`。CLI 提供 `resume`、`--remote` 和共享后台服务路径。
- 当前安装的 App 在无配置覆盖等前提下才允许通过 `CODEX_APP_SERVER_USE_LOCAL_DAEMON=1` 使用官方 daemon，否则使用自己的 stdio 执行端。不能把“官方 App 与 CLI 默认总在同一个 Host”作为设计事实。
- 当前 `shared` 桥接负责 App 两阶段启动、签名 Node、MCP pipe 与 Host 替换；默认路径退出这些职责。
- 官方资料：https://learn.chatgpt.com/docs/app-server 与 https://learn.chatgpt.com/docs/codex/cli 。本机包代码检查只证明当前安装版本的分支，不构成上游稳定接口承诺。

## 方案取舍

1. **采用：原生客户端模式。** `auto` 的标准原生配置解析为 `native`，使用官方 daemon 的既有 socket 和生命周期命令，按需连接或通过官方命令启动，不修改 App 环境。复用现有协议输入与消息可靠性逻辑。
2. **保留为显式兼容：`shared`、`daemon`、`managed`。** 原有启动桥接和协调语义留在明确选择的模式内，不再由普通自动配置启用。
3. **不采用：单前端独占或另起一套历史复制。** 前者增加用户限制，后者破坏原 thread 身份及上游历史契约。

## 配置和执行端

- 增加 `codex_host_mode: native`。`auto` 遇到自定义 socket 或 `run_as_user` 时保持 managed 兼容；其余原生配置选择 native。官方 standalone 不可用时明确报错，不默默启动第二种执行端。
- 自动探测不再写入 `codex_multi_frontend=true` 或 `codex_app_reuse_daemon=true`。旧字段保留 JSON round-trip；标准 auto/native 中不再赋予它们 App 接管或启动阻塞语义。显式旧模式继续解释旧字段。
- native 不调用 launchctl、Desktop 私有 IPC、App bridge、全机额外 Host 扫描或自动 CLI 升级。仅验证目标 socket、官方 daemon 身份与可执行文件，连接失败保留真实错误。
- 不因其他 App/CLI 进程存在就阻止 native 使用。目标 thread 在另一独立执行端中的占用或不可读由上游协议报告，不能忽略错误另起 writer。
- `weclaw codex cli` 在 native 中独立准备官方连接，不要求 WeClaw 正在运行或持有整个 CLI 会话的 WeClaw 生命周期租约。

## 会话与输入

- 保留 route→thread binding，原会话通过官方 resume 继续，不复制 rollout、不重写 SQLite provider。
- 同一执行端上的 active→steer、idle→start、状态竞态重读、未知交付不自动重试与审批 broker 保持不变。官方 0.162.1 隔离验证表明，并发 start 可以均成功且返回同一 turn；成功输入不得再 steer 重发。旧版明确拒绝时才按权威状态重读。
- App 使用独立 stdio 执行端时，不承诺搬移其内存中的活动工具或 App 专属 MCP。恢复原会话若被上游拒绝，返回该会话的明确错误；不能伪造运行中接手成功。
- 无会话独占规则；本地序列化仅保护一次协议提交和必要维护，不表示用户必须关闭另一个入口。

## 生命周期与迁移

- native 不预热或同步阻塞平台启动。Codex 不可用只影响需要 Codex 的请求。
- 普通 stop/restart/update 的排空和本地任务保护保留；native 不要求外部 App/CLI 空闲，不停止官方 daemon，不创建为了轮换外部 Host 的 journal。
- 旧版本已记录的 Host 停止或 pending journal 必须继续完成真实恢复，不能因切换 native 忽略。显式 `--force` 保留其现有全局终止授权和身份复核语义。
- native 会话准备使用官方协议，不自动迁移 SQLite provider。显式账户维护仍沿用现有账号事务及空闲门禁，不在普通消息中偷偷执行。
- 提供显式恢复旧 shared App 启动环境的入口，只恢复保护备份中仍匹配 WeClaw 应用值的变量，保留后来用户更改的变量。不在本轮自动退出 App、安装或重启本机服务。

## 验收与回滚

- 回归覆盖旧配置标志不再启用桥接、native 不写 App 环境、不全机扫描、CLI 不受服务租约约束、服务启停不接管 daemon、provider 准备不访问 SQLite，以及已有 start/steer/审批与未知交付边界。
- 运行受影响包测试、全仓测试与 race、vet、文档及 diff 校验；真实协议门禁仅在完整隔离 runtime 内运行，不能操作日常会话或把测试模型等同于真实 App 验收。
- 明确区分配置/单元测试、真实 daemon 协议、真实 App/飞书验收及本机部署证据。
- 回滚使用显式旧 Host 模式或代码回滚；不改现有 binding/outbox 状态格式，不删除 Codex 历史、数据库或锁。
