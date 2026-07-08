# 当前任务记录

## 目标

支持通过本地命令把飞书用户加入 `admin_users`，解决首次未配置管理员时只能手改配置文件的问题。

## 执行任务

- [x] P0 串行：保留上一轮飞书 `admin_users` 只使用 `union_id` 的未提交改动。
- [x] P1 串行：补本地 `weclaw feishu users approve <union_id> --admin` 的失败测试。
- [x] P2 串行：复用飞书身份授权写配置逻辑，增加本地 CLI approve 子命令。
- [x] P3 串行：更新本地 CLI 与飞书内命令的帮助文案，避免继续提示 `open_id/user_id` 可加入管理员。
- [x] P4 串行：跑受影响测试、全量验证与 review-gate。

## 验证命令

```bash
go test ./cmd ./messaging -run 'TestRunFeishuUsers|TestFeishuIdentityCommand|TestServiceAdminCommand' -count=1 -timeout 120s
go test ./cmd ./messaging -count=1 -timeout 120s
go test ./... -count=1 -timeout 120s
go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

终态：finished。

Review 小结：新增本地 `weclaw feishu users approve <union_id|user_id|open_id> [--bot <name|app_id>] [--admin]`，复用飞书聊天命令的授权写配置逻辑。`--admin` 只写入 `union_id`，支持待确认、已发现和已授权身份后补管理员；飞书管理命令仍只按 `union_id` 判断。全量测试、vet、文档结构校验和 diff 检查已通过。
