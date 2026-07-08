# 修复项目审查风险

## 目标

- 按审查发现顺序修复飞书附件授权顺序、身份审批稳定性、身份发现写入节流、工作区边界、飞书卡片口径和 Web token 失败策略。
- 每个行为变更先补失败测试，再实现最小代码。

## 非目标

- 不重构无关命令、发布脚本或 Agent 协议。
- 不删除用户现有配置字段。
- 不改变飞书审批卡片的既有权限申请体验。

## 当前事实

- `feishu/adapter_events.go:handleMessageEvent` 在进入平台 Registry 前调用 `toIncomingFromMessage`。
- `feishu/incoming.go:toIncomingFromMessage` 会在访问控制前下载飞书资源。
- `messaging/feishu_identity_commands.go:resolveFeishuIdentityApprovalRecord` 使用当前 pending 列表数字下标审批。
- `messaging/feishu_identity_store.go:Remember` 每次观察到身份都会保存状态文件。
- `messaging/handler_config.go:isWorkspaceAllowed` 在 `allowed_workspace_roots` 为空时放行所有目录。
- `messaging/reply_delivery.go:sendReplyWithMediaAfterStreamWithMetadata` 会把含选择提示的最终回复转为按钮卡片。
- `cmd/web.go:generateWebToken` 在随机数失败时回退固定 token。

## 决策日志

- 2026-07-08：优先修根因，不用静默 fallback；行为变更均补单测。

## 执行计划

- [x] P1 串行：飞书附件授权前置。
- [x] P2 串行：飞书身份审批改成稳定选择器，移除数字审批入口。
- [x] P3 串行：飞书身份发现按实质变化保存，并增加容量/过期控制。
- [x] P4 串行：远程 `/cwd` 默认拒绝空白名单下的任意目录切换。
- [x] P5 串行：飞书普通选择卡口径收敛，非审批最终回复不自动转卡片。
- [x] P6 串行：Web token 随机失败改成显式启动失败。
- [x] P7 串行：运行最小相关测试、全量测试、vet、diff 检查。

## 验证结果

- P1：`go test ./feishu -run TestHandleMessageEventDoesNotDownloadUnauthorizedAttachment -count=1 -timeout 60s` 通过。
- P1：`go test ./feishu -count=1 -timeout 120s` 通过。
- P2：`go test ./messaging -run 'TestFeishuIdentityCommand' -count=1 -timeout 60s` 通过。
- P2：`go test ./messaging -count=1 -timeout 120s` 通过。
- P3：`go test ./messaging -run 'TestFeishuIdentityStore(SkipsDuplicateSave|CapsDiscoveredRecords|PurgesStalePendingRecords)' -count=1 -timeout 60s` 通过。
- P3：`go test ./messaging -count=1 -timeout 120s` 通过。
- P4：`go test ./messaging -run 'TestCwdAllowlist|TestHandleCwdRecordsActiveClaudeWorkspace|TestHandleMessageKeepsFeishuSenderUserIDForWorkspaceCommands' -count=1 -timeout 60s` 通过。
- P4：`go test ./messaging -count=1 -timeout 120s` 通过。
- P4：`go test ./cmd -count=1 -timeout 120s` 通过。
- P5：`go test ./messaging -run 'TestSendReplyWithMediaKeepsChoiceLikeFinalReplyAsText|TestHandleMessageKeepsFeishuChoiceLikeFinalReplyAsText' -count=1 -timeout 60s` 通过。
- P5：`go test ./messaging -count=1 -timeout 120s` 通过。
- P6：`go test ./cmd -run TestGenerateWebTokenReturnsErrorWhenRandomFails -count=1 -timeout 60s` 通过。
- P6：`go test ./cmd -count=1 -timeout 120s` 通过。
- P7：`go test ./feishu ./platform ./messaging ./cmd ./web -count=1 -timeout 120s` 通过。
- P7：`go test ./... -count=1 -timeout 120s` 通过。
- P7：`go vet ./...` 通过。
- P7：`python3 scripts/validate_docs.py . --profile generic` 通过。
- P7：`git diff --check` 通过。

## Review 小结

- 已完成代码级复核；普通回复自动卡片化逻辑已删除，显式审批/help/Codex 导航卡片仍由 `AskChoices` 路径保留。
