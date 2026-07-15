# 当前任务记录

## 目标

为 Claude ACP session 增加唯一远程窗口所有权、选择即接管、显式本地释放和
fail-closed 任务门禁。

## 任务清单

- [x] Task 1：升级 Claude 状态模型并完成 v2 安全迁移。
- [x] Task 2：实现原子选择、释放和 session 有序锁。
- [x] Task 3：实现统一选择接管 saga。
- [x] Task 4：收口工作空间切换与飞书入口。
- [x] Task 5：让 `/cc new` 与全局 `/new` 创建后原子接管。
- [x] Task 6：实现 owner 查询、远程接管与写入门禁。
- [x] Task 7：实现 `/cc cli` 本地交接与失败补偿。
- [ ] Task 8：补齐并发、重启、文档与全量验证。

## 并行说明

用户选择多代理逐任务执行。每个 Task 必须先 RED、再实现并完成定向验证；
共享 session/control/runtime/active-task 状态的任务不并发修改同一文件。

## 当前状态

Task 7 已完成：`/cc cli` 在 binding 锁内先复用统一 release，把 owner 持久化为 `local`
并清理 ACP runtime，再调用本地 opener；成功后保留最近 session 选择并拒绝普通远程任务。
opener 失败时从当前 binding 与 ACP 目录解析同一 session，复用统一 acquire 恢复 runtime 和
remote owner；目录、runtime 或持久化补偿失败时保持当前 route fail-closed，只返回固定的
“远程恢复未确认”脱敏提示。并发其他 route 已接管时不会被当前 route 的补偿覆盖。
