---
ai_summary:
  purpose: "记录 2026-07-14 对修复提交 9af1731 的复审结论：上一轮缺陷的修复验收、两处新引入的 interrupted 终态缺陷、遗留项现状与后续建议。"
  read_when:
    - "修复 interrupted 事件消费者遗漏或复核其状态时。"
    - "评估 2026-07-13 审查报告所列缺陷是否已经解决时。"
    - "改动 watcher 生命周期、飞书去重所有权或顺序屏障前了解既有不变量时。"
  source_of_truth:
    - "agent/codex_turn_dispatch.go"
    - "agent/codex_thread_watch.go"
    - "messaging/codex_external_task.go"
    - "messaging/task_admission.go"
    - "feishu/dedup.go"
    - "feishu/dispatch_order.go"
  verify_with:
    - "python3 scripts/validate_docs.py . --profile generic"
    - "git diff --check"
  stale_when:
    - "interrupted 事件消费者遗漏被修复，或本报告所列遗留项状态变化后。"
    - "watcher、dedup、顺序屏障或任务准入机制再次重构后。"
---

# WeClaw 复审报告（2026-07-14）

## Purpose

对提交 `9af1731`（修复：统一会话切换与任务终态）做只读复审：验收它对 `docs/CODE_REVIEW_2026-07-13.md` 所列缺陷的修复质量，审查新增并发机制是否引入新问题，并更新遗留项清单。本报告取代 2026-07-13 报告作为当前缺陷状态的权威来源；原报告保留为 `9b42cda` 时点快照。

## Source of truth

- 复审基准：commit `9af1731`（基于上次审查基准 `9b42cda` 的修复提交，55 个文件，约 +1977/-161），工作区无代码改动。
- 复审方法：对修复 diff 的专项深审（逐项验收 P1–P6、新文件通读、锁序全局核查），叠加对 watcher 停止时序、dedup 所有权状态机、顺序屏障、任务准入等核心机制的独立人工验证，双方结论交叉一致。
- 修复方自述见 `tasks/code-review-fixes-2026-07-13.md` 与 `tasks/codex-interrupted-reconciliation.md`。

## Key facts

上一轮报告的 7 个高/中危缺陷全部真实修复，无"修一半"路径；新增的并发机制（所有权化去重、单临界区任务准入、同窗口顺序屏障、pendingSteering 占位）选型正确、锁序全局一致、race 全绿。但随修复引入的"interrupted 结构化终态"改造遗漏了两个既有事件消费者，构成两处新的中危缺陷（见下），建议作为紧随其后的小修处理。

复审自动化基线全部通过：`go build`、`go vet`、`staticcheck`（`all` 级别，仅排除 ST1000/ST1005，零告警）、`go test ./...`（12 个含测试的包全部 PASS）、`go test -race ./messaging ./feishu ./agent`（无 DATA RACE）。

## 修复验收（对照 2026-07-13 报告）

- **高 1 飞书附件失败与去重记账 — 已正确修复。** `feishu/dedup.go` 改为 reserve/complete/release 三态所有权状态机：预约只占用 processing（不落盘），处理成功才提交为 seen 并持久化；失败按 `feishu/resource_error.go` 分类——永久错误（超 32MiB、官方明确要求修正输入的错误码）通知用户后 complete 防止重投重复打扰，瞬时错误通知后 release 允许重投重试；通知本身发送失败也会 release 并把错误返回给 SDK。processing 条目有 10 分钟 TTL 兜底，即使异常遗漏 release 也不会永久占用。修复质量超出原建议。
- **高 2 watcher 非终态生命周期 — 已正确修复。** 三条泄漏路径全部封闭：`messaging/codex_external_task.go` 在非终态且 `isStopping()` 时强制转失败终态（`cancelActiveTask` 持 task.mu 先置 stopping 再解锁后 cancel，时序经独立验证无竞态窗口）；rollout 来源错误一律判终态失败；turn 被替换从"继续无限轮询"改为报错终态。临时 Desktop 断线仍保持观察（有意设计）。注意：其正确性依赖"取消 watcher ctx 前必先 markStopping"这一隐式不变量，当前所有取消入口满足，建议补断言或注释固化。
- **高 3 标题字节截断 — 已正确修复。** `truncateUTF8Bytes` 逐 rune 累计字节预算，永远切在字符边界；对无效 UTF-8 输入只会保守提前截断，不会越界。
- **中 4 广播 ClientID 竞态 — 已根除。** 直接删除无锁写，唯一性由 `wechat/replier.go` 既有的 `clientIDUsed` 机制（r.mu 内）保证；顺带消除了 messaging 对 wechat 的一处引用。
- **中 5 文本去重误伤 — 已正确修复。** `messaging/message_dedup.go` 重复文本只在同 owner+route+内容指纹的任务仍在运行时拦截，否则刷新放行；锁序（activeTasksMu → task.mu）与全仓一致。代价是任务结束后 TTL 内的真重投会执行第二次，属无消息 ID 场景下的自觉取舍。
- **中 6 保存目录失败反馈 — 已修复**（`messaging/incoming_attachments.go` 补发用户提示）。
- **中 7 启动/排队窗口 — 已正确修复。** 新增 `messaging/task_admission.go`：`beginOrQueueActiveTask` 在 activeTasksMu 单临界区内完成启动或排队，四态返回区分"任务已消失"与"队列占用"，Claude/Codex/广播三条路径统一接入。
- **低 /guide 回滚窗口 — 已修复**（pendingSteering 占位替代"取走再还回"，任务在发送期间自然终态时由 `finishExternalCodexGuide` 补执行，消息不丢）。
- **额外修复（上轮未列出）**：飞书卡片会话切换与普通消息抢跑竞态——新增 `feishu/dispatch_order.go` 同窗口顺序屏障，reserve 在 ws 回调内同步登记保证登记顺序等于接收顺序；卡片回调改为"已提交"语义 + 2 分钟执行上限，超时经 `defer finish()` 释放同窗口队列，无泄漏。

