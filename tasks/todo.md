# 当前任务记录

## 目标

将飞书里的 `/model` 和 `/reasoning` 无参数查询优化为交互卡片，用户可直接选择模型或推理强度；点击后只收纳当前卡片，并明确提示新设置从下一个新会话生效。

## 非目标

- 不改变微信的文本命令体验。
- 不改变 `/model <模型ID>`、`/reasoning <强度>` 的直接命令入口。
- 不改变 Codex 模型与推理强度的持久化语义，仍只更新当前 WeClaw 运行时配置。
- 不允许选择 Codex 当前模型目录未声明支持的推理强度。

## 当前事实

- `messaging/model_command.go` 的 `renderModelOverview`、`renderReasoningOverview` 已能查询模型目录和当前模型支持的推理强度，但只返回文本。
- `messaging/platform_commands.go` 的 `handleBuiltInPlatformCommand` 直接把 `/model`、`/reasoning` 的结果发送为文本，没有使用 `platform.Replier.AskChoices`。
- `feishu/choice.go` 的 `buildChoiceCard` 与 `feishu/adapter_events.go` 的 `handleCardActionEvent` 已支持把卡片按钮回放为统一业务命令，并在点击后收纳原卡片。
- `messaging/platform_message.go` 的 `platformMessageText` 会把卡片 `choice` 还原为命令文本，因此按钮可直接携带 `/model <ID>`、`/reasoning <强度>`。

## 决策日志

- 采用现有 `AskChoices` 通道，不为模型设置新增第二套回调状态机。
- 飞书无参数命令优先发送卡片；显式带参数命令继续返回文本确认。
- 模型卡片选项来自 `ListCodexModels`；推理强度只来自当前模型的 `EffortOptions`。
- 当前值在按钮标签中标记为“当前”，点击后由现有回调收纳原卡片，业务层另行发送生效提示。
- 当模型目录查询失败或没有有效选项时返回现有文本说明，不伪造固定档位。
- 不使用 subagent；消息路由、卡片选项和测试共享同一行为边界，串行 TDD 可避免写冲突。

## 执行计划

- [x] P1 串行 RED：在 `messaging/model_command_test.go` 补模型卡片、推理强度卡片、当前值标记和无选项回退测试，并运行定向测试确认按预期失败。
- [x] P2 串行 GREEN：在 `messaging/model_command.go` 增加模型/推理强度卡片选项构造函数；在 `messaging/platform_commands.go` 让飞书无参数命令调用 `AskChoices`，带参数命令保持原路径。
- [x] P3 串行 RED/GREEN：在 `feishu/choice_selected_card_test.go` 覆盖设置卡片点击后的收纳结果，确认选项命令通过现有 `choice` 回放，不新增独立设置状态。
- [x] P4 串行验证：执行格式化、messaging 与 feishu 定向测试、全仓测试、race、vet、文档契约、`git diff --check` 和 review gate。

## 验证矩阵

- 飞书 `/model`：展示当前模型和动态模型按钮，当前项有明确标记。
- 飞书 `/reasoning`：只展示当前模型支持的档位，当前项有明确标记。
- 飞书按钮点击：回放现有带参数命令，收纳被点击卡片，并发送“下一个新会话生效”的确认。
- 微信及无按钮能力平台：继续收到文本概览。
- 模型目录查询失败或选项为空：返回文本概览，不发送空卡片。
- 非 Codex Agent：继续返回“由配置固定”的现有提示。
- 回归命令：`go test ./messaging ./feishu -count=1 -timeout 60s`。
- 全量命令：`go test -race ./messaging ./feishu -count=1 -timeout 60s`、`go test ./... -count=1 -timeout 120s`、`go vet ./...`、`python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

## 进度记录

- 2026-07-11：用户确认 `/model` 与 `/reasoning` 一起卡片化；已完成只读代码分析和实施规划。
- 2026-07-11：P1 完成；飞书 `/model`、`/reasoning` 卡片测试均因当前实现只发送文本而按预期失败，空模型列表回退测试通过。
- 2026-07-11：P2 完成；飞书无参数 `/model`、`/reasoning` 使用动态卡片，当前项有标记，空选项回退文本；带参数命令与微信保持文本。
- 2026-07-11：P3 完成；飞书点击推理强度后收纳原卡片，并将完整 `/reasoning high` 命令回放到统一业务层；无需新增飞书回调状态。
- 2026-07-11：P4 完成；修正显式模型未命中目录时借用其他模型推理档位的边界，并完成最终 race、全仓测试、vet、文档契约和差异检查。

## Review 小结

终态：finished。

Spec 符合度：通过。飞书无参数 `/model`、`/reasoning` 使用动态选择卡片，当前值明确标记；点击后复用统一命令路由并只收纳已点击卡片；微信和显式带参数命令保持文本。

安全检查：模型和推理强度选项只来自 Codex `model/list`；显式模型未命中目录时不借用其他模型档位；未新增权限入口、外部命令拼接或凭据处理。

测试与验证：TDD 主路径和未知模型边界均先失败后通过；最终 `go test -race ./messaging ./feishu -count=1 -timeout 60s`、`go test ./... -count=1 -timeout 120s`、`go vet ./...`、文档契约和 `git diff --check` 均通过。

复杂度检查：`handleBuiltInPlatformCommand` 已提取 Codex 会话和模型设置职责，降至 49 行；所有修改文件少于 300 行，新增函数参数不超过 3 个、嵌套不超过 3 层。

Document-refresh: not-needed

原因：命令名称和配置结构未变化，只把飞书已有文本查询升级为同语义的交互卡片，帮助文案无需调整。

剩余风险：未显式配置模型时沿用 Codex 模型目录首项作为默认模型档位来源，依赖 Codex `model/list` 保持默认模型优先排序。

潜在技术债：`CodexModel` 尚未解析 app-server 的显式默认模型标记；若未来模型目录不再默认项优先，需要补充该字段以消除排序依赖。

结论：通过。
