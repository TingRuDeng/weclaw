# 当前任务记录

## 2026-09-05 已确认需求与验收基线

**交付状态：Codex 共享适配、设置恢复与微信持久观察已接入产品源码，受影响四个模块的 Race 回归、静态检查、双平台构建及文档校验通过，隔离测试环境已收尾。用户最新明确不需要频繁实测：依据现有证据推进实现，只保留必要回归，不再要求用户逐项完成手动矩阵。R1–R6 的功能目标不变，未实测场景如实保留；Claude 暂停。用户已确认发布、安装及首次共享入口切换；正在执行 v0.1.305 发布流程。**

### 需求来源与边界

用户在“评估多端任务接管”中提出：飞书、微信与本地 Codex App、Codex CLI、Claude Code 都能随时接手任务，并已明确选择：

- Codex 与 Claude 各自打通，不跨 Agent 迁移任务。
- 执行中不中断，直接给正在执行的任务补充指令。
- 保留官方 CLI 界面，可以接受通过 WeClaw 命令启动。

用户在本次排障中补充：模型与推理强度切换必须可用；飞书接手不能让本地长期无法使用，`/cx release` 后本地必须确实能够继续操作。

**最新范围修订（2026-09-05）：用户明确说明“Claude Code可以不需要想codex这样，只要能让飞书控制Claude Code进行任务就行”。因此下表 R1–R6 仅约束 Codex；Claude 改按 C1–C3 验收。Claude 的官方终端双向接手、运行中跨端补充、微信端点对等与 Channels/background/attach 均退出本次必需范围。已有相关能力不因本次修订而删除。**

以下要求约束后续设计与实现。daemon、Host、binding、observer、writer lease 都是技术手段，不能反过来缩小需求；旧文档中的技术前提不等于用户接受的使用限制。技术路线不支持某项要求时，记录能力缺口，不能改写该要求或把降级行为标为完成。需求变更必须说明影响并取得用户明确同意。

| 编号 | 固定要求 | 验收证据 |
| --- | --- | --- |
| R1 | Codex 覆盖 App、官方 CLI、飞书、微信。 | 代码覆盖四端，依据已有协议/端点证据和必要回归交付；后续真实使用反馈不改写功能目标。 |
| R2 | 各端继续原会话及其历史。空闲时在原会话开启下一轮，运行中操作同一当前任务。 | 核对原 thread/session 身份与历史；新建、复制或重新投递任务不能充当接手。 |
| R3 | 运行中换端后，补充指令直接到达正在执行的任务，不中断。 | 原任务仍在执行时，另一端输入被接收并作用于该任务；忙碌拒绝、先停止、排队到终态后再执行均不算通过。 |
| R4 | 本地保留官方 Codex CLI 界面与功能；允许通过 WeClaw 命令启动。 | 在真实官方 CLI 中完成接手；简化终端、禁用入口或另开独立会话均不算通过。 |
| R5 | 飞书接手不能让本地长期不可用；`/cx release` 后本地确实能够继续同一会话。 | 真实 App/CLI 可继续输入，无需等后台卸载、重启服务或换新会话；解绑落盘、退订响应成功不能单独作为通过证据。 |
| R6 | 指定会话的模型、推理强度能够可靠切换，反馈与实际执行一致，并保持各端可用。 | 分别切换模型和强度，核对目标会话与实际执行配置，再从另一端继续；不只验证设置接口返回成功。 |

Claude 使用以下独立验收项，不套用 Codex 的会话占用与多端协议要求：

| 编号 | 当前要求 | 验收证据 |
| --- | --- | --- |
| C1 | 飞书能够选择或创建 Claude 会话，并下发任务。 | 现有飞书入口调用 Claude Code 的真实运行时完成任务；仅握手或模拟响应不算通过。 |
| C2 | 飞书能够获得任务进度、最终结果及真实失败原因。 | 核对执行结果与回传内容一致；现有权限交互和停止入口按受影响范围验证。 |
| C3 | 在飞书继续已选择的 Claude 会话。 | 后续消息沿用原 session 与上下文；不要求本地官方终端接手，也不要求运行中输入立即进入当前任务。 |

### 当前能力与缺口

下表区分源码事实、当前故障和未验收项，不用某一个方向成功推断反方向成功。