## 新引入缺陷（按严重度）

### 中 A：interrupted 终态事件在通道拥塞时会被丢弃

位置：`agent/codex_turn_dispatch.go`（`isCodexTurnControlEvent`）。

本次修复把 Codex `turn/completed status=interrupted` 的事件 Kind 从 `"error"` 改为结构化的 `"interrupted"`（配合 rollout 核对，见 `tasks/codex-interrupted-reconciliation.md`），但控制事件清单仍只收录 `completed`/`error`/`started`。旧 Kind 享有保留容量与挤占投递的送达保证；新 Kind 走普通事件路径，通道接近满（容量 256、水位 cap-8）时被直接丢弃。进度密集的 turn 恰在拥塞瞬间被中断时，`chatCodexAppServerTurn` 永远收不到终态，挂到任务超时以 DeadlineExceeded 失败，且绕过了本应触发的 rollout 核对。与该文件"终态不能丢"的注释直接矛盾。

修复方向：把 `"interrupted"` 加入控制事件清单，一行修复。

### 中 B：WatchCodexThread 把真实中断误报为成功

位置：`agent/codex_thread_watch.go`（事件循环与 `reconcileAttachedCodexTurn`）。

事件循环只处理 `"error"`（失败终态）与 `"completed"`（成功终态），`"interrupted"` 事件被静默滑过；随后 2 秒 reconcile 发现 turn 不再 active，按成功返回 assembler 文本 / LastAgentMessageText / "已完成，但没有返回文本"。修复前该场景会以"已中断"失败终态结束。影响外部任务 watcher 与 Desktop 重连观察路径：用户在 Codex Desktop 手动停止任务，微信/飞书侧却收到成功回复与部分文本。chat 路径已完成结构化 reconcile 改造，watch 路径未同步——与缺陷 A 同根因（共享事件 Kind 改名只对齐了一个消费者）。

修复方向：在 watch 循环中给 `"interrupted"` 加与 chat 路径一致的 rollout 核对分支，或至少映射回失败终态。

### 低（择要）

- **dedup 处理权超时的静默丢弃**：`feishu/dedup.go` 的 cleanup 会清掉超过 10 分钟的 processing 占用；附件处理若超 10 分钟（32MiB 上限下罕见），complete 失败后仅日志、无用户反馈。理论风险，记录备查。
- **卡片动作 2 分钟超时无用户反馈**：`feishu/adapter_events.go` 超时仅打日志，卡片停留"已提交，正在处理"，用户无从知道动作未生效。另有一次性过渡边界：修复前已下发的旧卡片回调无 SessionKey，回退用 ChatID 作分发键，不与消息共享同一顺序队列。
- **消息路径顺序屏障的队头等待无上限**：`dispatchIncomingMessage` 等待前序票据无超时。对主推的 Codex/Claude ACP（任务异步、dispatch 毫秒级返回）无实质影响；若飞书窗口配置同步执行的 cli/http 型 agent，同窗口后续消息（含 `/stop`）会在 adapter 层排队且零反馈直到前一任务完成。lark ws SDK 的事件派发并发模型本次未能核实，若为串行派发则单窗口阻塞会放大。建议给消息票据也加超时或确认 SDK 行为。
- 小项：`storePendingGuide` 已无生产调用方（死代码）；`toIncomingFromMessage` 仅剩测试使用；`cancelActiveTask` 置 stopping 不判 terminal，与自然完成竞态可吞一条 pending（先前已存在的窄窗口）。

## 遗留未修复项（上轮低危，本次确认仍存在，均未列入修复计划）

1. `api/server.go` — `json.NewEncoder(w).Encode` 返回值被忽略。
2. `messaging/approvals.go` — 同用户多个待审批时文本回复因歧义被静默忽略。
3. `messaging/codex_feishu_cards.go` — 仍用中文子串嗅探命令回复是否为错误。
4. `messaging/handler_config.go` — `SetSaveDir` 仍是唯一无锁 setter（当前仅启动期调用）。
5. `messaging/admin_commands.go` — 管理命令超时在等待 `serviceAdminMu` 期间就开始计时。

架构层三笔债（Handler 上帝对象、wechat 依赖豁免、执行管道三胞胎）与测试盲区（remotefetch 拨号路径、web 写操作、wechat CDN、config.Load、symlink 逃逸）状态不变，仍以 2026-07-13 报告的分析为准。

## 建议行动

1. **紧随小修**：缺陷 A（一行）+ 缺陷 B（同根因，建议同一提交处理），并为"取消必先置 stopping"补断言或注释。
2. **观察确认**：核实 lark ws SDK 事件派发是否 per-event goroutine，决定消息票据是否需要超时。
3. 其余低危与架构项按 2026-07-13 报告的优先级推进。

## How to verify

quick:

```bash
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

full:

```bash
go test ./... -count=1 -timeout 180s
go vet ./...
go test -race ./messaging ./feishu ./agent -count=1 -timeout 180s
```

## Stale when

- 缺陷 A/B 或任一遗留项被修复后。
- watcher 生命周期、飞书去重、顺序屏障、任务准入或 interrupted 终态语义再次改动后。
- 新增平台、Agent 类型或安全边界后未重新审查。
