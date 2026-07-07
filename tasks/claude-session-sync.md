# Claude 会话本地删除同步计划

## 目标

Claude 会话列表和切换应能感知本机 `.claude` transcript 删除结果，尽量和 Codex 工作空间同步体验保持一致。

## 非目标

- 不删除用户本机 `.claude` transcript 文件。
- 不迁移 Claude 自身数据格式。
- 不新增 Claude App 或 CLI 的实时文件监听。
- 不改变 Codex 会话同步行为。

## 当前事实

- `messaging/claude_local_sessions.go` 的 `discoverLocalClaudeSessions` 每次读取 `.claude` 项目配置和 `projects/<encoded-workspace>/*.jsonl` transcript；本机文件被删除后，未缓存 session 会自然从本机列表消失。
- `messaging/claude_local_handler.go` 的 `claudeSwitchTargets` 会先加入 WeClaw 已记录 session，再追加本机会话；因此已缓存但本机删除的 session 仍可能继续显示。
- `messaging/claude_workspace_handler.go` 的 `handleClaudeSwitch` 对 stale session 没有预清理，只在 `UseClaudeSession` 失败时把错误返回给用户。
- `messaging/codex_browser_groups.go` 的 `codexSessionsForWorkspace` 已有 Codex stale thread 清理路径，会用可见 thread 集合调用 `clearStaleWorkspaceThread`。

## 决策日志

- 问题根因在 Claude 列表合并顺序：持久化记录优先于本地扫描结果，但没有用本地可见集合校正缓存。
- 有没有更优雅的方式：优先复用 `codexSessionStore.clearStaleWorkspaceThread`，而不是给 Claude 新写一套状态结构；Claude store 本身已经包装同一个底层 store。
- 同步应是按需同步，不做文件监听；触发点是 `/cc ls`、`/cc switch <编号>` 和后续可能的飞书卡片选择。

## 方案对比

### 方案 A：只过滤列表，不改持久化缓存

- 做法：`claudeSwitchTargets` 展示时跳过本机不可见的已缓存 session。
- 优点：改动最小。
- 缺点：缓存长期保留，`/cc switch <sessionId>` 仍可能命中旧缓存；状态查询仍可能显示旧 session。

### 方案 B：列表合并时清理 stale 缓存

- 做法：读取本机 Claude sessions 后，按 workspace 构建可见 session 集合，清理已缓存但不在本机可见集合里的 session，再合并列表。
- 优点：和 Codex stale 清理语义接近；后续按编号切换不会命中过期项；持久化状态也会收敛。
- 缺点：如果 Claude transcript 因权限或临时 IO 问题读不到，可能把缓存清掉；需要谨慎限定只在该 workspace 有明确本地扫描结果时清理。

### 方案 C：切换失败后再清理

- 做法：`UseClaudeSession` 失败时删除对应缓存。
- 优点：避免误删缓存。
- 缺点：用户仍会看到旧列表，体验不符合“同步”；失败路径依赖 agent 错误文本，不稳定。

## 推荐方案

采用方案 B，并保留失败暴露：在本地扫描可获得 workspace 视图时清理 stale 缓存；对无法确认的 workspace 不做静默删除。这样解决根因，同时避免用 fallback 掩盖真实失败。

## 执行计划

1. 测试先行：在 `messaging/handler_claude_session_test.go` 增加失败测试，覆盖已缓存 session 在本机 transcript 删除后不再出现在 `/cc ls`。
2. 测试先行：增加按编号切换测试，确认 stale 缓存被清理后编号对应本机真实可见 session。
3. 存储能力：在 `messaging/claude_sessions.go` 暴露 Claude 包装层的 stale 清理方法，内部复用 `codexSessionStore.clearStaleWorkspaceThread`。
4. 同步逻辑：在 `messaging/claude_local_handler.go` 的 `claudeSwitchTargets` 读取本机会话后，按 workspace 清理 stale 缓存，再合并存储记录和本机记录。
5. 边界处理：只对本机扫描明确出现过的 workspace 做 stale 清理；避免 `.claude` 配置缺失或目录不可读时误清理所有历史缓存。
6. 验证：跑 Claude 相关测试、messaging 包测试、全量测试、vet 和 diff 检查。

## 验证结果

- `GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestClaudeCcLsClearsStoredSessionMissingFromLocalWorkspace|TestClaudeCcLsClearsStoredSessionWhenLocalWorkspaceHasNoSessions|TestClaudeSwitchIndexSkipsStoredSessionMissingFromLocalWorkspace' -count=1 -timeout 60s`：通过。
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -count=1 -timeout 60s`：通过。
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`：通过。
- `GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`：通过。
- `python3 scripts/validate_docs.py . --profile generic`：通过。
- `git diff --check`：通过。

## 进度记录

- P0 已完成：用户已确认执行计划，可以进入 TDD 实现阶段。
- P1 已完成：新增 RED 测试，确认 `/cc ls` 会显示 stale 缓存 session，`/cc switch 0` 会命中过期 session。
- P2/P3 已完成：Claude 本机会话扫描现在返回可见 session 集合，列表合并前会复用底层 session store 清理 stale 缓存。
- P4 已完成：定向测试、messaging 包测试、全量测试、vet、文档校验和 diff 检查均通过。

## Review 小结

终态：finished。Spec 符合度：通过；方案 B 已实现，未扩大到实时监听或删除本机 `.claude` 文件。

安全检查：通过；未写入 secret，未新增 shell/SQL 拼接，失败不会被伪装成成功。

测试与验证：通过；验证命令见上方“验证结果”。

复杂度检查：通过；文件大小、函数长度和嵌套深度均在项目约束内。

Document-refresh: not-needed
原因：用户可见命令未变化，只修正已有 `/cc ls` 和 `/cc switch <编号>` 的状态同步。

剩余风险：直接输入未知 `/cc switch <sessionId>` 仍依赖 Claude agent 的失败返回，不在本轮同步范围内。

潜在技术债：Codex 与 Claude 的本地 session 同步逻辑仍是两套相近实现，未来可在不改变行为后抽公共 snapshot/visible-set helper。
