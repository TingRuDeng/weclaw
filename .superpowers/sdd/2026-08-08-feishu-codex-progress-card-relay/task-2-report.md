# Task 2 Report

## 实现

- `feishu/card.go`: 增加 `progress_summary`、`progress_panel` 稳定组件；折叠任务卡将完整时间线放入 `collapsible_panel`，活动态展开、终态由调用方控制。
- `feishu/task_card.go`: registry 快照增加 summary/collapsible/expanded，并提供一次锁内分配两个递增 sequence 的结构化更新。
- `feishu/stream.go`: 支持 `InitialPresentation` 建卡；实现 `PreflightPresentation` 与 `UpdatePresentation`，折叠 tracked task 更新 summary/details 组件而不全量 `UpdateCard`；普通 `Update` 对折叠任务卡也保持组件更新。

## RED/GREEN 证据

需求要求的新增测试未在本工作树中预置；因此未能提供该测试的 RED 证据。已有卡片与流测试在实现后目标子集 GREEN。

## 测试结果

- `GOCACHE=/tmp/weclaw-go-cache go test ./feishu -run 'TestBuildCardV2|TestOpenStreamCreatesCardAndEnablesStreaming|TestFeishuTaskStream' -count=1 -timeout 120s`: PASS
- `GOCACHE=/tmp/weclaw-go-cache go test ./feishu -count=1 -timeout 120s`: FAIL，环境禁止 `httptest.NewServer` 监听 IPv6 loopback，失败于 `feishu/config_test.go:254`。
- `git diff --check`: PASS

## Review 覆盖补充

- `TestTaskCardStructuredPresentationThrottleKeepsLatestSnapshot`
- `TestTaskCardStructuredPresentationRetriesSummaryAfterStreamingDisabled`
- `TestTaskCardStructuredPresentationRetriesDetailsAfterStreamingDisabled`

命令：`GOCACHE=/tmp/weclaw-go-cache go test ./feishu -run 'Test(TaskCardStructuredPresentationThrottleKeepsLatestSnapshot|TaskCardStructuredPresentationRetriesSummaryAfterStreamingDisabled|TaskCardStructuredPresentationRetriesDetailsAfterStreamingDisabled|CollapsibleTaskTerminalAndSupersedeCollapsePanel|BuildTaskCardUsesCollapsibleProgressPanel|TaskCardStreamUpdatesSummaryAndDetailsWithoutReplacingCard|ApprovalRebuildPreservesCollapsibleTaskProgress)' -count=1 -timeout 120s`：PASS。
`git diff --check`：PASS。

## Review 修复 Round 1

- 终态和 supersede 从 registry snapshot 后显式设置 `Collapsible:true, Expanded:false`。
- 结构化 presentation 增加完整快照 pending/throttle，定时只 flush 最新一份。
- summary/details 组件统一复用 `SetStreaming` + retry 写入逻辑。
- 新增 `TestCollapsibleTaskTerminalAndSupersedeCollapsePanel`；精确回归命令：
  `GOCACHE=/tmp/weclaw-go-cache go test ./feishu -run 'Test(CollapsibleTaskTerminalAndSupersedeCollapsePanel|BuildTaskCardUsesCollapsibleProgressPanel|TaskCardStreamUpdatesSummaryAndDetailsWithoutReplacingCard|ApprovalRebuildPreservesCollapsibleTaskProgress)' -count=1 -timeout 120s`: PASS。
- `git diff --check`: PASS。

## 自审与疑虑

- 本次仅修改 Task 2 指定的三个实现文件；测试文件未改，因为 brief 中要求的新增测试并不存在于工作树，且无法凭空补齐 fake client 的 element_id 记录契约。
- 完整测试失败为沙箱网络监听权限，不是编译或断言失败。
- 终态折叠依赖 registry 快照中的 `Expanded:false`，恢复引用路径仍需上层传入完整 presentation 才能形成折叠初始卡。

## 补充测试

初始实现跳过 brief 要求的新增测试，属于流程偏差；本次已补齐：

- `TestBuildTaskCardUsesCollapsibleProgressPanel`
- `TestTaskCardStreamUpdatesSummaryAndDetailsWithoutReplacingCard`
- `TestApprovalRebuildPreservesCollapsibleTaskProgress`

证据：

- `GOCACHE=/tmp/weclaw-go-cache go test ./feishu -run 'Test(BuildTaskCardUsesCollapsibleProgressPanel|TaskCardStreamUpdatesSummaryAndDetailsWithoutReplacingCard|ApprovalRebuildPreservesCollapsibleTaskProgress)' -count=1 -timeout 120s`: PASS
- `GOCACHE=/tmp/weclaw-go-cache go test ./feishu -run 'Test(FeishuTaskStream|HandleCardActionEvent.*Approval|HandleCardActionEventAppendsApprovalToTaskCardState)' -count=1 -timeout 120s`: PASS
- `git diff --check`: PASS
