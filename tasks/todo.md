# 当前任务记录

## 目标

移除飞书单聊和群聊回复串隔离能力，让“一个飞书机器人 = 一个项目入口”成为默认模型，降低 WeClaw 会话路由复杂度。

## 执行任务

- [x] P0 串行：创建备份 tag `pre-remove-feishu-thread-sessions-20260706`。
- [x] P1 串行：补充/调整失败测试，明确单聊 `/cx new-thread` 不再创建子会话，群聊不再按 root/thread 隔离。
- [x] P2 串行：移除飞书 `dm_thread` 与 group thread route 生成逻辑。
- [x] P3 串行：移除回复串发送路径和 `/cx new-thread` 内置命令。
- [x] P4 串行：清理帮助文案、README、上下文文档和过时任务说明。
- [x] P5 串行：运行最小充分验证并完成 review-gate。

## 并行评估

本轮不启用 subagent。改动集中在飞书会话路由、回复器和 Codex 会话命令，同一组文件存在写冲突，串行 TDD 更清晰。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./feishu -run 'TestToIncomingFromMessageDMNewThreadUsesDMSession|TestToIncomingFromMessageGroupIgnoresRootForSession|TestHandleMessageEventGroupReplyUsesFreshMessage|TestHandleMessageEventDMNewThreadReplyUsesFreshMessage' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestCodexNewThreadIsNotBuiltinSessionCommand|TestFeishuGroupStatusUsesChatSessionMetadataForRouting' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./feishu ./messaging -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

终态：finished。Spec 符合度：已按“单聊和群聊回复串隔离都移除”的范围完成；飞书 DM session key 固定为聊天 + 发送者，群聊 session key 固定为群聊，不再包含 root/thread；`/cx new-thread` 不再是内置 Codex 会话命令；飞书消息和卡片回调不再自动回复到原消息 / 话题。

安全检查：未引入密钥、外部输入执行、SQL/Shell 拼接或静默 fallback。复杂度检查：删除旧分支多于新增逻辑，核心路径更短；保留底层 Replier 原消息回复能力，避免扩大到无关底层 SDK 封装。

验证命令：`GOCACHE=/private/tmp/weclaw-go-cache go test ./feishu -run 'TestBuildFeishuSessionKey|TestToIncomingFromMessageDMNewThreadUsesDMSession|TestToIncomingFromMessageGroupIgnoresRootForSession|TestHandleMessageEventGroupReplyUsesFreshMessage|TestHandleMessageEventDMNewThreadReplyUsesFreshMessage|TestHandleMessageEventDMThreadReplyUsesFreshMessage|TestHandleCardActionEventGroupReplyUsesFreshMessage|TestHandleCardActionEventDMReplyUsesFreshMessage' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestCodexNewThreadIsNotBuiltinSessionCommand|TestFeishuGroupStatusUsesChatSessionMetadataForRouting|TestBuildCodexSessionHelpTextIncludesDescriptions|TestFeishuDMSessionWorkspaceSwitchStaysInChatSession' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./feishu ./messaging -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`python3 scripts/validate_docs.py . --profile generic`、`git diff --check`，结果均通过。全量测试因 sandbox 禁止本地 listener 曾失败，已用提权权限重跑通过。

剩余风险：历史状态里已经存在的 `dm_thread` route 不会被迁移；新入站消息不会再生成这些 route。若用户手动保留旧 route 数据，它只会作为普通 route 字符串存在，不再由飞书入站路径引用。
