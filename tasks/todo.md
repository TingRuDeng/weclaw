# 当前任务记录

## 2026-08-11 Codex 本地端与飞书协作接力

### 目标

让飞书与一个本地 Codex 端共同操作同一官方 Host、同一 thread：支持“飞书 + Codex App”或“飞书 + 受控 Codex CLI”两种配对，同步展示任务输出并允许双方继续输入；不再虚构排他控制权。

### 范围

- 每个受支持场景只承诺一个本地端与飞书配对，不要求 Codex App、CLI、飞书三端同时在线；三端意外同时打开同一 thread 时不宣称精确归属或排他。
- 两端必须复用同一个已验证官方 Host 和同一 thread；受控 CLI 继续固定连接官方 daemon，独立 `codex exec`、第二 app-server 和私有续跑任务不在范围内。
- 飞书选择活动会话后读取当前快照并继续同步输出；active turn 的普通输入统一使用 `turn/steer(expectedTurnId)`，空闲会话才开始新 turn。
- `/cx release` 只解除当前消息窗口的 durable binding、停止该窗口观察并冻结活动卡；不得 interrupt/stop active turn、重启 Host 或向飞书补发终态结果。
- `/stop` 仍停止同一 thread 的全局 active turn，文案必须明确同时影响本地端和飞书。
- WeClaw 重启保留 durable binding；平台投递通道恢复后重新挂接可恢复的活动观察，不能把“已选择但尚未重挂”伪装成已同步。
- 产品文案使用“绑定会话、同步任务、解除绑定”，不再使用“接管、归还控制权”。
- 全局取消 `admin_users` 二次提权；当前平台/机器人 `allowed_users` 是唯一远程可信身份来源，空列表继续默认拒绝，并保留私聊、确认、审计和运行时安全门禁。
- 保留当前工作区已有的飞书进度卡、文案和文档改动，不覆盖或回滚其内容。

### 验收标准

- [x] Codex App 配对与受控 CLI 配对都保持同一 `threadId` 和 Host generation；绑定、同步与解除绑定不调用 `turn/interrupt` 或 Host restart。
- [x] 飞书绑定活动会话后按当前 reducer/权威 thread 快照回填已有进度并持续接收后续输出；本地端同步看到 daemon 提供的同一 turn 输出。
- [x] 任一端在 active turn 期间的补充输入进入同一 `turnId`；空闲时才允许创建下一 turn，并发失败保留 daemon 的真实错误语义。
- [x] `/cx release` 后本地任务继续运行，飞书窗口不再收到进度、审批、问答或最终结果；重新绑定后从最新快照继续同步。
- [x] WeClaw 重启后 durable binding 不丢失，投递通道恢复时可重新挂接活动观察；恢复失败必须可观察且不得影响本地任务。
- [x] `/stop` 明确提示其全局影响，并只对当前绑定 thread 的 active turn 生效。
- [x] App/CLI 发起任务后的审批与结构化问答分别通过真实协议测试；不支持的上游路由必须明确提示，不能伪装为已同步。
- [x] 所有 `allowed_users` 身份具有相同管理能力，空名单仍全部拒绝，机器人账号之间不串权；旧 `admin_users` 只告警并忽略，不自动扩大名单。
- [ ] 定向单元测试、真实官方 daemon 集成测试、全仓发布门禁和 Codex App/CLI/飞书真机验收通过。

### 实施步骤

- [x] Phase 0：核对官方协议与当前实现，确认采用平等前端的协作接力模型，删除 controller lease、revision fencing 和本地只读要求。
- [x] Phase 1：测试驱动实现 `/cx release`、durable binding 解除、observer 非中断 detach、卡片冻结和终态投递隔离。
- [x] Phase 2：补齐绑定活动 App/CLI thread 的快照回填、后续输出同步与服务重启恢复语义。
- [x] Phase 3：验证并补齐 App/CLI 发起任务的审批、结构化问答、补充输入与全局停止行为，统一协作文案。
- [x] Phase 4：同步 `/cx status`、帮助、卡片和审计中的绑定/同步状态，不宣称精确本地 frontend 归属。
- [x] Phase 5：删除 `admin_users` 运行时权限模型，接入 Registry 授权 capability，并更新身份管理、CLI、Web 与 doctor。
- [ ] Phase 6：README、帮助、权威上下文、长期经验、独立复核和完整自动化验证已完成；Codex App/CLI/飞书真机验收待执行。

### 验证方式

