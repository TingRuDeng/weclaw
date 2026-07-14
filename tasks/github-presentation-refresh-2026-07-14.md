# GitHub 项目展示刷新

## 目标

- 让 GitHub About 和 README 准确体现微信、飞书、Codex 与 Claude 远程接管能力。
- 统一中文、英文 README 的功能、命令和安装入口。
- 消除当前发行版、上游版本和废弃命令混用。
- 优化 GitHub 首屏的信息层级和实际渲染效果。

## 非目标

- 不修改 Go 业务代码、配置格式或命令行为。
- 不迁移 Go Module 路径。
- 不发布新的二进制版本。
- 不新增产品说明文档。
- 不制作虚构产品截图。

## 当前事实

- 当前维护和发布仓库是 `TingRuDeng/weclaw`，最新基线为 `v0.1.176`。
- 当前仓库是 `fastclaw-ai/weclaw` 的 fork，上游主分支和发行版不包含当前持续维护功能。
- GitHub About 仍只描述微信，Homepage 为空，Topics 为空。
- `README.md` 混入中文并保留旧 Codex Companion 和废弃命令。
- `README_CN.md` 未包含 `/cx owner` 控制权移交主路径。
- 现有四张 `previews/` 图片最后更新于 2026-03-23，未展示飞书、ACP 和显式控制权。
- 当前正式 Release 只发布 `darwin/arm64` 资产。

## 决策日志

- 采用重构现有 README 的方案，不拆分新的用户文档。
- `README_CN.md` 作为中文权威内容，`README.md` 作为完整英文镜像。
- 快速安装只展示当前仓库可验证的一键安装，不再混用上游 Go 和 GHCR 发行入口。
- 删除过时截图，用 GitHub 原生 Mermaid 展示架构；没有可公开真实截图时不添加替代假图。
- 当前仓库的 CI、Release、贡献者和 Star 链接指向 `TingRuDeng/weclaw`，上游只在致谢中说明。
- GitHub About 最后更新，确保 README 先完成提交和渲染验证。

## 执行计划

- [x] P0：建立任务记录并同步 `tasks/todo.md`。
- [x] P1：重构中文 README 的首屏、工作流、架构、命令、配置、安全和维护信息。
- [x] P2：按相同结构同步英文 README，清理所有中文混入。
- [x] P3：删除四张过时视觉资产及全部引用。
- [x] P4：执行文档、差异、命令、链接和双语结构验证。
- [x] P5：执行 Review Gate，修复计划内发现。
- [x] P6：提交、推送并检查 GitHub README 渲染。
- [x] P7：更新 GitHub About description 和 Topics，并回读确认。

## 验证结果

- `python3 scripts/validate_docs.py . --profile generic`：通过。
- `git diff --check`：通过。
- `go test ./messaging -run 'Test(BuildHelpText|BuildCodexSessionHelpTextIncludesDescriptions|RemovedCompatibilityRoutesAreNotBuiltinCommands|CodexOwner|HandleProgressCommand|ModelCommand|ReasoningCommand)' -count=1 -timeout 60s`：通过。
- CLI 帮助、GitHub Release 资产、关键链接、双语标题层级、Mermaid 和折叠区域检查：通过。
- GitHub `main`、README 内容、公开页面渲染和 About API 回读：通过。

## 进度记录

- 2026-07-14：现状、功能点、风险与执行计划已分段确认，HARD-GATE 已通过。
- 2026-07-14：P0 完成。
- 2026-07-14：P1 完成，中文 README 压缩为 257 行并清除已知漂移项。
- 2026-07-14：P2 完成，英文 README 与中文权威版本保持相同结构和事实。
- 2026-07-14：P3 完成，删除四张过时视觉资产并改用 Mermaid 架构图。
- 2026-07-14：P4 完成，文档、命令、链接、双语结构和 GitHub Markdown API 渲染验证通过。
- 2026-07-14：P5 完成，Review Gate 结论为 finished / 通过。
- 2026-07-14：P6 完成，提交 `066c597` 已推送，GitHub 公开页面显示新 README。
- 2026-07-14：P7 完成，About description、10 个 Topics 和空 Homepage 回读一致。

## Review 小结

- 终态：finished。
- Spec 符合度：通过。
- 安全检查：未发现硬编码凭据、私钥或用户数据。
- 测试与验证：最小充分验证全部通过。
- 复杂度检查：所有修改后的文本文件均低于 300 行。
- Document-refresh: not-needed；本轮已完成产品文档刷新且未修改代码。
- 剩余风险：无阻塞风险；GitHub README 与 About 已通过公开页面和 API 回读。
- 潜在技术债：当前没有可公开的真实产品截图，按决策暂不展示截图。
- 结论：通过。