| 链路 | 当前结论 | 证据入口 |
| --- | --- | --- |
| 飞书 ↔ Codex App | 未通过。已观察到飞书解绑后 daemon 仍加载原 thread，而 App 无法继续；当前版本退订不是立即释放执行占用。 | `messaging/codex_thread_release.go`、`agent/codex_thread_handoff.go`；本次运行态排障。 |
| Codex 官方 CLI ↔ 其他端 | 已有连接共享服务的启动实现，完整双向流程未验收。 | `agent/codex_cli_launch.go`、`agent/codex_runtime_turn.go`。 |
| 微信 ↔ Codex 其他端 | 有即时绑定/输入路径，完整流程未验收；持久 follower 登记现已覆盖飞书与微信。 | `messaging/codex_follower_store.go`。 |
| 飞书 → Claude Code | 已有 ACP 任务、进度、结果和续聊路径；握手、新建会话与相关回归测试通过，真实请求连接失败，尚未通过端到端验收。 | `agent/acp_chat.go`、`messaging/claude_session_handler.go`、`messaging/agent_task.go`；见下方本轮 ACP 核验。 |
| Claude 跨端与官方 CLI 接手 | 不属于最新必需范围。现有另一窗口忙碌拒绝、同窗口后续输入排队及本地接手入口禁用，不作为 C1–C3 的失败项。 | `messaging/task_admission.go`、`messaging/claude_cli_handler.go`；不为追求 Codex 对等而改写这些路径。 |
| Codex 模型/推理强度切换 | 局部源码修复与回归测试已完成，未安装、未做真实双端验收；按需 resume 后退订仍需纳入 R5 的生命周期设计复核。 | `agent/codex_model.go`、`agent/acp_thread_test.go`。 |

### 2026-09-05 技术路线核查

**状态：已完成当前版本的源码与协议核查，用户已确认按 P0–P4 执行。共享适配的核心能力已有原型证据并已进入产品实现，R1–R6 不变；下面 Claude Channels 与官方界面的核查仅保留为范围修订前的证据，不再驱动本次实现。**

本次核对的版本：App 包 `26.901.31953`，App 内置 Codex `0.153.1`，standalone Codex `0.153.4`，Claude Code `2.1.224`。上游文档可能描述更新版本，涉及行为时以本机版本源码和实际验证为准。

