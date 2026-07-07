# 当前任务记录

## 目标

修复普通 Agent 回复被飞书卡片化的问题：生成过程只允许任务卡显示最新状态行，最终正文必须走普通文本；普通方案列表不能被误判成按钮卡。

## 现状分析

- `messaging/progress.go` 的 `handleProgressDelta` 会把 delta 累积为 tail，并交给 `renderDeltaProgress` 渲染。
- `messaging/progress_render.go` 的 `renderDeltaProgress` 在 stream 模式下展示 tail 预览，容易把正文碎片持续刷进卡片。
- `messaging/reply_delivery.go` 已支持 `FinalReplyOutsideStream`，但缺少回归测试确保普通最终正文不会进入任务卡。
- `messaging/choices.go` 的 `containsChoicePrompt` 曾把任意“选择”当成按钮触发词，普通方案说明中出现“用户选择”加编号列表时会误判成按钮卡。

## 执行任务

- [x] P0 串行：补 RED 测试，确认 stream 模式只渲染最后一条非空状态行。
- [x] P1 串行：实现最后非空行提取和长度限制。
- [x] P2 串行：同步进度会话测试中的旧“实时片段”断言。
- [x] P3 串行：补 RED 测试，覆盖普通方案列表提到“选择”时不触发按钮卡。
- [x] P4 串行：收紧 choices 触发词，只保留明确要求用户回复编号的提示。
- [x] P5 串行：补回归测试，确认普通最终回复不进入 stream 卡片正文。
- [x] P6 串行：运行定向测试、全量测试、vet、文档校验和 diff check。
- [x] P7 串行：执行 review-gate。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestDetectChoices|TestFinalReplyOutsideStreamDoesNotPutOrdinaryAnswerInCard|TestStreamMode|TestStartProgressSession|TestSendToNamedAgent.*Progress' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

终态：finished。

Spec 符合度：已修复两条普通回复卡片化路径：stream 更新只显示最新状态行，不再把最终正文刷进任务卡；普通编号方案列表即使提到“用户选择”，也不会被 `detectChoices` 转成按钮卡。

安全检查：未新增外部输入执行、网络请求、凭据处理或权限放行路径；只调整文本识别和进度渲染。

测试与验证：新增回归测试覆盖普通方案列表不触发 choices、最终正文留在普通文本、stream 卡片只保留最新状态行。验证通过 `go test ./messaging`、`go test ./...`、`go vet ./...`、文档校验和 `git diff --check`。

复杂度检查：只新增一个小型正则和一个小型 helper，函数长度、文件大小和嵌套深度仍在约束内。

Document-refresh: not-needed
原因：本轮修复内部消息渲染误判，不改变用户配置格式、命令语法或公开文档契约。

剩余风险：只有明确包含“请选择 / 请回复 / 回复编号 / 输入编号 / 选择编号 / 选一个 / choose / select”的编号列表才会自动转按钮；更口语化的选择提示会按普通文本发送。

潜在技术债：`detectChoices` 仍是启发式识别；如后续需要更强交互，建议让 Agent 输出显式结构化 choice 协议。

结论：通过。
