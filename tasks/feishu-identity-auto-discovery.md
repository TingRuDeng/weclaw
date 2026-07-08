# 飞书用户身份自动发现与授权确认

## 目标

- 支持多个飞书机器人场景下自动发现同一用户在不同应用下的身份。
- 未授权用户发消息时记录为待确认身份，并给出可操作的授权提示。
- 管理员确认后，把稳定身份写入配置，减少每个 bot 手工维护 `open_id` 的成本。
- 管理命令的 `admin_users` 支持 `union_id/user_id/open_id` 别名判断，避免顶层管理员在多 bot 下失效。

## 非目标

- 不允许陌生用户仅靠发消息自动获得 WeClaw 使用权限。
- 不调用飞书通讯录 OpenAPI 查询用户资料，避免新增权限和网络依赖。
- 不把 `app_secret` 或其它凭据写入身份状态文件。
- 不恢复飞书话题 / 回复串子会话模型。

## 当前事实

- `config.Config` 顶层只有 `AdminUsers`，飞书 bot 只有 `FeishuBotConfig.AllowedUsers`，没有全局身份簿。
- `cmd/start_platform.go:buildFeishuRegistryEntry` 直接用 `bot.AllowedUsers` 构造 `platform.AccessControl`。
- `platform/registry.go:guardedDispatch` 在进入 `messaging.Handler` 前校验白名单，未授权消息当前不会进入业务命令层。
- `feishu/incoming.go:feishuUserAliases` 已把 `open_id/user_id/union_id` 放入 `IncomingMessage.UserAliases`。
- `platform/registry.go:accessAllowsMessage` 已用 `IncomingMessage.UserIdentityKeys()` 进行别名授权判断。
- `messaging/admin_commands.go:isAdminUser` 当前只检查 `msg.UserID`，没有检查 `msg.UserAliases`。
- `cmd/start_config_reload.go:applySoftConfig` 已支持按飞书 `app_id` 热更新 bot 白名单。

## 决策日志

- 采用“自动发现，管理员确认授权”的方案。
- 发现记录写入独立状态文件 `~/.weclaw/feishu-identities.json`，避免高频发现数据污染主配置。
- 确认授权时写入主配置 `~/.weclaw/config.json`，优先写入 `union_id`；没有 `union_id` 时才写当前 bot 的 `open_id`。
- 默认授权范围为所有已配置飞书 bot；命令支持指定单个 bot，便于只开放某个项目入口。
- 管理员确认动作只允许已通过 `admin_users` 的用户执行。

## 方案对比

### 方案 A：只要求用户配置 `union_id`

- 优点：改动小，当前访问控制已经支持。
- 缺点：仍然要求用户自己找 ID、复制配置，不能解决“太麻烦”的体验问题。
- 结论：淘汰。

### 方案 B：身份簿 + 手动 CLI 维护

- 优点：结构清晰，安全边界明确。
- 缺点：仍需要用户主动输入身份，不能覆盖未授权消息被拒绝时的自动发现。
- 结论：作为方案 C 的基础能力保留。

### 方案 C：自动发现 + 管理员确认授权

- 优点：用户只需要让目标用户给任意 bot 发一条消息，管理员再确认；适合多 bot 场景。
- 缺点：需要新增状态文件、Registry 发现钩子、管理命令和配置写回。
- 结论：推荐并按此执行。

## 执行计划

- [x] P0 串行：新增身份状态模型与持久化
  - 文件：`messaging/feishu_identity_store.go`
  - 函数：`DefaultFeishuIdentityFile()`
  - 函数：`newFeishuIdentityStore()`
  - 函数：`(*feishuIdentityStore).SetFilePath(filePath string)`
  - 函数：`(*feishuIdentityStore).Remember(msg platform.IncomingMessage)`
  - 函数：`(*feishuIdentityStore).ListPending()`
  - 函数：`(*feishuIdentityStore).Approve(key string)`
  - 验证：新增 `messaging/feishu_identity_store_test.go`。