| 项目 | 已确认事实 | 对方案的影响 |
| --- | --- | --- |
| Codex App 共享服务条件 | 本机 App 的 `app.asar` 中，`main-C5K7o1Hr.js` 的 `tk` 为本地启动返回 App 工具配置；成功和缺失工具的分支都返回非空覆盖项。`src-VqXTPopo.js` 的 transport `connect` 只有在 `getConfigOverrides` 为空时才走 local daemon。 | 仅设置 `CODEX_APP_SERVER_USE_LOCAL_DAEMON` 不能实现这一版 App 的服务共享。现有进程检查把带 Code Mode 标记的 app-server 排除，也不能据此证明其没有持有会话。 |
| Codex 释放与配置 | `0.153.4` 的 `thread/unsubscribe` 在最后观察者离开后仍保留已加载会话，满足无活动条件 30 分钟后才卸载；`thread/settings/update` 作用于已加载会话的后续轮次配置。 | R5 需要本地端能连接同一会话服务，不能依靠退订后等待。R6 要核对实际配置及生效时机，不能只检查空成功响应。参见 [对应版本协议](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/app-server/README.md)。 |
| Codex 连接适配的边界 | 官方 stdio 使用 JSONL，Unix socket 使用 WebSocket；`app-server proxy` 只转发原始字节。已加载会话的 resume 会忽略一般配置覆盖项，MCP 扩展能力也固定在会话加载时。 | 不能直接把原始 proxy 当作 App 的 stdio 服务，也不能丢弃 App 工具配置或假设第二个客户端 resume 就会应用它。候选适配必须先证明这些能力可以保留。参见 [resume 实现](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/app-server/src/request_processors/thread_processor.rs)。 |
| Code Mode gRPC | `--code-mode-host` 所指协议承载 JavaScript 执行单元与工具调用，不是 thread/turn 会话接口。 | 共享这个子进程本身不能解决两个 app-server 对同一会话的写入冲突。参见 [协议定义](https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/code-mode-protocol/src/grpc/codex.code_mode.v1.proto)。 |
| Claude 运行中输入 | 本机 `2.1.224` 的 channel 通知以 `mode: prompt`、`priority: next` 入队；执行循环在工具批次后读取 `getCommandsByMaxPriority("next")` 并吸收到当前上下文。它与 `now` 触发中断、`later` 留待后续执行有区别。 | Channels 是满足 R3 的候选路径，不应仅凭文档中的“next turn”判定必须等整个任务结束；仍须真机证明消息在原任务终态前生效。当前源码证据不代表长工具执行期间已经收到，也不代表真实端点验收通过。 |
| Claude 官方界面与回执 | 本机 `claude attach --help` 支持连接官方后台会话；官方 Channels 可接入原会话并提供回复工具，但通知写入成功没有处理回执。自定义 channel 还受预览期启用与组织策略约束。 | 候选是官方会话进程加 Channels，并通过官方 background/attach 保留终端界面。必须验证本机连接配置下可启用、处理回执、输出同步和交互响应。参见 [Channels 协议](https://code.claude.com/docs/en/channels-reference) 与 [官方后台会话](https://code.claude.com/docs/en/agent-view)。 |

### 已确认的原型与文件级实施顺序

Codex 让各端操作同一官方会话实例，窗口选择只负责路由；先验证协议能力，再处理与 R1–R6 冲突的生命周期。Claude 使用现有 ACP 接入，仅修复 C1–C3 的实际阻断，不引入 Channels 或官方终端接手架构。

| 顺序 | 工作及影响范围 | 通过条件 |
| --- | --- | --- |
| P0-A：Codex 隔离原型 | 先验证 App 的 stdio 到同一官方会话服务的适配，官方 CLI 连接同一服务。只在隔离环境验证；产品落点是 `agent/codex_app_daemon_reuse*.go`、`agent/codex_cli_launch.go`、`cmd/codex_cli.go`，新适配文件在原型确定后再命名。 | 真实 App 与官方 CLI 共享原 thread，双向补充同一 active turn；保留 App 工具配置、权限与交互能力。必须覆盖 App 先加载及消息端先加载两种顺序。丢配置、换会话、仅共享 Code Mode 子进程均判失败。 |
| P0-B：Claude ACP 执行核验 | 检查本机已配置的 `claude-agent-acp`，在独立测试会话验证 `initialize`、`session/new`、`session/prompt`、进度及续聊；再核对飞书路由与结果交付。 | C1–C3：真实模型执行成功，原 session 可继续，进度和终态如实返回。原生 CLI Channels 的启用限制不作为该链路的失败证据。 |
| P1：输入和会话生命周期 | Codex 基于 P0-A 现有证据调整 `agent/codex_runtime_turn.go` 及必要调用方。Claude 独立依据 P0-B 的实际问题，修复 `agent/acp_chat.go` 或相关会话处理。 | Codex 满足 R2、R3、R4；Claude 满足 C1–C3。两域不互设原型通过门禁，不提前批量改写状态结构。 |
| P2：消息端接入 | Codex 调整 `messaging/codex_follower_store.go`、`messaging/codex_session_acquire.go` 及对应进度、交互和恢复路径。Claude 仅核对现有飞书任务入口与回传。 | Codex 两个消息端均满足 R1；Claude 按 C1–C3 验证飞书，不扩展为微信或 CLI 对等验收。 |
| P3：Codex 释放和设置闭环 | 统一复核 `messaging/codex_thread_release.go`、`messaging/codex_thread_unsubscribe.go`、`agent/codex_thread_handoff.go`、`agent/codex_model.go`、`messaging/codex_model.go`。 | R5、R6：释放后本地立即可继续原会话；模型与强度的反馈对应实际生效配置。现有模型补丁只能在这一闭环通过后纳入交付。 |
| P4：必要回归与使用反馈 | 合并执行受影响模块回归、静态检查和构建；复用已有原型证据。 | 按用户最新要求取消重复手动实测及 24 场景前置门禁；未实测能力不得写成已验证，后续按真实使用中的具体问题修复。 |

Codex 根据已完成的原型证据继续 P1–P3；P0 的未覆盖场景不再要求用户反复配合。Claude 按用户要求暂停，恢复前不再请求模型服务。

Codex 原型若必须要求日常关闭 App、禁用官方 CLI、每次释放重启、等待任务结束或迁移到新会话，直接判为不能覆盖 R1–R6 并报告缺口。是否接受这类限制只能由用户决定。两域验证均不覆盖当前安装、生产绑定或活动会话；回退只停止本次创建且身份可核对的隔离进程、撤销本次测试设置，保留既有源码改动和原会话数据。

### 首轮 P0 实测结果

**历史结论：范围修订前的 P0 未通过，P1–P4 当时未开始。Codex 局部通过项仍不代表完整多端接手；Claude Channels 结果不用于判定最新 C1–C3。** 原型代码与运行数据位于受保护的本机临时目录，没有接入产品；运行版本仍为上表所列版本。

| 验证项 | 实测结果 | 证据与边界 |
| --- | --- | --- |
| Codex App / 官方 CLI 连接共享服务 | 连接层通过。隔离 App 的 JSONL 经 WebSocket 适配后连接同一个官方 app-server，官方 CLI 通过 `--remote` 连接该 socket；握手返回的 home 与隔离目录一致。 | 适配器保留了 App 的完整启动覆盖项，包括 `mcp_servers.codex_app`；界面连通不代表工具与双向接手已通过。 |
| 官方 CLI 执行中接受另一连接的输入 | 真实通过。官方 CLI 执行 `sleep 20` 时，观察者收到命令开始事件，第二连接向原 turn 发送带 `expectedTurnId` 的 `turn/steer`；原命令退出码为 0，原 turn 正常完成，最终回复从旧标记变为 `P0_REMOTE_STEER_B`。 | 使用真实模型和官方 TUI，没有取消、重启或新建会话代替这次输入；只覆盖 CLI 与第二协议连接，尚不是飞书/微信端点验收。 |
| 第二连接退订后，本地继续 | 真实通过。第二连接收到 `unsubscribed` 后，官方 CLI 在同一 thread 的下一轮回复 `P0_LOCAL_AFTER_RELEASE_C`。 | 这证明共享服务上的该链路可行；尚未执行真实 `/cx release`，也未证明 App 的 R5。 |
| App 工具保留 | 未通过。`codex_app` 工具列表为空，官方 MCP 返回 `Codex app tools pipe closed`，App 日志为 `missing-code-signing-identity`。 | 直接启动与 LaunchServices 启动均复现；官方 Node 文件签名校验通过，直接运行 App 自带 MCP 程序也复现。尚未定位运行时身份判定失败的根因，不能断言所有官方版本都不支持该路线。未修改签名校验。 |
| 真实 App 操作 | 未验收。电脑操作工具出于安全策略拒绝控制 Codex 应用。 | 曾请求用户执行隔离界面的标记输入；因工具保留项未通过，已暂缓该步骤。不得用协议客户端代替真实 App 输入证据。 |
| Claude Channels 启用 | 存在实际配置冲突。沿用本机 `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` 时，官方 CLI 明确忽略开发通道参数并提示 `Channels are not currently available`。 | 在隔离目录复用本机原有特性缓存、且不带该环境项时才观察到 Channels 启用。该差异只用于定位，未修改用户设置，不能作为当前配置已可用的证据。 |
| Claude 原任务内输入及官方 attach（已退出必需范围） | 未通过真实验证。原生 CLI 的测试模型请求在沙箱内外均报告证书/连接错误，未执行测试工具。 | WeClaw 配置指向 `claude-agent-acp`，没有单独 env 覆盖，但仍需核对 adapter 实际使用的 SDK/CLI；不能把原生 CLI 的失败直接当作 ACP 已验证失败。 |

上述首轮之后曾转为核验 Claude ACP；用户随后要求暂停 Claude，当前继续 Codex。临时原型的重建属于测试环境排障，不作为 Codex R5 的释放流程。

收尾核验：本次隔离 App、服务、CLI 与通道测试进程已退出，日常 Codex 入口指向与修改时间未变；临时 Codex 登录文件副本已移除，保留受保护的本地测试证据。没有安装、发布或重启生产服务。文档校验和 `git diff --check` 通过，本轮没有新增产品代码改动。

### Codex App 工具连接的后续定位

- 保持 App 自带 native peer authorization 不变，使用其官方签名 Node 做单变量对照：直接启动时默认 HOME 与隔离 HOME 均通过；通过 Python 启动时，即使保持默认 HOME 也报告 `missing-code-signing-identity`。根因是受保护连接检查的祖先进程身份，不能归因于 HOME 隔离。
- 临时适配器改为 App 自带签名 Node 直接启动官方 app-server；Python 只做独立的 JSONL/WebSocket 转换，不再作为共享服务的父进程。未修改、关闭或跳过 App 的签名校验。
- 经上述启动链，官方 App MCP 的 `initialize` 与 `tools/list` 成功；未带登录副本时返回 27 个工具，恢复隔离登录副本后返回 38 个工具。目录成功只是连接层证据，仍需核对会话实际连接、工具执行、App 双向输入及 R5/R6。
- 随后的会话级 `mcpServerStatus/list` 显示 `codex_app` 为 `connected`；官方 CLI 恢复原测试会话后，真实模型成功执行 App 的只读用量查询工具，并返回约定标记。没有关闭 App 工具或丢弃启动覆盖项。
- 在原会话中，第二连接先只切换模型，再单独切换推理强度，每次均退订后由官方 CLI 发起下一轮。持久化 `turn_context` 分别确认实际执行为 `gpt-5.6-sol / medium`、`gpt-5.6-sol / high`，没有被 CLI 的旧设置覆盖。该证据覆盖第二协议连接与 CLI，尚不是飞书入口或 App 设置验收。
- 修正启动链后重新验证两个方向：CLI 发起 `sleep 20`，第二连接的补充进入原 turn；第二连接发起 `sleep 30`，官方 CLI 通过输入框回车补充，原 turn 包含两条用户输入并输出新标记。两次均仅执行一次等待命令、退出码为 0、原 turn 正常完成，之后第二连接退订成功。仍未用真实消息平台替代第二协议连接。
- 原型保存在受保护临时目录；产品已新增 shared 模式、App stdio 适配入口、签名 Node 启动链与工具连接刷新。正式安装仍未改变。
- 用户确认隔离 App 输入框可用并已发送等待任务；App 在原 CLI thread 中实际执行一次等待命令，退出码 0、任务正常结束。监听器未捕获该轮 Code Mode 事件，因此本轮 App 运行中跨连接补充不标为已验证。按用户最新要求不再重复安排实测。

### 范围修订后的 Claude ACP 核验（现已暂停）

- 本机配置使用 `claude-agent-acp 0.58.1`、`claude-agent-sdk 0.3.205`，adapter 实际执行其自带 Claude Code `2.1.205`，并非先前原生 CLI 原型的 `2.1.224`。
- 通过当前 WeClaw `agent.NewACPAgent` 创建独立测试会话，保留用户原有模型、连接和非必要流量设置；`initialize`、`session/new` 成功，模型快照为 `claude-fable-5-1`。没有使用 Channels，也没有改写日常会话或配置。
- 真实任务是读取隔离目录中的标记文件，再在原会话续聊核对。首轮 `session/prompt` 等待 100 秒后超时，未收到进度事件或最终回复，因此后续上下文验证未执行。仅开启 SDK 诊断的第二次测试限定 40 秒，记录到多次 `API error ... Connection error`，不能把该请求标为执行成功。
- 对同一配置模型地址执行不带凭据、保持 TLS 校验的只读请求，TLS 握手阶段返回 `SSLZeroReturnError: TLS/SSL connection has been closed (EOF)`。这把当前阻断定位到模型连接层；远端服务、网络路径及本机连接环境仍需进一步区分，不能据此认定是认证或特定证书故障。
- 现有飞书 Claude 路由、ACP 和会话回归测试通过：`go test ./messaging ./agent -run 'Test(FeishuClaude|Claude.*(ACP|New|Session|Resume)|ACPAgentChat|HandleClaude)' -count=1 -timeout 120s`。这不是实际飞书客户端的任务回传证据，C1–C3 仍未完成。
- 本轮只更新任务范围与证据；尚无需要修改产品代码的已定位缺陷。两次测试的 ACP 进程组均已退出，产物保存在受保护的临时目录，扫描未发现认证 Token 落盘；生产服务未安装、重启或改动绑定。

### 当前实现与交付边界

- [x] 保留 R1–R6，Claude 暂停；根据用户反馈取消频繁手动实测。
- [x] 新增 `agent/codex_app_bridge*.go`、内嵌 Node 启动与 MCP 环境脚本、`cmd/codex_app_bridge.go`。App 完整配置进入同一官方 Host，shared 关闭私有 Desktop 写入所有权。
- [x] 官方 CLI 连接同一 shared socket；App 重开时更新工具 pipe，连接退出仅断开当前 frontend；启动超时回收本次启动器，避免延迟产生第二个 Host。
- [x] 模型/强度更新遇到未加载 thread 时恢复原 thread 后重试，最后释放临时订阅；微信与飞书共用持久 follower 和授权回传。
- [x] `go test -race ./agent ./messaging ./config ./cmd -count=1 -timeout 240s` 通过；包含真实本地 WebSocket 转发和 Node preload 检查，不调用真实模型。
- [x] 静态检查、darwin/arm64 与 linux/amd64 构建、文档校验和差异检查通过；本轮隔离 App/Host 及其 19 个受控进程已退出，临时登录副本已移除，日常 Codex 入口未变化。
- [ ] 已获发布授权：执行 v0.1.305 正式发布、weclaw update 安装与首次共享入口切换；以实际发布和运行状态分别确认结果。

尚未逐项实测 App/CLI/飞书/微信全部双向组合，也未在本轮从真实飞书执行 `/cx release`；现有协议与 App 原任务证据不冒充这些结果。此处是证据边界，不再要求用户补做手动矩阵。

## 2026-09-01 Codex 可用性优先多前端写入：候选实现记录

本节保留已有代码工作与当时的验证记录，勾选只表示对应实现步骤完成。原目标中的“同一已验证 Host”是待验证的技术前提；本节不替代上面的 R1–R6，也不证明真实 Codex App、CLI、飞书、微信已共享同一执行端。

### 目标

让 Codex App、受控 CLI、飞书和微信在同一已验证 Host 上共享 thread 时，普通输入以权威 Host 状态为准，不再被 frontend binding、follower observer 或进度卡同步状态排他阻断。

### 范围与验收标准

- [x] binding 只表示当前窗口选择；Host/目标不可读时保留旧 binding，observer/卡片/历史失败时提交 binding 并标记同步降级。
- [x] 普通输入统一执行权威 `read -> steer/start -> 竞态重读`，不依赖 follower `ready`，不为 Host 不可用的输入排队。
- [x] 写后结果未知只以 turn/items 基线和消息摘要核对；无证据时不自动重试，不持久化完整输入。
- [x] `/cx status` 分开显示绑定、运行通道和进度同步；旧 v14 `preparing/ready` 无需迁移且立即可写。
- [x] 普通 switch/release 不重启 daemon；无人继续观察时调用当前连接的 `thread/unsubscribe`。
- [x] 扩展 official daemon 双客户端协议门禁，覆盖 read 不订阅、并发 start 转 steer、unsubscribe/observer 中断不影响另一客户端和终态补查。
- [x] 更新中英文 README、维护者上下文和长期经验。
- [x] 完成全仓 test、Race、Vet、module tidy、Staticcheck、govulncheck、安装脚本、双平台构建、文档校验和差异复核。
- [ ] 历史计划中的 App/飞书完整手动矩阵尚未执行；测试安排按上方用户最新修订，不再作为本轮实现前置门禁。

### 验证方式

```bash
go test ./... -count=1 -timeout 300s
go test -race ./... -count=1 -timeout 480s
go vet ./...
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
sh scripts/install_test.sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

### 回滚

- 代码回滚只恢复旧的 follower-ready 写入门禁和 Host handoff；不得删除 v14 会话状态、input-attempt 元数据、Codex rollout、SQLite 或 terminal outbox。
- 若真实多前端验收暴露上游协议不兼容，保留唯一 Host 和未知交付防重复边界，停止发布并补充兼容修复；不启动第二个 app-server。

### Review

- 实现与自动化门禁已完成：全仓普通测试、全仓 Race、Vet、module tidy、Staticcheck、govulncheck、安装脚本 24 项、文档/格式/差异检查和 darwin/arm64、linux/amd64 构建均通过。
- 最终复核确认：v14 会话格式保持为 14，配置与发布脚本未改；普通 switch/release 不包含 Host 重启，未知交付没有自动重试或完整输入持久化。
- `TestCodexOfficialDaemonTwoClientProtocol` 已扩展但仍是隔离环境 opt-in 门禁，本轮没有用真实 `CODEX_HOME` 执行；Codex App + 飞书双向输入及同步故障注入当时未验收；本轮测试安排以上方最新用户修订为准。
