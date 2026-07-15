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
- [x] Task 8：补齐并发、重启、文档与全量验证。

## 当前状态

Claude “选择即接管”治理任务已完成：统一选择、新建、工作空间切换、飞书选择和 owner/CLI
交接入口，写入前后都校验唯一远程 owner 与 revision；持久化、runtime 或补偿失败时按
fail-closed 处理，用户回复不暴露底层错误。并发竞争、重启恢复、入口矩阵、失败补偿和只读
命令不改控制权均已有自动化测试。最终门禁覆盖全仓测试、messaging/agent 竞态检测、vet、
staticcheck、构建、文档校验与差异检查。
