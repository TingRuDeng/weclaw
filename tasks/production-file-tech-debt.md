# 生产文件技术债处理计划

## 目标

在不改变业务行为的前提下，继续降低生产 Go 文件体量和职责混杂带来的回归风险。优先处理当前超过 300 行且边界清晰的文件。

## 非目标

- 不修改用户命令、配置字段、飞书/微信交互语义、发布流程或持久化格式。
- 不处理测试文件拆分；测试体量已在前序任务中处理到可接受范围。
- 不顺手修功能 bug、调日志策略或引入新抽象。
- 不提交、不发布，除非用户后续明确要求。

## 当前事实

- 当前分支：`main...origin/main`。
- 工作区已有未提交改动，继续处理时必须保留这些改动并避免覆盖。
- `messaging/handler.go` 当前 289 行，`agent/acp_agent.go` 当前 78 行，不再是本轮主要技术债。
- 当前超过 300 行的生产 Go 文件包括：
  - `feishu/adapter.go`：416 行，混合 adapter 主体、消息事件、卡片事件、审批幂等和审批卡片更新。
  - `config/config.go`：398 行，混合配置结构、权限派生、进度配置、加载保存和环境变量覆盖。
  - `cmd/update.go`：359 行，混合 CLI 命令、release 资产、校验和、下载、替换二进制和重启。
  - `ilink/monitor.go`：346 行，混合轮询、队列、聚合、状态持久化和摘要格式化。
  - `feishu/choice.go`：342 行，混合普通选择卡、审批按钮、回调解析和已处理卡片渲染。
  - `cmd/doctor.go`：326 行，混合 doctor 依赖、检查编排和各类检查项。

## 决策日志

- 有没有更优雅的方式：优先机械拆分，而不是改接口或抽新框架；当前风险来自文件职责过多，不来自抽象缺失。
- 优先拆生产文件，不动测试文件：测试文件当前最大 323 行，收益低于生产文件。
- 优先拆 `feishu/adapter.go` 和 `feishu/choice.go`：近期飞书审批、按钮、会话路由反复出问题，降低局部复杂度收益最高。
- 第二优先拆 `cmd/update.go`：更新/重启链路高风险，但可按 release/download/checksum/restart 边界机械移动。
- `config/config.go` 与 `ilink/monitor.go` 暂排后：配置和轮询状态耦合较强，先不混入飞书与更新链路拆分。
- 本轮串行执行：当前工作区已有未提交改动，生产文件拆分若并行写入容易增加 review 成本。

## 执行计划

- [x] P0 串行：拆 `feishu/adapter.go` 的审批处理
  - 新增 `feishu/adapter_approval.go`。
  - 移动 `approvalRecord`、`feishuApprovalTTL`、`handleApprovalCardAction`、`approvalActionToast`、`approvalActionExpired`、`approvalActionOwnedByUser`、`recordApprovalAction`、`updateApprovalActionRecord`、`purgeApprovalsLocked`、`nowOrDefault`、`approvalActionKey`、`updateTaskCardWithApproval`、`updateApprovalPanelWithAction`。
  - 保持函数签名和调用点不变。
- [x] P1 串行：拆 `feishu/adapter.go` 的事件分发
  - 新增 `feishu/adapter_events.go`。
  - 移动 `newEventDispatcher`、`handleMessageEvent`、`handleMirrorDedup`、`dispatchIncomingMessage`、`handleCardActionEvent`。
  - `adapter.go` 保留结构体、构造器、平台接口方法和 `Run`。
- [x] P2 串行：拆 `feishu/choice.go`
  - 新增 `feishu/choice_parse.go`，移动 `parseCardAction`、`callbackValueString`、`firstStringValue`。
  - 新增 `feishu/choice_status_card.go`，移动 `buildChoiceHandledCard`、`buildChoiceHandledStatusCard`、`approvalHandledStatus`。
  - `choice.go` 保留选择卡构建与按钮构建。
