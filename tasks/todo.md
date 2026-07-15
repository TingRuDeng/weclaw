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
- [ ] Task 7：实现 `/cc cli` 本地交接与失败补偿。
- [ ] Task 8：补齐并发、重启、文档与全量验证。

## 并行说明

用户选择多代理逐任务执行。每个 Task 必须先 RED、再实现并完成定向验证；
共享 session/control/runtime/active-task 状态的任务不并发修改同一文件。

## 当前状态

Task 6 已完成：`/cc owner` 可脱敏展示控制方，`local` 复用统一 release，`remote`
复用统一 acquire；普通 Claude ACP 消息在任务登记前校验 remote owner 并记录 session/revision，
prompt 前再次复核，当前 session 的模型写入也执行相同门禁。状态、列表和帮助文本同步展示
所有权边界，并提示重新接管前先结束本地 Claude CLI。复核后已把相同的准入与 revision
复核扩展到 Claude 广播及其暂存续跑，并对不可准入 binding 状态、workspace、session 和
conversation 不一致执行 fail-closed。
