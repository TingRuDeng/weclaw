# 当前任务记录

## 目标

优化 `weclaw feishu users pending/list` 输出，通过飞书通讯录接口把用户 ID 补全为可读姓名。

## 执行任务

- [x] P1 串行：补 `feishu users list` 输出联系人姓名和查询失败提示的 RED 测试。
- [x] P2 串行：暴露身份记录中的每机器人 open_id 映射，供联系人查询使用。
- [x] P3 串行：按机器人凭证调用飞书通讯录用户查询，并把姓名回填到 CLI 输出。
- [x] P4 串行：跑最小充分验证与 review-gate。

## 验证命令

```bash
go test ./cmd -run 'TestRunFeishuUsersListPrintsContact' -count=1 -timeout 60s
go test ./cmd -run 'TestRunFeishuUsers' -count=1 -timeout 60s
go test ./cmd -count=1 -timeout 120s
go test ./... -count=1 -timeout 120s
go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

终态：finished。

Review 小结：`weclaw feishu users pending/list` 现在会基于身份记录里的每机器人 `open_ids`，使用对应飞书机器人凭证调用通讯录用户查询，把输出首行补全为 `姓名 (union_id)`；查询失败时保留原始 ID 输出，并显式打印 `姓名查询失败: ...`，不静默吞掉权限或凭证问题。全量测试、vet、文档结构校验和 diff 检查已通过。
