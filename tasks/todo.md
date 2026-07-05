# 当前任务记录

## 目标

重做 WeClaw 的 Codex 权限三档：删除旧档位兼容，接入 Codex app-server `approvalsReviewer` 透传，让飞书审批、自动审查和全权限模式的语义与 Codex 官方权限模型一致。

## 执行任务

- [x] P1 串行：更新配置模型与权限档位映射。
- [x] P2 串行：把 `approvalsReviewer` 透传到 Codex `thread/start` 与 `turn/start`。
- [x] P3 串行：更新 `/mode` 文案、README 与 AI_CONTEXT。
- [x] P4 串行：补充测试并运行最小充分验证。

## 并行评估

本轮不启用 subagent。改动集中在 Codex 权限配置、ACP 参数和相关测试文案，同一批文件存在写冲突；串行执行更清晰。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./config ./agent ./messaging ./cmd -count=1 -timeout 60s
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

已将 Codex 权限档位重做为 `default`、`auto_review`、`full_access` 三档；省略 `permission_level` 时等同 `default`。旧档位 `request_approval`、`auto_approval`、`auto`、`ask` 会在配置校验阶段 fail-fast。新增 `approval_reviewer` 高级字段，并把 `approvalsReviewer` 透传到 Codex app-server 的 `thread/start`、`thread/resume` 和 `turn/start`。`/mode yolo` 文案已改为“本用户自动同意审批请求”，避免和全局 sandbox / reviewer 配置混淆。

验证命令：`GOCACHE=/private/tmp/weclaw-go-cache go test ./config -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./agent -run 'TestCodexAppServerUsesConfiguredApproval|TestAgentConfigDoesNotExist' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestMode|TestApprovalHandlerYolo|TestHelpText' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'TestCreateAgentByName|TestCompanionAutoLaunch' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./config ./agent ./messaging ./cmd -count=1 -timeout 60s`、`python3 scripts/validate_docs.py . --profile generic`、`git diff --check`，结果均通过。cmd 目标测试在沙箱内因本地监听权限失败，已按规则提权重跑并通过。
