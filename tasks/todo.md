# 当前任务记录

## 目标

清理已经完成的历史任务流水账和 legacy PR 文档，保留当前权威上下文、验证入口和长期 lessons。

## 执行任务

- [x] 串行：确认本地和远端分支状态，判断没有可删除分支。
- [x] 串行：删除已完成的单任务执行记录。
- [x] 串行：删除 legacy PR 文档，并同步上下文索引。
- [x] 串行：更新上下文验证脚本，避免继续要求 legacy 文档存在。
- [x] 串行：运行文档验证和 diff 检查。

## Review 小结

已删除已完成历史任务记录和 legacy PR 文档，`tasks/todo.md` 收缩为当前任务记录，`tasks/lessons.md` 作为长期经验沉淀保留。同步更新了 `docs/README.md`、`AGENTS.md` 和 `scripts/validate_docs.py`，避免文档索引继续引用已删除文件。验证命令：`python3 scripts/validate_docs.py . --profile generic`、`PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`、`git diff --check`，结果均通过。本次为文档清理，未运行 Go 测试。
