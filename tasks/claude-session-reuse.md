# Claude 会话复用执行记录

## 目标

让 Claude 在微信侧获得接近 Codex 的 workspace/session 复用体验：可列出、切换、新建、查看状态，并可在本地 Terminal 接手当前会话。

## 非目标

- 不实现 Claude Companion 或 Remote Control server。
- 不读取或展示 Claude transcript 正文。
- 不改造 Codex 现有会话链路。

## 当前事实

- `agent/cli_agent.go:chatClaude` 已支持 `--resume <sessionID>` 的基础续接。
- `messaging/handler.go:handleCodexSessionCommand` 已提供 Codex 会话命令范式。
- `messaging/codex_local_sessions.go:discoverLocalCodexSessions` 已有“只读最小元数据”的本机会话发现模式。
- Claude 官方 CLI 支持 `--resume/-r`、`--continue/-c`、`--session-id` 与会话命名。

## 决策日志

- 采用方案 A：新增 Claude 专属会话层，先避免抽象改造 Codex。
- 本地 Claude 会话扫描只读取文件名、mtime、必要首行元数据，不暴露 transcript 正文。
- Claude 完整会话切换优先支持 CLI Agent；ACP Agent 仍保留基础 session 复用，不强行映射为 Claude Code 本机会话。

## 执行计划

- [x] P1 串行：补 Claude 本机会话扫描红灯测试。
- [x] P2 串行：补 Claude CLI 会话控制红灯测试。
- [x] P3 串行：补 `/cc` 会话命令红灯测试。
- [x] P4 串行：实现 Claude 本机会话扫描与存储。
- [x] P5 串行：实现 Claude CLI 会话控制接口。
- [x] P6 串行：实现 `/cc` 会话命令与本地 CLI 接手。
- [x] P7 串行：最小充分验证与 review-gate。
- [x] P8 串行：同步 README 中的 Claude 会话复用命令、边界和隐私说明。

## 进度记录

- 2026-06-12：用户确认按方案 A 推进，方案 B 待稳定后再优化。
- 2026-06-12：P1 红灯测试已补充，`go test ./messaging -run 'TestDiscoverLocalClaudeSessions' -count=1 -timeout 60s` 因缺少 `discoverLocalClaudeSessions` 与 `encodeClaudeProjectPath` 失败，符合预期。
- 2026-06-12：P2/P3 红灯测试已补充，`go test ./agent ./messaging -run 'TestCLIAgentClaudeSessionControl|TestClaudeCc|TestHandleClaude' -count=1 -timeout 60s` 因缺少 Claude 会话接口、命令处理与本地接手入口失败，符合预期。
- 2026-06-12：P4-P6 已实现并通过新增测试集合：`go test ./agent ./messaging -run 'TestCLIAgentClaudeSessionControl|TestDiscoverLocalClaudeSessions|TestClaudeCc|TestHandleClaude|TestHandleGlobalNewResetsActiveClaudeWorkspaceSession|TestHandleCwdRecordsActiveClaudeWorkspace' -count=1 -timeout 60s`。
- 2026-06-12：完成全量验证：`go test ./... -count=1 -timeout 60s` 通过；`git diff --check` 通过。
- 2026-06-12：P8 已同步 `README_CN.md` 和 `README.md`，补充 `/cc` 会话命令、Claude CLI/ACP 边界、本机 `~/.claude` 只读扫描说明，并修正 `README.md` 中已过期的默认进度模式描述。
- 2026-06-12：成熟产品审查后补齐 3 个优化点：启动时加载 Claude 会话持久化文件；自定义名称但命令为 Claude 的 CLI Agent 允许 session 控制；`/cc ls` 改为展示可切换会话，保证编号与 `/cc switch <编号>` 一致。

## 验证结果

- `go test ./agent ./messaging -run 'TestCLIAgentClaudeSessionControl|TestDiscoverLocalClaudeSessions|TestClaudeCc|TestHandleClaude|TestHandleGlobalNewResetsActiveClaudeWorkspaceSession|TestHandleCwdRecordsActiveClaudeWorkspace' -count=1 -timeout 60s`：通过。
- `go test ./... -count=1 -timeout 60s`：通过，文档同步后复跑仍通过。
- `git diff --check`：通过，文档同步后复跑仍通过。

## Review 小结

- Spec 符合：已按方案 A 新增 Claude 专属会话层，未抽象改造 Codex。
- 安全：Claude 本地 transcript 扫描只读项目配置、文件名、mtime 和首行摘要，不读取或展示完整正文。
- 复杂度：新增文件均小于 300 行，新增函数长度扫描未发现超过 50 行。
- 文档：已同步 README 里的 Claude 会话命令、CLI/ACP 边界和 transcript 隐私边界。
- 优化补丁：已补齐启动加载 Claude 会话持久化文件、自定义 Claude CLI Agent session 控制、`/cc ls` 编号与 `/cc switch` 目标一致性。
- 剩余风险：Claude ACP 仍只保留基础 session 复用，完整 `/cc switch` 体验依赖 Claude CLI Agent 实现 `ClaudeSessionAgent`；本轮未把 `~/.claude/transcripts` 无 workspace 归属的全局 transcript 强行纳入列表；60 秒全量测试会被 `config` 包真实命令探测耗尽超时。
