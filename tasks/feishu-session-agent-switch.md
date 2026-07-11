# 飞书会话级 Agent 切换

## 目标

- 裸 `/cc`、`/cx` 等 Agent 命令只切换当前飞书会话的默认 Agent。
- 会话选择持久化，WeClaw 重启后继续生效。
- Agent 解析优先级为：会话选择、机器人配置、平台配置、全局配置。

## 非目标

- 不修改飞书机器人的公共 `default_agent` 配置。
- 不改变 `/cc <内容>` 等带内容命令的单次路由语义。
- 不调整 Codex 或 Claude 自身会话管理逻辑。

## 当前事实

- `messaging/default_session.go` 的 `switchDefault` 当前修改全局 `defaultName`。
- `messaging/progress_config.go` 的 `defaultAgentNameForAccount` 优先读取机器人级配置。
- 三个本机飞书机器人均配置为 `default_agent: codex`，因此裸 `/cc` 的全局切换会被覆盖。

## 决策日志

- 采用独立的 `agent-sessions.json`，以现有 `routeUserID` 作为会话键。
- 状态文件只保存会话键和 Agent 名称，使用 `0600` 权限与原子替换。
- 目标 Agent 启动或持久化失败时，不改变当前会话选择。
- `/model`、`/reasoning`、`/new` 同样读取当前会话选择，避免切换后仍操作机器人级 Agent。
- 串行执行；核心状态与路由文件存在强依赖，并行写入收益低且容易冲突。

## 执行计划

- [x] P1 串行：新增失败测试，覆盖存储恢复与飞书会话隔离。
- [x] P2 串行：实现会话 Agent 状态存储。
- [x] P3 串行：接入裸 Agent 命令、普通消息路由和启动加载。
- [x] P4 串行：执行完整验证与 Review Gate。

## 进度记录

- 2026-07-11：用户确认方案 A、功能边界、风险边界和执行计划。
- 2026-07-11：RED 测试因缺少状态存储与 Handler 接口按预期失败；状态存储最小测试随后通过。
- 2026-07-11：完成会话级路由、启动加载和失败边界；定向测试与 messaging 全包通过。
- 2026-07-11：验收前发现模型设置与 `/new` 仍读取机器人级 Agent；用户确认纳入本轮修复。

## 验证结果

- `GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`：通过。
- `GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`：通过。
- `python3 scripts/validate_docs.py . --profile generic`：通过。
- `git diff --check`：通过。

## Review 小结

- 终态：finished。
- Spec 符合度：通过；裸 Agent 命令只写当前会话，普通消息、模型设置和 `/new` 均读取会话级选择。
- 安全检查：通过；状态文件不含凭据，外部 JSON 校验版本与字段，权限为 `0600`，写入采用原子替换。
- 复杂度检查：通过；新增文件 152 行，现有受影响文件均低于 300 行，新增函数未超过 50 行。
- Document-refresh: needed。
- 原因：`README_CN.md` 的“切换默认 Agent 写入配置文件”已不符合新语义，但该文件存在用户未提交改动，按计划未修改。
- 剩余风险：状态文件暂不自动清理长期不再使用的会话键，数据只会按会话数量线性增长。
- 潜在技术债：旧的全局 `SaveDefaultFunc` 已不再由消息切换路径调用，为避免扩大 API 清理范围本轮保留。
- 结论：通过。