```bash
go test ./agent ./messaging ./platform ./feishu ./cmd ./config ./web -count=1 -timeout 180s
go test ./... -count=1 -timeout 180s
go test -race ./... -count=1 -timeout 240s
go vet ./...
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

### Phase 0 结论

- 当前 Codex App follower 与官方 daemon 都支持围绕同一 thread 的状态同步和动作转发，但不提供客户端级排他控制权。
- 当前消息 binding 已是非 owner 模型，active turn 的远程普通输入也已通过 `turn/steer` 进入同一 turn；协作接力可以复用这些现有能力。
- 产品支持边界调整为“一个本地端 + 飞书”的同-thread协作；`/cx release` 表示解除飞书绑定，不表示释放 daemon 控制权。

### Review

- 已完成同 Host/thread 协作、活动 turn steer、durable follower 重挂、`/cx release` 非中断解除、全局 `/stop`、账号级授权 capability 及旧 `admin_users` 兼容告警。
- `scripts/release.sh` 的权威 `run_validations` 已完整通过：安装脚本 22 项、全仓测试、全仓 Race、Vet、Staticcheck、govulncheck、文档校验、module tidy 和差异检查均成功。
- 两轮独立源码复核覆盖解绑崩溃栅栏、恢复单飞、重绑、父子启动预检和账号撤权，最终未发现可复现 P0–P2。
- 尚未执行真实飞书网络、Codex App 与受控 CLI 的端到端真机验收，也未做真实进程 `kill -9` 故障注入；本轮未提交、推送、发布或更新 Jump 服务器。

## 2026-08-10 飞书无进度终态与等待文案精简

### 目标

让没有结构化进度的任务卡只保留简洁终态，同时移除耗时切换卡片暴露的内部实现术语；最终结果仍使用独立消息投递。

### 范围

- 飞书原生 stream 任务在没有结构化进度时，成功、失败和停止卡片只显示对应终态，不再显示“本任务未产生结构化进度记录”。
- 工作空间加载、会话或账号切换、其他耗时卡片操作分别使用简洁的加载、切换和处理中提示。
- 保留卡片中的已受理目标、超时指引、失败原因、最终结果消息、审批记录和既有幂等恢复语义。
- 同步相关测试、`tasks/lessons.md` 与 `docs/AI_CONTEXT.md`；不修改 README 和 provider 切换脚本。

### 验收标准

- [x] 无结构化进度的成功卡片正文只显示“已完成”，失败和停止卡片同样只显示对应终态。
- [x] 有结构化进度或审批记录的任务卡仍完整保留既有内容。
- [x] `/cx cd` 显示“正在加载中，请稍后……”，会话和 Codex 账号切换显示“正在切换中，请稍后……”，其他耗时操作显示“正在处理中，请稍后……”。
- [x] 超时、失败及独立最终结果投递行为不变。
- [x] 定向测试、文档校验和差异检查通过。

### 实施步骤

- [x] 先补充并运行失败测试，覆盖紧凑终态和三类等待文案。
- [x] 最小修改任务卡终态构建与切换等待文案。
- [x] 更新维护规则和上下文说明。
- [x] 运行受影响测试、文档校验和 `git diff --check`，复核最终差异。

### 验证方式

```bash
go test ./feishu ./messaging -count=1 -timeout 120s
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

### Review

- 无进度终态不再生成占位正文；成功、失败和停止只渲染对应状态，审批记录仍保留，最终结果继续独立投递。
- 工作空间加载、会话或账号切换和其他耗时操作分别使用简洁的用户文案，超时与失败指引未改动。
- 已通过全仓测试、`go vet ./...`、文档校验和差异检查；本轮未进行真实飞书桌面端或移动端验收。

## 2026-08-09 飞书 Codex 本地会话交接端侧验收

### 目标

验证当前正式版本中官方 daemon 多前端 Handoff、引导接力卡和进度折叠在真实飞书桌面端与移动端的表现。自动化测试与发布完成不替代端侧验收。

### 待验收

- [ ] 本地 Codex 已打开目标 thread 时，在飞书选择同一会话，确认绑定成功且运行通道可用。
- [ ] 从飞书发送普通消息或 `/guide`，确认输入进入当前 turn，不创建第二个 writer 或私有续跑任务。
- [ ] 连续发送多条成功引导，确认每条消息下各生成一张新接力卡，旧卡显示“已转移”并停止更新。
- [ ] 确认活动卡可手动折叠且普通更新不重置展开状态；完成、失败、停止和已转移卡自动折叠。
- [ ] 在飞书桌面端与移动端分别确认最新卡持续接收进度和唯一终态，最终结果消息不重复。

### 已完成边界

- [x] 官方 daemon 保持唯一 Host，Codex App、受控 CLI、飞书和微信复用同一组 thread。
- [x] 显式会话 Handoff 仅在官方 daemon 权威且 Desktop 返回 `no-client-found` 时允许释放并恢复；其他路径失败关闭。
- [x] 引导接力卡、折叠面板、终态收敛和重启恢复已实现并通过自动化验证。
- [x] 相关功能已进入当前正式版本并完成本机更新与重启。