- [x] P3 串行：拆 `cmd/update.go`
  - 新增 `cmd/update_release.go`，移动 `releaseAssetNameForRuntime`、`getLatestVersion`、`releaseTagFromLatestRedirect`、`newGitHubRequest`、`githubAuthToken`。
  - 新增 `cmd/update_checksum.go`，移动 `verifyReleaseAssetChecksum`、`parseReleaseChecksums`、`verifyDownloadedAssetChecksum`。
  - 新增 `cmd/update_install.go`，移动 `downloadFile`、`replaceBinary`、`resolveSymlink`、`validateUpdateTargetMatchesRuntime`。
  - `update.go` 保留 cobra 命令与 `runUpdate` 主流程。
- [x] P4 串行：运行验证和 review-gate。

## 验证矩阵

- `gofmt -w` 受影响 Go 文件。
- `go test ./feishu ./cmd -count=1`。
- `go test ./... -count=1 -timeout 120s`。
- `go test -race ./feishu ./cmd -count=1 -timeout 120s`。
- `go vet ./...`。
- `python3 scripts/validate_docs.py . --profile generic`。
- `git diff --check`。

## 风险与预想失败场景

- 机械移动漏 import：通过 `go test` 和 `go vet` 捕获。
- 同包未导出函数被移动后仍可访问，但注释和命名可能需要同步：通过编译和 review-gate 捕获。
- 当前已有未提交改动：执行时只触碰计划内文件，避免覆盖前序修复。
- `go vet` 可能触发 Go build cache 沙箱权限问题：若沙箱失败，按权限规则申请非沙箱重跑。

## 进度记录

- [x] 已完成只读现状分析。
- [x] 用户已确认 HARD-GATE。
- [x] P0 已完成：新增 `feishu/adapter_approval.go`，审批记录、幂等、审批回调和审批卡片更新已从 `feishu/adapter.go` 拆出；`go test ./feishu -count=1` 通过。
- [x] P1 已完成：新增 `feishu/adapter_events.go`，消息事件、镜像去重、卡片回调分发已从 `feishu/adapter.go` 拆出；`go test ./feishu -count=1` 通过。
- [x] P2 已完成：新增 `feishu/choice_parse.go` 和 `feishu/choice_status_card.go`，卡片回调解析与已处理状态卡渲染已从 `feishu/choice.go` 拆出；`go test ./feishu -count=1` 通过。
- [x] P3 已完成：新增 `cmd/update_release.go`、`cmd/update_checksum.go`、`cmd/update_install.go`，release 查询、checksum 校验、下载安装辅助已从 `cmd/update.go` 拆出；`go test ./cmd -count=1` 通过。
- [x] P4 已完成：计划内验证全部通过。

## Review 小结

终态：finished。

Spec 符合度：通过。本轮严格按已确认计划拆分 `feishu/adapter.go`、`feishu/choice.go`、`cmd/update.go`，未修改用户命令、配置字段、交互语义、发布流程或持久化格式。

安全检查：通过。未新增 secret、网络访问入口、权限放宽、mock、fallback 或静默降级；更新链路的下载、校验和替换逻辑只是同包机械移动。

测试与验证：通过。验证命令：

- `go test ./feishu -count=1`
- `go test ./cmd -count=1`
- `go test ./feishu ./cmd -count=1`
- `go test ./... -count=1 -timeout 120s`
- `go test -race ./feishu ./cmd -count=1 -timeout 120s`
- `go vet ./...`
- `python3 scripts/validate_docs.py . --profile generic`
- `git diff --check`

复杂度检查：通过。目标文件拆分后行数：

- `feishu/adapter.go`：133 行。
- `feishu/adapter_approval.go`：203 行。
- `feishu/adapter_events.go`：99 行。
- `feishu/choice.go`：202 行。
- `feishu/choice_parse.go`：81 行。
- `feishu/choice_status_card.go`：73 行。
- `cmd/update.go`：133 行。
- `cmd/update_release.go`：80 行。
- `cmd/update_checksum.go`：63 行。
- `cmd/update_install.go`：107 行。

Document-refresh: not-needed
原因：本轮是职责拆分，不改变公开命令、配置、发布目标或用户文档语义。

剩余风险：当前仍有其他生产文件超过 300 行，例如 `config/config.go`、`ilink/monitor.go`、`cmd/doctor.go`、`cmd/codex_app_protocol.go`、`api/server.go`；它们不在本轮已确认计划范围内。

潜在技术债：配置加载/校验、微信轮询聚合、doctor 检查和 Codex App 协议仍可继续按独立计划拆分。

结论：通过。