- [x] P1 串行：在 Registry 拒绝前记录飞书身份
  - 文件：`platform/registry.go`
  - 类型：新增 `IdentityObserver` 或等价回调接口。
  - 函数：`NewRegistry(entries []RegistryEntry, opts ...RegistryOption)`
  - 函数：`guardedDispatch(...)`
  - 行为：对所有飞书入站消息记录身份；拒绝前也记录，但不放行。
  - 验证：扩展 `platform/registry_test.go` 覆盖 denied message 仍触发发现。

- [x] P2 串行：启动时串联身份 store、Handler 和 Registry
  - 文件：`messaging/handler.go`
  - 字段：新增 `feishuIdentities *feishuIdentityStore`。
  - 文件：`messaging/session_stores.go`
  - 函数：`SetFeishuIdentityFile(filePath string)`
  - 文件：`cmd/start.go`
  - 行为：启动时创建默认身份文件路径，并把同一个 store 传给 Handler 与 Registry。
  - 文件：`cmd/start_platform.go`
  - 函数：`buildPlatformRegistry(...)` 需要接收身份观察器或 options。

- [x] P3 串行：实现管理员确认命令
  - 文件：`messaging/feishu_identity_commands.go`
  - 命令：`/feishu users pending`
  - 命令：`/feishu users approve <编号|union_id|open_id> [--bot <name|app_id>] [--admin]`
  - 命令：`/feishu users list`
  - 行为：确认后更新 `config.json` 的 `bots[].allowed_users`；带 `--admin` 时同步更新 `admin_users`。
  - 验证：新增 `messaging/feishu_identity_command_test.go`。

- [x] P4 串行：修复管理命令身份判断
  - 文件：`messaging/admin_commands.go`
  - 函数：`isAdminUser(userID string)` 改为按 `platform.IncomingMessage.UserIdentityKeys()` 判断。
  - 函数：`handleServiceAdminCommand(...)` 使用完整消息身份判断。
  - 验证：扩展 `messaging/admin_command_test.go` 覆盖 `admin_users` 配 `union_id` 时不同 bot 的 `open_id` 仍可执行管理命令。

- [x] P5 串行：确认授权后热更新运行时白名单
  - 文件：`messaging/feishu_identity_commands.go`
  - 方式：保存配置后提示“已写入配置，运行中服务会通过配置热重载生效”。
  - 文件：`cmd/start_config_reload.go`
  - 行为：复用现有配置 watcher 更新 `Registry.UpdateAccessForAccount`，不新增静默内存白名单。

- [x] P6 串行：CLI 辅助查看身份
  - 文件：`cmd/feishu.go`
  - 命令：`weclaw feishu users pending`
  - 命令：`weclaw feishu users list`
  - 行为：读取同一个身份状态文件，只做查看，不绕过管理员确认。
  - 验证：新增或扩展 `cmd/feishu_test.go`。

- [x] P7 串行：公开说明与回归验证
  - 文件：`README_CN.md`
  - 内容：新增自动发现与管理员确认授权的最小说明。
  - 验证：运行定向测试、全量测试、`go vet`、文档校验和 `git diff --check`。

## 验证矩阵

- `GOCACHE=/private/tmp/weclaw-go-cache go test ./platform ./messaging ./cmd -run 'Test.*FeishuIdentity|Test.*Admin.*Union|Test.*Registry.*Identity' -count=1 -timeout 60s`
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./platform ./messaging ./cmd -count=1 -timeout 60s`
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`
- `GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`
- `python3 scripts/validate_docs.py . --profile generic`
- `git diff --check`

## 并行执行说明

- 是否启用 subagent：暂不启用。
- 原因：核心改动会连续触碰 `platform.Registry`、`messaging.Handler`、配置写回和命令路由，写冲突明显；串行更容易保证授权边界。
- 可并行部分：验证阶段可并行跑 `platform`、`messaging`、`cmd` 定向测试，但最终由主流程统一汇总。

## 风险与预想失败场景

