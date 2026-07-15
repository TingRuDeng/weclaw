# 当前任务记录

## 目标

为 Claude ACP session 增加唯一远程窗口所有权、选择即接管、显式本地释放和
fail-closed 任务门禁。

## 任务清单

- [x] Task 1：升级 Claude 状态模型并完成 v2 安全迁移。
- [x] Task 2：实现原子选择、释放和 session 有序锁。
- [ ] Task 3：实现统一选择接管 saga。
- [ ] Task 4：接入 switch、卡片、new 与全局 `/new`。
- [ ] Task 5：实现 owner 查询、远程接管与本地释放。
- [ ] Task 6：实现 `/cc cli` 本地交接与失败补偿。
- [ ] Task 7：增加普通消息、任务与配置写入门禁。
- [ ] Task 8：补齐并发、重启、文档与全量验证。

## 并行说明

用户选择多代理逐任务执行。每个 Task 必须先 RED、再实现并完成定向验证；
共享 session/control/runtime/active-task 状态的任务不并发修改同一文件。

## 当前状态

Task 2 已完成：原子选择与释放使用完整快照 CAS，写盘成功后才发布内存；
session 锁按去重排序后获取、共享单一等待预算并逆序释放。