- 风险：发现记录如果直接放行，会扩大授权面。处理：发现只记录，确认才写配置。
- 风险：没有 `union_id` 的事件只能按当前 bot 的 `open_id` 授权。处理：命令输出明确提示该授权不具备跨 bot 稳定性。
- 风险：保存配置成功但热重载未触发。处理：命令回显配置路径和重载预期，测试覆盖配置写入；运行时仍可通过手动重启恢复。
- 风险：身份状态文件损坏。处理：读取失败只记录日志，不伪造空白成功；管理员命令应返回明确错误。

## 进度记录

- P0 已完成：新增飞书身份状态 store，支持发现记录、待确认列表、确认标记和状态文件持久化。
- P0 验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run TestFeishuIdentityStore -count=1 -timeout 60s` 通过。
- P1 已完成：`platform.Registry` 支持身份观察回调，飞书消息在访问控制前触发观察，拒绝逻辑不变。
- P1 验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./platform -run TestRegistryObservesDeniedFeishuIdentity -count=1 -timeout 60s` 通过。
- P2 已完成：`Handler` 持有同一身份 store，启动时把 `ObserveFeishuIdentity` 传入 Registry。
- P2 验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run TestHandlerObservesFeishuIdentity -count=1 -timeout 60s` 通过。
- P3 已完成：新增 `/feishu users pending/list/approve` 消息命令，确认授权后写入 `config.json`。
- P3 验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestFeishuIdentityCommand' -count=1 -timeout 60s` 通过。
- P4 已完成：管理命令改为按 `UserIdentityKeys()` 判断，`admin_users` 配 `union_id` 可跨 bot 生效。
- P4 验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run TestServiceAdminCommandAllowsFeishuUnionIDAlias -count=1 -timeout 60s` 通过。
- P5 已完成：确认授权只写 `config.json`，继续复用现有配置 watcher 热重载 `bots[].allowed_users`。
- P6 已完成：新增 `weclaw feishu users pending/list` 本地只读查看命令。
- P6 验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'TestRunFeishuUsers' -count=1 -timeout 60s` 通过。
- P7 已完成：更新 `README_CN.md` 与 `docs/AI_CONTEXT.md` 的飞书身份自动发现说明。

## 验证结果

- 通过：`GOCACHE=/private/tmp/weclaw-go-cache go test ./platform ./messaging ./cmd -run 'Test.*FeishuIdentity|Test.*Admin.*Union|Test.*Registry.*Identity|TestRunFeishuUsers|TestServiceAdminCommandAllowsFeishuUnionIDAlias' -count=1 -timeout 60s`
- 通过：`GOCACHE=/private/tmp/weclaw-go-cache go test ./platform ./messaging ./cmd -count=1 -timeout 60s`，首次沙箱运行因既有 `httptest` 本地端口监听被拒，提权后通过。
- 通过：`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`
- 通过：`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`
- 通过：`python3 scripts/validate_docs.py . --profile generic`
- 通过：`git diff --check`

## Review 小结

终态：finished。

Spec 符合度：通过。已实现飞书身份自动发现、待确认列表、管理员确认写配置、`admin_users` 按身份别名判断，以及本地 CLI 只读查看。

安全检查：通过。自动发现只写 `~/.weclaw/feishu-identities.json`，不会自动加入 `allowed_users`；确认授权必须由已在 `admin_users` 的用户触发；未写入 secret。

测试与验证：通过。新增 store、Registry、Handler、消息命令、CLI 和管理命令 alias 测试，并完成定向、受影响包、全量、vet、文档和 diff 检查。

复杂度检查：通过。新增文件均小于 300 行，函数保持小粒度拆分，未引入跨模块大重构。

Document-refresh: needed
原因：新增用户可见命令和飞书授权流程，已更新 `README_CN.md` 与 `docs/AI_CONTEXT.md`。

剩余风险：飞书事件缺少 `union_id` 时只能按当前 bot 的 `open_id` 授权，跨 bot 稳定性取决于飞书事件实际返回字段。

潜在技术债：身份状态文件目前是本地 JSON，后续如果多人并发管理或多实例共享，需要考虑集中存储或锁粒度增强。

结论：通过。
