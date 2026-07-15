# Codex 远程会话选择即接管 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让微信或飞书窗口在明确选择 Codex thread 时原子取得远程控制权、释放本窗口原有 thread，并在 Desktop 活跃任务上立即获得进度回传、`/guide` 与 `/stop` 能力。

**Architecture:** 保留 `agent.CodexLiveRuntimeAgent` 作为实际 writer/runtime 权威边界，在 Messaging 层新增一条要求外层已持有 binding 锁的 saga。事务先按 thread ID 排序持锁、执行并可逆记录 runtime handoff，再预留活动任务观察，最后以 copy-on-write 方式一次持久化 workspace/thread 与全部 control intents；失败按逆序补偿，结果不确定时保持 fail-closed。

**Tech Stack:** Go 1.26.5、标准库 `context/sync/sort/errors/encoding/json`、现有 Codex app-server/ACP 接口、Go `testing` 与 race detector；不新增第三方依赖。

## Global Constraints

- 权威设计：`docs/superpowers/specs/2026-07-15-codex-remote-selection-ownership-design.md`。
- 所有用户可见文本、代码注释、任务文件和 Git Commit 使用简体中文；`README.md` 保持其既有英文语言，且必须与 `README_CN.md` 语义一致。
- 核心状态链路串行实施；不得让多个执行者同时修改 session/control/runtime/active-task 相关文件。
- `/cx` 命令与全局 `/new` 的调用方已经持有 binding 锁；新事务不得重复获取该锁。
- 多 thread 锁必须去重、按 thread ID 升序获取、逆序释放，并共享一次 5 秒等待预算。
- 目标由其他 `RouteBindingKey` 控制时拒绝；不得泄露对方 route、conversation 或用户身份。
- 选择成功必须同时满足：目标选择已持久化、目标属于当前 route、同 route 非目标 thread 已释放、runtime 已确认；目标活动时观察槽已预留。
- Desktop 活跃任务保持 `CodexRuntimeDesktop`，不得调用 `UseCodexThread` 或迁移到 WeClaw app-server。
- `/cx owner desktop` 只显式释放，普通消息不得静默重新接管。
- 不引入静默 fallback、模拟成功、空闲超时释放或新的状态文件；继续使用 `codex-sessions.json` version 2。
- 单函数不超过 50 行、单文件不超过 300 行、嵌套不超过 3 层、位置参数不超过 3 个、圈复杂度不超过 10。
- 逻辑修改测试先行；定向测试与 race 测试均使用 60 秒硬超时。

---

## 文件与职责地图

### 新建文件

- `messaging/codex_remote_selection_store.go`：只负责 remote selection 快照、校验、copy-on-write 原子提交和幂等判断。
- `messaging/codex_remote_selection_store_test.go`：覆盖单次写入、全部旧所有权释放、CAS、并发、持久化失败和重启恢复。
- `messaging/codex_session_locks.go`：负责多 thread 排序锁和统一等待预算。
- `messaging/codex_session_locks_test.go`：覆盖 ABBA、部分获取失败释放和超时后锁可复用。
- `messaging/codex_external_task_reservation.go`：把活动任务观察拆成“解析、预留、激活、取消”四阶段，形成成功屏障。
- `messaging/codex_session_acquire.go`：统一事务编排、校验、store 提交与成功结果。
- `messaging/codex_session_acquire_runtime.go`：runtime handoff 日志、逆序补偿、超时后只读 resync 和 fail-closed 错误分类。
- `messaging/codex_session_acquire_test.go`：覆盖 A→B、冲突、幂等、释放集合、补偿与不确定结果。
- `messaging/codex_session_new.go`：统一 `/cx new` 与默认 Agent 为 Codex 时的全局 `/new` 创建后接管、失败恢复。

### 修改文件

- `messaging/agent_conversation.go`：ACP 只为空 session store 回填 thread。
- `messaging/codex_session_persistence.go`、`messaging/codex_sessions.go`：提供候选状态写入器与深拷贝，不改变 JSON schema。
- `messaging/codex_session_command.go`：复用多 thread 锁，保留 5 秒统一预算。
- `messaging/codex_live_fakes_test.go`：按 thread 记录 inspect/handoff，支持精确故障注入。
- `messaging/codex_external_task.go`：旧入口改为调用观察预留接口；成功提示加入 `/stop`。
- `messaging/codex_session_switch.go`：只保留目标解析、统一事务调用和结果渲染；删除“先选择、后探测”的旧路径。
- `messaging/codex_browser.go`：唯一会话通过统一事务；零/多会话只导航且不改变所有权。
- `messaging/codex_feishu_cards.go`：唯一会话自动接管后不再发送重复的单会话选择卡。
- `messaging/codex_session_command_dispatch.go`：为 `/cx new` 透传平台、reply 与真实 actor 上下文。
- `messaging/default_session.go`：全局 `/new` 的 Codex 分支复用创建后接管事务。
- `messaging/codex_owner_command.go`：`owner remote` 复用统一事务；`owner desktop` 保留显式释放路径。
- `messaging/codex_runtime_binding.go`：释放后的普通消息提示改为“重新选择或显式 owner remote”，不自动接管。
- 既有相关测试：改写固化旧两阶段语义的断言，并增加入口/路由行为覆盖。
- `README_CN.md`、`README.md`、`docs/AI_CONTEXT.md`、`tasks/lessons.md`、`tasks/todo.md`：同步产品语义、状态流、经验和执行记录。

## Task 1: 阻止 stale ACP 覆盖显式选择

**Files:**
- Modify: `messaging/agent_conversation.go:202-215`
- Test: `messaging/handler_codex_thread_state_test.go`

**Interfaces:**
- Consumes: `codexSessionStore.getThread(bindingKey, workspaceRoot) (string, bool)`。
- Produces: `syncCodexThreadFromAgent` 仅在 `threadID == "" && !pending` 时回填。

- [ ] **Step 1: 写入失败回归测试**

在 `messaging/handler_codex_thread_state_test.go` 增加：

```go
func TestSyncCodexThreadFromAgentDoesNotOverwriteExplicitSelection(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	bindingKey := codexBindingKey("user-1", "codex")
	h.ensureCodexSessions().setThread(bindingKey, workspace, "thread-selected")
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		threadID: "thread-stale",
	}

	h.syncCodexThreadFromAgent("user-1", "codex", workspace, ag)

	threadID, pending := h.ensureCodexSessions().getThread(bindingKey, workspace)
	if pending || threadID != "thread-selected" {
		t.Fatalf("thread=%q pending=%v，显式选择不应被 ACP 旧映射覆盖", threadID, pending)
	}
}

func TestSyncCodexThreadFromAgentBackfillsEmptySelection(t *testing.T) {
	h := NewHandler(nil, nil)
	workspace := t.TempDir()
	ag := &fakeCodexThreadAgent{
		fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}},
		threadID: "thread-restored",
	}

	h.syncCodexThreadFromAgent("user-1", "codex", workspace, ag)

	threadID, pending := h.ensureCodexSessions().getThread(codexBindingKey("user-1", "codex"), workspace)
	if pending || threadID != "thread-restored" {
		t.Fatalf("thread=%q pending=%v，空状态应允许 ACP 回填", threadID, pending)
	}
}
```

- [ ] **Step 2: 运行测试确认旧实现失败**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestSyncCodexThreadFromAgent' -count=1 -timeout 60s`

Expected: `TestSyncCodexThreadFromAgentDoesNotOverwriteExplicitSelection` FAIL，实际 thread 为 `thread-stale`。

- [ ] **Step 3: 最小实现显式选择优先**

将 `syncCodexThreadFromAgent` 中的前置判断替换为：

```go
	store := h.ensureCodexSessions()
	threadID, pending := store.getThread(bindingKey, workspaceRoot)
	if pending || strings.TrimSpace(threadID) != "" {
		return
	}
```

其余 ACP `CurrentCodexThread` 回填逻辑保持不变。

- [ ] **Step 4: 运行定向测试**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestSyncCodexThreadFromAgent|TestRecordCodexThread' -count=1 -timeout 60s`

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add messaging/agent_conversation.go messaging/handler_codex_thread_state_test.go
git commit -m "修复显式 Codex 会话被旧映射覆盖"
```

## Task 2: 增加选择与所有权的单次原子提交

**Files:**
- Create: `messaging/codex_remote_selection_store.go`
- Create: `messaging/codex_remote_selection_store_test.go`
- Modify: `messaging/codex_sessions.go:10-45`
- Modify: `messaging/codex_session_persistence.go:103-172`

**Interfaces:**
- Consumes: `codexSessionBinding`、`codexControlIntent`、`writeCodexSessionStateFile`。
- Produces:
  - `remoteSelectionSnapshot(bindingKey, targetThreadID string) codexRemoteSelectionSnapshot`
  - `commitRemoteSelection(update codexRemoteSelectionUpdate) (codexRemoteSelectionResult, error)`
  - `codexRemoteSelectionThreadIDs(snapshot codexRemoteSelectionSnapshot) []string`

- [ ] **Step 1: 写原子交换、幂等、CAS、失败回滚与重启测试**

在新测试文件中使用以下表和断言；每个 case 都必须同时核对 binding、目标 intent、旧 intent 和写入次数：

```go
func TestCodexRemoteSelectionCommitSelectsTargetAndReleasesRouteOwnedThreads(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "codex-sessions.json")
	store := newCodexSessionStore()
	store.SetFilePath(stateFile)
	bindingKey := "feishu:window-a\x00codex"
	workspaceA, workspaceB := "/workspace/a", "/workspace/b"
	store.setActiveWorkspace(bindingKey, workspaceA)
	store.setThread(bindingKey, workspaceA, "thread-a")
	store.setThread(bindingKey, workspaceB, "thread-b")
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{
		threadID: "thread-a", owner: codexControlRemote,
		bindingKey: bindingKey, conversationID: "conversation-a",
	})
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{
		threadID: "thread-c", owner: codexControlRemote,
		bindingKey: bindingKey, conversationID: "conversation-c",
	})
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{
		threadID: "thread-b", owner: codexControlDesktop,
	})

	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-b")
	result, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: workspaceB, TargetThreadID: "thread-b",
		ConversationID: "conversation-b", Expected: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Target.Owner != codexControlRemote || result.Target.RouteBindingKey != bindingKey {
		t.Fatalf("target=%#v", result.Target)
	}
	for _, threadID := range []string{"thread-a", "thread-c"} {
		if got := store.controlIntent(threadID); got.Owner != codexControlDesktop {
			t.Fatalf("%s intent=%#v，旧所有权应释放", threadID, got)
		}
	}
	threadID, pending := store.getThread(bindingKey, workspaceB)
	if pending || threadID != "thread-b" {
		t.Fatalf("thread=%q pending=%v", threadID, pending)
	}
	if active, _ := store.getActiveWorkspace(bindingKey); active != workspaceB {
		t.Fatalf("active=%q", active)
	}
	reloaded := newCodexSessionStore()
	reloaded.SetFilePath(stateFile)
	if got := reloaded.controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("reloaded target=%#v", got)
	}
}

func TestCodexRemoteSelectionCommitKeepsLiveStateWhenWriteFails(t *testing.T) {
	store := newCodexSessionStore()
	store.SetFilePath(filepath.Join(t.TempDir(), "state.json"))
	bindingKey := "route-a\x00codex"
	store.setThread(bindingKey, "/workspace/a", "thread-a")
	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-b")
	store.writeState = func(string, []byte) error { return errors.New("写入失败") }

	_, err := store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b",
		ConversationID: "conversation-b", Expected: snapshot,
	})
	if err == nil {
		t.Fatal("候选状态写入失败时不应提交内存")
	}
	threadID, _ := store.getThread(bindingKey, "/workspace/a")
	if threadID != "thread-a" || store.controlIntent("thread-b").Owner != codexControlUnclaimed {
		t.Fatalf("thread=%q target=%#v", threadID, store.controlIntent("thread-b"))
	}
}

func TestCodexRemoteSelectionCommitRejectsStaleSnapshot(t *testing.T) {
	store := newCodexSessionStore()
	bindingKey := "route-a\x00codex"
	claimCodexStoreIntent(t, store, codexStoreIntentFixture{
		threadID: "thread-b", owner: codexControlDesktop,
	})
	snapshot := store.remoteSelectionSnapshot(bindingKey, "thread-b")
	current := store.controlIntent("thread-b")
	_, err := store.updateControlIntent(codexControlIntentUpdate{
		ThreadID: "thread-b", Owner: codexControlRemote,
		RouteBindingKey: bindingKey, ConversationID: "conversation-a",
		ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: bindingKey, WorkspaceRoot: "/workspace/b", TargetThreadID: "thread-b",
		ConversationID: "conversation-a", Expected: snapshot,
	})
	if !errors.Is(err, errCodexRemoteSelectionChanged) {
		t.Fatalf("error=%v", err)
	}
}
```

同文件补齐：

```go
type codexStoreIntentFixture struct {
	threadID      string
	owner         codexControlOwner
	bindingKey    string
	conversationID string
}

func claimCodexStoreIntent(t *testing.T, store *codexSessionStore, fixture codexStoreIntentFixture) {
	t.Helper()
	current := store.controlIntent(fixture.threadID)
	_, err := store.updateControlIntent(codexControlIntentUpdate{
		ThreadID: fixture.threadID, Owner: fixture.owner,
		RouteBindingKey: fixture.bindingKey, ConversationID: fixture.conversationID,
		ExpectedRevision: current.Revision,
	})
	if err != nil {
		t.Fatalf("建立测试控制意图失败: %v", err)
	}
}
```

并增加：

- `TestCodexRemoteSelectionCommitIsIdempotent`：第二次提交不增加 revision、不调用 writer。
- `TestCodexRemoteSelectionCommitRejectsOtherRoute`：目标为其他 route 时零变化。
- `TestCodexRemoteSelectionCommitRejectsOwnedSetChange`：快照后新增同 route 的 C 时零变化。
- `TestCodexRemoteSelectionConcurrentClaimHasSingleWinner`：两个 binding 并发争用 B，仅一个成功。

- [ ] **Step 2: 运行测试确认接口尚不存在**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestCodexRemoteSelection' -count=1 -timeout 60s`

Expected: FAIL，提示 `remoteSelectionSnapshot` 或 `commitRemoteSelection` 未定义。

- [ ] **Step 3: 增加候选状态写入器**

在 `codex_sessions.go` 中加入并初始化：

```go
type codexSessionStateWriter func(filePath string, data []byte) error

type codexSessionStore struct {
	mu         sync.Mutex
	saveMu     sync.Mutex
	filePath   string
	bindings   map[string]codexSessionBinding
	controls   map[string]codexControlIntent
	writeState codexSessionStateWriter
}

func newCodexSessionStore() *codexSessionStore {
	return &codexSessionStore{
		bindings: make(map[string]codexSessionBinding),
		controls: make(map[string]codexControlIntent),
		writeState: writeCodexSessionStateFile,
	}
}
```

在 `codex_session_persistence.go` 增加以下完整边界，并让现有 `persistStateLocked` 复用它：

```go
func (s *codexSessionStore) persistCandidate(filePath string, state codexSessionState) error {
	if filePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return fmt.Errorf("创建状态目录: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("编码状态: %w", err)
	}
	writer := s.writeState
	if writer == nil {
		writer = writeCodexSessionStateFile
	}
	return writer(filePath, data)
}
```

- [ ] **Step 4: 实现 remote selection 快照与 copy-on-write 提交**

新文件必须定义以下类型；`commitRemoteSelection` 在持有 `saveMu -> mu` 时校验快照、构造候选 maps、写盘成功后再替换 live maps：

```go
var (
	errCodexRemoteSelectionChanged    = errors.New("Codex 会话选择状态已变化")
	errCodexRemoteSelectionOtherRoute = errors.New("Codex 会话由其他远程窗口控制")
)

type codexRemoteSelectionSnapshot struct {
	TargetThreadID string
	Binding        codexSessionBinding
	Target         codexControlIntent
	RouteOwned     map[string]codexControlIntent
}

type codexRemoteSelectionState struct {
	bindings map[string]codexSessionBinding
	controls map[string]codexControlIntent
}

type codexRemoteSelectionUpdate struct {
	BindingKey     string
	WorkspaceRoot  string
	TargetThreadID string
	ConversationID string
	Expected       codexRemoteSelectionSnapshot
}

type codexRemoteSelectionResult struct {
	Target   codexControlIntent
	Released map[string]codexControlIntent
}

func (s *codexSessionStore) remoteSelectionSnapshot(bindingKey string, targetThreadID string) codexRemoteSelectionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return remoteSelectionSnapshotLocked(codexRemoteSelectionState{
		bindings: s.bindings, controls: s.controls,
	}, bindingKey, targetThreadID)
}

func codexRemoteSelectionThreadIDs(snapshot codexRemoteSelectionSnapshot) []string {
	ids := make([]string, 0, len(snapshot.RouteOwned)+1)
	for threadID := range snapshot.RouteOwned {
		ids = append(ids, threadID)
	}
	if target := strings.TrimSpace(snapshot.TargetThreadID); target != "" {
		ids = append(ids, target)
	}
	return sortedUniqueCodexThreadIDs(ids)
}
```

快照和提交使用以下完整边界，避免在主方法中混入深拷贝与 CAS 细节：

```go
func (s *codexSessionStore) commitRemoteSelection(update codexRemoteSelectionUpdate) (codexRemoteSelectionResult, error) {
	update = normalizeCodexRemoteSelectionUpdate(update)
	if err := validateCodexRemoteSelectionUpdate(update); err != nil {
		return codexRemoteSelectionResult{}, err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := codexRemoteSelectionState{bindings: s.bindings, controls: s.controls}
	if err := validateCodexRemoteSelectionSnapshotLocked(current, update); err != nil {
		return codexRemoteSelectionResult{}, err
	}
	nextBindings := cloneCodexSessionBindings(s.bindings)
	nextControls := cloneCodexControlIntents(s.controls)
	now := time.Now().UTC()
	candidate := codexRemoteSelectionState{bindings: nextBindings, controls: nextControls}
	result, changed := applyCodexRemoteSelection(candidate, update, now)
	if !changed {
		return result, nil
	}
	state := codexSessionState{
		Version: codexSessionStateVersion, Bindings: nextBindings, Controls: nextControls,
		Updated: now.Format(time.RFC3339),
	}
	if err := s.persistCandidate(s.filePath, state); err != nil {
		return codexRemoteSelectionResult{}, fmt.Errorf("保存 Codex 会话选择: %w", err)
	}
	s.bindings, s.controls = nextBindings, nextControls
	return result, nil
}
```

同文件加入以下 helper 实现：

```go
func normalizeCodexRemoteSelectionUpdate(update codexRemoteSelectionUpdate) codexRemoteSelectionUpdate {
	update.BindingKey = strings.TrimSpace(update.BindingKey)
	update.WorkspaceRoot = normalizeCodexWorkspaceRoot(update.WorkspaceRoot)
	update.TargetThreadID = strings.TrimSpace(update.TargetThreadID)
	update.ConversationID = strings.TrimSpace(update.ConversationID)
	return update
}

func validateCodexRemoteSelectionUpdate(update codexRemoteSelectionUpdate) error {
	if update.BindingKey == "" || update.WorkspaceRoot == "" ||
		update.TargetThreadID == "" || update.ConversationID == "" {
		return fmt.Errorf("Codex 选择提交缺少必要路由字段")
	}
	if update.Expected.TargetThreadID != update.TargetThreadID {
		return errCodexRemoteSelectionChanged
	}
	return nil
}

func validateCodexRemoteSelectionSnapshotLocked(state codexRemoteSelectionState, update codexRemoteSelectionUpdate) error {
	current := remoteSelectionSnapshotLocked(state, update.BindingKey, update.TargetThreadID)
	if current.Target.Owner == codexControlRemote && current.Target.RouteBindingKey != update.BindingKey {
		return errCodexRemoteSelectionOtherRoute
	}
	if !sameCodexRemoteSelectionSnapshot(current, update.Expected) {
		return errCodexRemoteSelectionChanged
	}
	return nil
}

func sameCodexRemoteSelectionSnapshot(left codexRemoteSelectionSnapshot, right codexRemoteSelectionSnapshot) bool {
	return left.TargetThreadID == right.TargetThreadID &&
		sameCodexSessionBinding(left.Binding, right.Binding) &&
		left.Target == right.Target &&
		sameCodexControlIntentMap(left.RouteOwned, right.RouteOwned)
}

func cloneCodexSessionBindings(source map[string]codexSessionBinding) map[string]codexSessionBinding {
	cloned := make(map[string]codexSessionBinding, len(source))
	for key, binding := range source {
		workspaces := make(map[string]codexWorkspaceSession, len(binding.Workspaces))
		for workspaceRoot, session := range binding.Workspaces {
			workspaces[workspaceRoot] = session
		}
		cloned[key] = codexSessionBinding{ActiveWorkspace: binding.ActiveWorkspace, Workspaces: workspaces}
	}
	return cloned
}

func cloneCodexControlIntents(source map[string]codexControlIntent) map[string]codexControlIntent {
	cloned := make(map[string]codexControlIntent, len(source))
	for threadID, intent := range source {
		cloned[threadID] = intent
	}
	return cloned
}

func sameCodexSessionBinding(left codexSessionBinding, right codexSessionBinding) bool {
	if left.ActiveWorkspace != right.ActiveWorkspace || len(left.Workspaces) != len(right.Workspaces) {
		return false
	}
	for workspaceRoot, session := range left.Workspaces {
		if right.Workspaces[workspaceRoot] != session {
			return false
		}
	}
	return true
}

func sameCodexControlIntentMap(left map[string]codexControlIntent, right map[string]codexControlIntent) bool {
	if len(left) != len(right) {
		return false
	}
	for threadID, intent := range left {
		if right[threadID] != intent {
			return false
		}
	}
	return true
}
```

`remoteSelectionSnapshotLocked(state codexRemoteSelectionState, bindingKey string, targetThreadID string) codexRemoteSelectionSnapshot`
深拷贝当前 binding，读取目标 intent，并仅把
`Owner == remote && RouteBindingKey == bindingKey` 的 thread 收入 `RouteOwned`。
`applyCodexRemoteSelection(state codexRemoteSelectionState, update codexRemoteSelectionUpdate, now time.Time) (codexRemoteSelectionResult, bool)`
依次调用 `selectCodexRemoteWorkspace`、
`releaseCodexRemoteRouteThreads` 和 `claimCodexRemoteTarget`：目标 workspace 选中 B 并设为
active；同 binding 内其他 workspace 若引用 B 则清空；`RouteOwned` 中除 B
外全部变成 desktop；B 变成当前 binding/conversation 的 remote。只有
owner/route/conversation 实际变化时才设置 `revision+1` 和 `UpdatedAt=now.RFC3339Nano`；
binding 和 intents 均无变化时返回 `changed=false`，不写盘。每个 helper 限制在
50 行内，并由 Step 1 的幂等、全量释放和重启用例直接验证。

- [ ] **Step 5: 运行 store 测试与现有持久化回归**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestCodex(RemoteSelection|ControlIntent|SessionStore)' -count=1 -timeout 60s`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add messaging/codex_sessions.go messaging/codex_session_persistence.go messaging/codex_remote_selection_store.go messaging/codex_remote_selection_store_test.go
git commit -m "增加 Codex 选择与所有权原子提交"
```

## Task 3: 增加多 thread 有序锁

**Files:**
- Create: `messaging/codex_session_locks.go`
- Create: `messaging/codex_session_locks_test.go`

**Interfaces:**
- Consumes: `lockCodexThreadControlContext`、`codexSessionLockWaitTimeoutValue`。
- Produces: `lockCodexSessionThreads(req codexSessionThreadLockRequest) (func(), error)`。

- [ ] **Step 1: 写部分失败释放与 ABBA 回归测试**

```go
func TestLockCodexSessionThreadsReleasesPartialLocksOnTimeout(t *testing.T) {
	h := NewHandler(nil, nil)
	h.codexLockWaitTimeout = 20 * time.Millisecond
	unlockB := h.lockCodexThreadControl("thread-b")
	_, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
		ctx: context.Background(), command: "switch", threadIDs: []string{"thread-b", "thread-a"},
	})
	if !isCodexSessionControlTimeout(err) {
		t.Fatalf("error=%v", err)
	}
	unlockB()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlockA, err := h.lockCodexThreadControlContext(ctx, "thread-a")
	if err != nil {
		t.Fatalf("部分失败后 thread-a 锁未释放: %v", err)
	}
	unlockA()
}

func TestLockCodexSessionThreadsUsesStableOrder(t *testing.T) {
	h := NewHandler(nil, nil)
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, ids := range [][]string{{"thread-a", "thread-b"}, {"thread-b", "thread-a"}} {
		ids := ids
		go func() {
			<-start
			unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
				ctx: context.Background(), command: "switch", threadIDs: ids,
			})
			if err == nil {
				unlock()
			}
			done <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认接口尚不存在**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestLockCodexSessionThreads' -count=1 -timeout 60s`

Expected: FAIL，提示 `codexSessionThreadLockRequest` 未定义。

- [ ] **Step 3: 实现统一预算的排序锁**

```go
package messaging

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type codexSessionThreadLockRequest struct {
	ctx       context.Context
	command   string
	threadIDs []string
}

func (h *Handler) lockCodexSessionThreads(req codexSessionThreadLockRequest) (func(), error) {
	threadIDs := sortedUniqueCodexThreadIDs(req.threadIDs)
	started := time.Now()
	waitCtx, cancel := context.WithTimeout(normalizeContext(req.ctx), h.codexSessionLockWaitTimeoutValue())
	defer cancel()
	unlocks := make([]func(), 0, len(threadIDs))
	for _, threadID := range threadIDs {
		unlock, err := h.lockCodexThreadControlContext(waitCtx, threadID)
		if err != nil {
			releaseCodexSessionThreadLocks(unlocks)
			logCodexSessionControlTimeout(
				req.command, "threads", strings.Join(threadIDs, ","), started, err,
			)
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	var once sync.Once
	return func() { once.Do(func() { releaseCodexSessionThreadLocks(unlocks) }) }, nil
}

func sortedUniqueCodexThreadIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func releaseCodexSessionThreadLocks(unlocks []func()) {
	for index := len(unlocks) - 1; index >= 0; index-- {
		unlocks[index]()
	}
}
```

- [ ] **Step 4: 运行锁与 race 测试**

Run: `GOTOOLCHAIN=local GOPROXY=off go test -race ./messaging -run 'TestLockCodexSessionThreads|TestCodexSessionCommand.*Lock' -count=10 -timeout 60s`

Expected: PASS，无 race、无超时。

- [ ] **Step 5: 提交**

```bash
git add messaging/codex_session_locks.go messaging/codex_session_locks_test.go
git commit -m "增加 Codex 多会话有序锁"
```

## Task 4: 把外部活动任务观察改为可预留的成功屏障

**Files:**
- Create: `messaging/codex_external_task_reservation.go`
- Modify: `messaging/codex_external_task.go:35-49,98-112,123-144`
- Test: `messaging/handler_codex_external_task_test.go`
- Test: `messaging/handler_codex_external_control_test.go`

**Interfaces:**
- Consumes: `resolveExternalCodexTask`、`beginActiveTask`、`runExternalCodexTaskWatcher`。
- Produces:
  - `prepareExternalCodexTask(opts externalCodexTaskOptions) (preparedExternalCodexTask, error)`
  - `reserveExternalCodexTask(opts externalCodexTaskOptions, prepared preparedExternalCodexTask) (externalCodexTaskReservation, error)`
  - `activateExternalCodexTaskReservation(reservation externalCodexTaskReservation)`
  - `cancelExternalCodexTaskReservation(reservation externalCodexTaskReservation)`

- [ ] **Step 1: 写观察槽冲突、复用与取消测试**

```go
func TestReserveExternalCodexTaskRejectsDifferentTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	conversationID := "conversation-1"
	existing, _, started := h.beginActiveTask(context.Background(), conversationID, activeTaskMeta{
		owner: "user-1", agentName: "codex", codexThreadID: "thread-1", codexTurnID: "turn-old",
	})
	if !started {
		t.Fatal("未能建立旧观察任务")
	}
	defer h.finishActiveTask(conversationID, existing)
	prepared := preparedExternalCodexTask{
		active: true,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-new",
		}, Controllable: true},
	}
	_, err := h.reserveExternalCodexTask(externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		agentName: "codex", conversationID: conversationID, threadID: "thread-1",
	}, prepared)
	if !errors.Is(err, errExternalCodexTaskReservationConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestCancelExternalCodexTaskReservationRemovesUnstartedTask(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared := preparedExternalCodexTask{
		active: true,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}, Controllable: true},
		watch: func(context.Context, func(string)) (string, error) { return "完成", nil },
	}
	opts := externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		agentName: "codex", conversationID: "conversation-1", threadID: "thread-1",
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	h.cancelExternalCodexTaskReservation(reservation)
	if _, active := h.activeTask(opts.conversationID); active {
		t.Fatal("取消预留后不应残留 active task")
	}
}

func TestReserveExternalCodexTaskReusesSameThreadTurn(t *testing.T) {
	h := NewHandler(nil, nil)
	prepared := preparedExternalCodexTask{
		active: true,
		state: externalCodexTaskState{CodexThreadState: agent.CodexThreadState{
			ThreadID: "thread-1", Active: true, ActiveTurnID: "turn-1",
		}, Controllable: true},
	}
	opts := externalCodexTaskOptions{
		ctx: context.Background(), actorUserID: "user-1", routeUserID: "route-1",
		agentName: "codex", conversationID: "conversation-1", threadID: "thread-1",
	}
	first, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer h.cancelExternalCodexTaskReservation(first)
	second, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if !second.reused || second.task != first.task {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}
```

- [ ] **Step 2: 运行测试确认预留接口尚不存在**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'Test(Reserve|Cancel)ExternalCodexTask' -count=1 -timeout 60s`

Expected: FAIL，提示 reservation 类型未定义。

- [ ] **Step 3: 实现解析、预留、激活和取消**

```go
package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

var errExternalCodexTaskReservationConflict = errors.New("当前窗口已有其他 Codex 活动任务")

type preparedExternalCodexTask struct {
	state  externalCodexTaskState
	watch  externalCodexTaskWatch
	active bool
}

type externalCodexTaskReservation struct {
	runtime externalCodexTaskRuntime
	key     string
	task    *activeAgentTask
	reused  bool
}

func (h *Handler) prepareExternalCodexTask(opts externalCodexTaskOptions) (preparedExternalCodexTask, error) {
	state, watch, found, err := h.resolveExternalCodexTask(opts)
	if err != nil || !found {
		return preparedExternalCodexTask{state: state}, err
	}
	if state.ActiveTurnID == "" {
		return preparedExternalCodexTask{}, fmt.Errorf("Codex App thread 处于 active 状态，但未找到 active turn")
	}
	if opts.reply == nil {
		return preparedExternalCodexTask{}, fmt.Errorf("Codex App thread 正在运行，但当前入口无法接管回推")
	}
	return preparedExternalCodexTask{state: state, watch: watch, active: true}, nil
}

func (h *Handler) reserveExternalCodexTask(opts externalCodexTaskOptions, prepared preparedExternalCodexTask) (externalCodexTaskReservation, error) {
	if !prepared.active {
		return externalCodexTaskReservation{}, nil
	}
	taskCtx := h.withAgentInteractions(context.Background(), agentInteractionContextOptions{
		actorUserID: opts.actorUserID, routeUserID: opts.routeUserID, reply: opts.reply,
	})
	runtimeOwner, ownerRevision := externalCodexTaskOwner(prepared.state)
	task, watchCtx, started := h.beginActiveTask(taskCtx, opts.conversationID, activeTaskMeta{
		owner: opts.actorUserID, routeUserID: opts.routeUserID, agentName: opts.agentName,
		message: firstNonBlank(prepared.state.Preview, "Codex App 本地任务"),
		runtimeOwner: runtimeOwner, ownerRevision: ownerRevision,
		codexThreadID: opts.threadID, codexTurnID: prepared.state.ActiveTurnID,
	})
	if !started {
		if sameExternalCodexTask(task, opts, prepared.state) {
			return externalCodexTaskReservation{key: opts.conversationID, task: task, reused: true}, nil
		}
		return externalCodexTaskReservation{}, errExternalCodexTaskReservationConflict
	}
	if prepared.state.Progress != "" {
		task.recordProgress(time.Now(), prepared.state.Progress)
	}
	return externalCodexTaskReservation{
		key: opts.conversationID, task: task,
		runtime: externalCodexTaskRuntime{
			opts: opts, state: prepared.state, watch: prepared.watch, task: task, ctx: watchCtx,
		},
	}, nil
}

func (h *Handler) activateExternalCodexTaskReservation(reservation externalCodexTaskReservation) {
	if reservation.task == nil || reservation.reused {
		return
	}
	go h.runExternalCodexTaskWatcher(reservation.runtime)
}

func (h *Handler) cancelExternalCodexTaskReservation(reservation externalCodexTaskReservation) {
	if reservation.task == nil || reservation.reused {
		return
	}
	reservation.task.cancel()
	h.finishActiveTask(reservation.key, reservation.task)
}

func sameExternalCodexTask(task *activeAgentTask, opts externalCodexTaskOptions, state externalCodexTaskState) bool {
	if task == nil {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.owner == strings.TrimSpace(opts.actorUserID) &&
		task.codexThreadID == strings.TrimSpace(opts.threadID) &&
		task.codexTurnID == strings.TrimSpace(state.ActiveTurnID) &&
		task.phase != codexTaskTerminal
}
```

- [ ] **Step 4: 让旧观察入口复用预留 API**

将 `startExternalCodexTaskIfActive` 替换为：

```go
func (h *Handler) startExternalCodexTaskIfActive(opts externalCodexTaskOptions) (externalCodexTaskState, bool, error) {
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil || !prepared.active {
		return prepared.state, false, err
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		return prepared.state, false, err
	}
	h.activateExternalCodexTaskReservation(reservation)
	return prepared.state, true, nil
}
```

删除 `codex_external_task.go:startExternalCodexTaskWatcher` 中已迁移到 reservation 的重复登记逻辑。将可控任务提示改成：

```go
lines = append(lines, "新消息会先暂存；回复 /guide 发送到当前任务，回复 /stop 停止任务，回复 /cancel 撤回暂存。")
```

- [ ] **Step 5: 运行外部任务与控制命令回归**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestCodex(External|Guide|Stop)|Test(Reserve|Cancel)ExternalCodexTask' -count=1 -timeout 60s`

Expected: PASS，最终结果不重复发送。

- [ ] **Step 6: 提交**

```bash
git add messaging/codex_external_task.go messaging/codex_external_task_reservation.go messaging/handler_codex_external_task_test.go messaging/handler_codex_external_control_test.go
git commit -m "增加 Codex 活动任务观察成功屏障"
```

## Task 5: 实现统一选择接管 saga

**Files:**
- Create: `messaging/codex_session_acquire.go`
- Create: `messaging/codex_session_acquire_runtime.go`
- Create: `messaging/codex_session_acquire_test.go`
- Modify: `messaging/codex_live_fakes_test.go:11-89`

**Interfaces:**
- Consumes: `remoteSelectionSnapshot`、`commitRemoteSelection`、`lockCodexSessionThreads`、观察 reservation、`agent.CodexLiveRuntimeAgent`。
- Produces:
  - `acquireCodexSessionWithBindingLocked(req codexSessionAcquireRequest) (codexSessionAcquireResult, error)`
  - `renderCodexSessionAcquireFailure(err error) string`
  - `codexSessionAcquireResult`，供 switch/cd/new/owner 共用。

- [ ] **Step 1: 扩展 live fake 以按 thread 注入故障**

在 `fakeCodexLiveAgent` 增加：

```go
	bindings       map[string]agent.CodexThreadBinding
	handoffErrors  map[string]error
	inspectErrors  map[string]error
	handoffHistory []agent.CodexRuntimeRequest
```

构造器初始化三个 map。`InspectCodexRuntime` 与 `HandoffCodexRuntime` 必须优先按 `req.Ref.ThreadID` 读取对应 map，且把每次 handoff request 追加到 `handoffHistory`；没有专用值时保持现有单 binding 行为。增加测试 helper：

```go
func (f *fakeCodexLiveAgent) setThreadBinding(threadID string, binding agent.CodexThreadBinding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	binding.State.ThreadID = threadID
	f.bindings[threadID] = binding
}

func (f *fakeCodexLiveAgent) handoffRequests() []agent.CodexRuntimeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.CodexRuntimeRequest(nil), f.handoffHistory...)
}
```

- [ ] **Step 2: 写核心状态机失败测试**

`messaging/codex_session_acquire_test.go` 包含以下全部可独立运行的测试：

```go
func TestAcquireCodexSessionSwitchesAtoBAndReleasesA(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	result, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err != nil {
		t.Fatal(err)
	}
	if result.route.threadID != "thread-b" || result.resolution.Request.Intent.Owner != agent.CodexControlRemote {
		t.Fatalf("result=%#v", result)
	}
	if got := fixture.h.ensureCodexSessions().controlIntent("thread-a"); got.Owner != codexControlDesktop {
		t.Fatalf("thread-a=%#v", got)
	}
	if got := fixture.h.ensureCodexSessions().controlIntent("thread-b"); got.Owner != codexControlRemote {
		t.Fatalf("thread-b=%#v", got)
	}
}

func TestAcquireCodexSessionRejectsOtherRemoteWithoutChanges(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	claimRemoteControlForTest(t, fixture.h, fakeRemoteControlOptions{
		routeUserID: "other-user", agentName: "codex", bindingKey: "other-route",
		workspace: fixture.workspaceB, threadID: "thread-b",
	})
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if !errors.Is(err, errCodexRemoteSelectionOtherRoute) {
		t.Fatalf("error=%v", err)
	}
	assertCodexAcquireOriginalState(t, fixture)
}

func TestAcquireCodexSessionCompensatesTargetWhenOldReleaseFails(t *testing.T) {
	fixture := newCodexSessionAcquireFixture(t)
	fixture.agent.handoffErrors["thread-a"] = errors.New("释放失败")
	_, err := fixture.h.acquireCodexSessionWithBindingLocked(fixture.request("thread-b"))
	if err == nil {
		t.Fatal("旧 thread 释放失败时事务不应成功")
	}
	assertCodexAcquireOriginalState(t, fixture)
	requests := fixture.agent.handoffRequests()
	if len(requests) < 3 || requests[len(requests)-1].Intent.Owner != agent.CodexControlDesktop {
		t.Fatalf("handoff history=%#v，最后应恢复 B 原 desktop intent", requests)
	}
}
```

同一 fixture 再增加：

- `TestAcquireCodexSessionRejectsActiveOldRemoteTask`
- `TestAcquireCodexSessionNormalizesMultipleRouteOwnedThreads`
- `TestAcquireCodexSessionIsIdempotent`
- `TestAcquireCodexSessionPersistenceFailureCompensatesRuntime`
- `TestAcquireCodexSessionCompensationFailureIsFailClosed`
- `TestAcquireCodexSessionHandoffTimeoutDoesNotRetrySideEffect`
- `TestAcquireCodexSessionLockTimeoutKeepsOriginalState`

fixture 必须为 A/B 建立不同 workspace、A remote/current route、B desktop、live Desktop bindings，并让 request 明确带 `reply`；断言 helper 必须同时核对 active workspace、A/B thread、A/B intents 和 handoff 次数。

- [ ] **Step 3: 运行测试确认统一事务尚不存在**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestAcquireCodexSession' -count=1 -timeout 60s`

Expected: FAIL，提示 `acquireCodexSessionWithBindingLocked` 未定义。

- [ ] **Step 4: 定义事务请求、结果、计划与错误**

`codex_session_acquire.go` 定义：

```go
var (
	errCodexSessionAcquireActiveOld  = errors.New("当前远程任务仍在执行")
	errCodexSessionAcquireUncertain  = errors.New("Codex 控制权移交结果未确认")
	errCodexSessionAcquireUnsupported = errors.New("当前 Codex Agent 不支持选择即接管")
)

type codexSessionAcquireRequest struct {
	ctx           context.Context
	actorUserID   string
	routeUserID   string
	agentName     string
	agent         agent.Agent
	route         codexConversationRoute
	platform      platform.PlatformName
	accountID     string
	reply         platform.Replier
	taskContext   context.Context
}

type codexSessionAcquireResult struct {
	route          codexConversationRoute
	resolution     codexRuntimeResolution
	externalState  externalCodexTaskState
	externalActive bool
	agentSessionErr error
}

type codexRuntimeIntentChange struct {
	threadID string
	route    codexConversationRoute
	before   codexControlIntent
	after    codexControlIntent
}

type codexRuntimeHandoffRequest struct {
	ctx          context.Context
	liveAgent    agent.CodexLiveRuntimeAgent
	change       codexRuntimeIntentChange
	resyncIntent codexControlIntent
}

type codexSessionAcquirePlan struct {
	request  codexSessionAcquireRequest
	snapshot codexRemoteSelectionSnapshot
	changes  []codexRuntimeIntentChange
}

type codexSessionAcquireCommit struct {
	request     codexSessionAcquireRequest
	resolution  codexRuntimeResolution
	committed   codexRemoteSelectionResult
	prepared    preparedExternalCodexTask
	reservation externalCodexTaskReservation
}

type codexSessionAcquireRuntimeCommit struct {
	plan       codexSessionAcquirePlan
	liveAgent  agent.CodexLiveRuntimeAgent
	resolution codexRuntimeResolution
	applied    []codexRuntimeIntentChange
}

type codexSessionAcquireRollback struct {
	plan        codexSessionAcquirePlan
	liveAgent   agent.CodexLiveRuntimeAgent
	applied     []codexRuntimeIntentChange
	reservation externalCodexTaskReservation
	cause       error
}
```

字段按 `gofmt` 对齐；每个关键类型添加中文注释。

- [ ] **Step 5: 实现事务编排**

主函数保持 50 行以内，并严格使用以下顺序：

```go
func (h *Handler) acquireCodexSessionWithBindingLocked(req codexSessionAcquireRequest) (codexSessionAcquireResult, error) {
	liveAgent, ok := req.agent.(agent.CodexLiveRuntimeAgent)
	if !ok {
		return codexSessionAcquireResult{}, errCodexSessionAcquireUnsupported
	}
	store := h.ensureCodexSessions()
	initial := store.remoteSelectionSnapshot(req.route.bindingKey, req.route.threadID)
	unlock, err := h.lockCodexSessionThreads(codexSessionThreadLockRequest{
		ctx: req.ctx, command: "acquire", threadIDs: codexRemoteSelectionThreadIDs(initial),
	})
	if err != nil {
		return codexSessionAcquireResult{}, err
	}
	defer unlock()
	locked := store.remoteSelectionSnapshot(req.route.bindingKey, req.route.threadID)
	if !sameCodexRemoteSelectionLockSet(initial, locked) {
		return codexSessionAcquireResult{}, errCodexRemoteSelectionChanged
	}
	plan, err := h.buildCodexSessionAcquirePlan(req, locked)
	if err != nil {
		return codexSessionAcquireResult{}, err
	}
	h.bindConversationCwd(req.agent, req.route.conversationID, req.route.workspaceRoot)
	resolution, applied, err := h.applyCodexRuntimeIntentChanges(plan, liveAgent)
	if err != nil {
		return codexSessionAcquireResult{}, h.rollbackCodexAcquire(codexSessionAcquireRollback{
			plan: plan, liveAgent: liveAgent, applied: applied, cause: err,
		})
	}
	return h.commitCodexSessionAcquire(codexSessionAcquireRuntimeCommit{
		plan: plan, liveAgent: liveAgent, resolution: resolution, applied: applied,
	})
}

func sameCodexRemoteSelectionLockSet(left codexRemoteSelectionSnapshot, right codexRemoteSelectionSnapshot) bool {
	leftIDs := codexRemoteSelectionThreadIDs(left)
	rightIDs := codexRemoteSelectionThreadIDs(right)
	if len(leftIDs) != len(rightIDs) {
		return false
	}
	for index := range leftIDs {
		if leftIDs[index] != rightIDs[index] {
			return false
		}
	}
	return true
}
```

`buildCodexSessionAcquirePlan` 先校验 `snapshot.Target`：其 owner 为 remote 且
`RouteBindingKey != req.route.bindingKey` 时返回 `errCodexRemoteSelectionOtherRoute`。然后按
thread ID 升序扫描 `snapshot.RouteOwned`，跳过目标 thread；任一旧 thread 通过
`activeCodexTaskConversation` 找到未终态任务时返回 `errCodexSessionAcquireActiveOld`。
`plan.changes` 只包含非目标 thread 的 remote→desktop change；目标 change 仅由
`acquireCodexTargetRuntime` 构造，避免同一 thread 重复 handoff。每个旧 thread 的
`change.before` 取快照 intent，`change.after` 为仅保留 `Owner=desktop`、
`Revision=before.Revision+1` 的新 intent；`change.route.ConversationID` 使用
`before.ConversationID`，workspace 从 `snapshot.Binding.Workspaces` 中按 thread ID 查找，未找到时
保持为空，但不改写 conversation/thread ref。

- [ ] **Step 6: 实现 runtime 应用与逆序补偿**

`codex_session_acquire_runtime.go` 使用以下完整接口：

```go
func (h *Handler) applyCodexRuntimeIntentChanges(plan codexSessionAcquirePlan, liveAgent agent.CodexLiveRuntimeAgent) (codexRuntimeResolution, []codexRuntimeIntentChange, error) {
	resolution, targetChange, err := h.acquireCodexTargetRuntime(plan, liveAgent)
	if err != nil {
		return resolution, nil, err
	}
	applied := make([]codexRuntimeIntentChange, 0, len(plan.changes))
	if targetChange != nil {
		applied = append(applied, *targetChange)
	}
	for _, change := range plan.changes {
		_, err := h.handoffCodexRuntimeIntent(codexRuntimeHandoffRequest{
			ctx: plan.request.ctx, liveAgent: liveAgent,
			change: change, resyncIntent: change.before,
		})
		if err != nil {
			return resolution, applied, err
		}
		applied = append(applied, change)
	}
	return resolution, applied, nil
}

func (h *Handler) compensateCodexRuntimeChanges(ctx context.Context, liveAgent agent.CodexLiveRuntimeAgent, applied []codexRuntimeIntentChange) error {
	var compensationErr error
	for index := len(applied) - 1; index >= 0; index-- {
		change := applied[index]
		reverse := codexRuntimeIntentChange{
			threadID: change.threadID, route: change.route,
			before: change.after, after: change.before,
		}
		_, err := h.handoffCodexRuntimeIntent(codexRuntimeHandoffRequest{
			ctx: ctx, liveAgent: liveAgent,
			change: reverse, resyncIntent: reverse.after,
		})
		if err != nil {
			compensationErr = errors.Join(compensationErr, err)
		}
	}
	return compensationErr
}
```

`acquireCodexTargetRuntime` 先构造以下 target intent：

```go
func proposedCodexRemoteSelectionIntent(current codexControlIntent, route codexConversationRoute) codexControlIntent {
	if current.Owner == codexControlRemote &&
		current.RouteBindingKey == route.bindingKey &&
		current.ConversationID == route.conversationID {
		return current
	}
	return codexControlIntent{
		Owner: codexControlRemote, RouteBindingKey: route.bindingKey,
		ConversationID: route.conversationID, Revision: current.Revision + 1,
	}
}
```

新旧 intent 一致时只调用一次 `InspectCodexRuntime`，返回 `targetChange=nil`；不一致时
调用 `handoffCodexRuntimeIntent`，其 `resyncIntent` 为 target 的 `before`，成功后返回
target change。`handoffCodexRuntimeIntent` 返回包含 request、binding 和 rollout checkpoint 的
`codexRuntimeResolution`；它使用 `buildCodexRuntimeRequest` 获取 checkpoint，将
`request.Intent` 替换为 `change.after` 后只调用一次 `HandoffCodexRuntime`。

调用超时或取消时不重发 handoff；将 `request.Intent` 替换为
`resyncIntent`，只调用一次 `InspectCodexRuntime` 校准 registry。前向操作的
`resyncIntent=change.before`；补偿操作的 `resyncIntent=reverse.after`，两者都指向当前
持久化权威意图。inspect 失败时返回
`errors.Join(errCodexSessionAcquireUncertain, handoffErr, inspectErr)`；inspect 成功时返回原
handoff 超时，让上层补偿已确认的更早变更。

- [ ] **Step 7: 预留观察后一次提交 store**

`commitCodexSessionAcquire` 必须先解析并预留观察槽，再提交 store；store 失败时先取消预留，再逆序补偿 runtime：

```go
func externalCodexTaskOptionsFromAcquire(req codexSessionAcquireRequest) externalCodexTaskOptions {
	taskContext := req.taskContext
	if taskContext == nil {
		taskContext = normalizeContext(req.ctx)
	}
	return externalCodexTaskOptions{
		ctx: taskContext, actorUserID: req.actorUserID,
		routeUserID: req.routeUserID, agentName: req.agentName,
		agent: req.agent, conversationID: req.route.conversationID,
		threadID: req.route.threadID, platform: req.platform,
		accountID: req.accountID, reply: req.reply,
	}
}

func (h *Handler) commitCodexSessionAcquire(commit codexSessionAcquireRuntimeCommit) (codexSessionAcquireResult, error) {
	opts := externalCodexTaskOptionsFromAcquire(commit.plan.request)
	prepared, err := h.prepareExternalCodexTask(opts)
	if err != nil {
		return codexSessionAcquireResult{}, h.rollbackCodexAcquire(codexSessionAcquireRollback{
			plan: commit.plan, liveAgent: commit.liveAgent, applied: commit.applied, cause: err,
		})
	}
	if codexResolutionActive(commit.resolution) && (!prepared.active || !prepared.state.Controllable) {
		err = fmt.Errorf("活动 Desktop 任务尚不能由当前窗口控制")
		return codexSessionAcquireResult{}, h.rollbackCodexAcquire(codexSessionAcquireRollback{
			plan: commit.plan, liveAgent: commit.liveAgent, applied: commit.applied, cause: err,
		})
	}
	reservation, err := h.reserveExternalCodexTask(opts, prepared)
	if err != nil {
		return codexSessionAcquireResult{}, h.rollbackCodexAcquire(codexSessionAcquireRollback{
			plan: commit.plan, liveAgent: commit.liveAgent, applied: commit.applied, cause: err,
		})
	}
	committed, err := h.ensureCodexSessions().commitRemoteSelection(codexRemoteSelectionUpdate{
		BindingKey: commit.plan.request.route.bindingKey,
		WorkspaceRoot: commit.plan.request.route.workspaceRoot,
		TargetThreadID: commit.plan.request.route.threadID,
		ConversationID: commit.plan.request.route.conversationID,
		Expected: commit.plan.snapshot,
	})
	if err != nil {
		return codexSessionAcquireResult{}, h.rollbackCodexAcquire(codexSessionAcquireRollback{
			plan: commit.plan, liveAgent: commit.liveAgent, applied: commit.applied,
			reservation: reservation, cause: err,
		})
	}
	return h.finishCodexSessionAcquire(codexSessionAcquireCommit{
		request: commit.plan.request, resolution: commit.resolution, committed: committed,
		prepared: prepared, reservation: reservation,
	}), nil
}
```

`finishCodexSessionAcquire` 使用以下实现；`agentSessions.Set` 是跨文件辅助状态，
失败不回滚已确认的 writer/ownership：

```go
func (h *Handler) finishCodexSessionAcquire(commit codexSessionAcquireCommit) codexSessionAcquireResult {
	intent := agentControlIntent(commit.committed.Target)
	commit.resolution.Request.Intent = intent
	commit.resolution.Binding.Control = intent
	h.switchCodexWorkspaceForRoute(
		firstNonBlank(commit.request.actorUserID, commit.request.routeUserID),
		commit.request.routeUserID, commit.request.agentName,
		commit.request.route.workspaceRoot, commit.request.agent,
	)
	agentSessionErr := h.ensureAgentSessions().Set(
		commit.request.routeUserID, commit.request.agentName,
	)
	h.activateExternalCodexTaskReservation(commit.reservation)
	return codexSessionAcquireResult{
		route: commit.request.route, resolution: commit.resolution,
		externalState: commit.prepared.state, externalActive: commit.prepared.active,
		agentSessionErr: agentSessionErr,
	}
}

func (h *Handler) rollbackCodexAcquire(rollback codexSessionAcquireRollback) error {
	h.cancelExternalCodexTaskReservation(rollback.reservation)
	if err := h.compensateCodexRuntimeChanges(
		rollback.plan.request.ctx, rollback.liveAgent, rollback.applied,
	); err != nil {
		return errors.Join(errCodexSessionAcquireUncertain, rollback.cause, err)
	}
	return rollback.cause
}

func codexResolutionActive(resolution codexRuntimeResolution) bool {
	return resolution.Binding.State.Active || resolution.Rollout.Active
}
```

最终渲染在 `agentSessionErr != nil` 时附加“接管成功，但默认 Agent
保存失败”，不把已成功事务误报成完整失败。

- [ ] **Step 8: 运行核心测试与 race**

Run: `GOTOOLCHAIN=local GOPROXY=off go test -race ./messaging -run 'TestAcquireCodexSession|TestCodexRemoteSelection|TestLockCodexSessionThreads' -count=10 -timeout 60s`

Expected: PASS；并发争用只有一个成功；无重复 side-effect handoff。

- [ ] **Step 9: 提交**

```bash
git add messaging/codex_session_acquire.go messaging/codex_session_acquire_runtime.go messaging/codex_session_acquire_test.go messaging/codex_live_fakes_test.go
git commit -m "实现 Codex 会话选择接管事务"
```

## Task 6: 让 switch、短编号、`/cx cd` 与飞书卡片共用事务

**Files:**
- Modify: `messaging/codex_session_switch.go:15-183`
- Modify: `messaging/codex_browser.go:95-191`
- Test: `messaging/handler_codex_live_switch_test.go`
- Test: `messaging/handler_codex_navigation_switch_test.go`
- Test: `messaging/handler_codex_browse_feishu_test.go`
- Test: `messaging/codex_short_navigation_card_test.go`
- Test: `messaging/handler_codex_browse_test.go`

**Interfaces:**
- Consumes: `acquireCodexSessionWithBindingLocked`、`codexSessionAcquireResult`。
- Produces:
  - `codexSwitchRequest.acquireRequest(route codexConversationRoute) codexSessionAcquireRequest`
  - `renderCodexSessionAcquireSuccess(result codexSessionAcquireResult) string`
  - `renderCodexSessionAcquireFailure(err error) string`

- [ ] **Step 1: 把旧两阶段断言改为“成功即接管”断言**

在 `handler_codex_live_switch_test.go` 重命名并改写以下用例：

- `TestCodexSwitchDesktopActiveAcquiresAndObservesTask`：成功后 B 为 remote/current route，runtime
  仍为 Desktop，`UseCodexThread` 零调用，active task 绑定 B/turn，回复包含
  “已切换并接管”与“已开始回传”。
- `TestCodexSwitchDesktopIdleAcquiresWithoutUse`：B remote，runtime Desktop，无 active task，
  `UseCodexThread` 零调用。
- `TestCodexSwitchUnclaimedAcquiresRemoteControl`：B 从 unclaimed 变为当前 route remote。
- `TestCodexSwitchRejectsOtherRemoteAndKeepsA`：B 归其他 route 时，A 仍是当前选择和
  remote owner，回复不含对方 route/conversation。
- `TestCodexSwitchProbeFailureKeepsA`、`TestCodexSwitchProbeTimeoutKeepsA`、
  `TestCodexSwitchThreadLockTimeoutKeepsA`：失败后 active workspace、A/B thread 和 intents 全部
  不变，不再断言“B 选择已保留”。

在 `handler_codex_navigation_switch_test.go` 改写
`TestCodexShortIndexPreservesBindingWhenSingleSessionCannotBeRestored`，断言单会话自动接管
失败后原 workspace/thread/owner 不变。在 `handler_codex_browse_feishu_test.go` 将
`TestFeishuCodexWorkspaceChoiceSendsSingleSessionChoiceCard` 改为
`TestFeishuCodexWorkspaceChoiceAutoAcquiresSingleSessionWithoutSecondCard`，断言只发一条成功文本、
不再发单会话选择卡。

- [ ] **Step 2: 运行改写后的用例，确认旧实现失败**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestCodexSwitch|TestCodexShortIndex|TestFeishuCodexWorkspaceChoice' -count=1 -timeout 60s`

Expected: FAIL；旧实现不会 handoff，且单会话飞书路径会再发选择卡。

- [ ] **Step 3: 用统一事务替换 switch 的先选择后探测路径**

`handleCodexSwitchForRouteWithOptions` 改为：

```go
func (h *Handler) handleCodexSwitchForRouteWithOptions(req codexSwitchRequest) string {
	if _, ok := req.agent.(agent.CodexLiveRuntimeAgent); !ok {
		return "当前 Codex Agent 不支持选择即接管。"
	}
	route, err := h.resolveCodexSwitchRoute(req)
	if err != nil {
		return err.Error()
	}
	result, err := h.acquireCodexSessionWithBindingLocked(req.acquireRequest(route))
	if err != nil {
		return renderCodexSessionAcquireFailure(err)
	}
	return h.renderCodexSessionAcquireSuccess(result)
}

func (req codexSwitchRequest) acquireRequest(route codexConversationRoute) codexSessionAcquireRequest {
	return codexSessionAcquireRequest{
		ctx: req.ctx, taskContext: codexExternalTaskContext(req),
		actorUserID: firstNonBlank(req.options.actorUserID, req.userID),
		routeUserID: req.userID, agentName: req.agentName, agent: req.agent,
		route: route, platform: req.options.platform, accountID: req.options.accountID,
		reply: req.options.reply,
	}
}
```

删除已无调用的 `commitCodexSwitchSelection`、`inspectSelectedCodexRuntimeLocked`、
`codexSwitchRenderRequest`、`renderCodexRuntimeInspectError` 和 `renderCodexSwitchControlTimeout`。
`renderCodexSessionAcquireSuccess` 固定首行为“已切换并接管。”，然后输出 workspace、
model/effort、“控制方: 当前远程窗口”、真实 runtime；`externalActive` 为 true 时追加
`renderExternalCodexActiveNotice`。`agentSessionErr` 非空时追加单独告警，不改变成功首行。

`renderCodexSessionAcquireFailure` 使用 `errors.Is` 映射：

```go
func renderCodexSessionAcquireFailure(err error) string {
	switch {
	case errors.Is(err, errCodexRemoteSelectionOtherRoute):
		return "其他远程窗口正在控制该会话，请原窗口先释放。"
	case errors.Is(err, errCodexSessionAcquireActiveOld):
		return "当前远程任务仍在执行，请等待完成或先发送 /stop。"
	case errors.Is(err, errCodexRemoteSelectionChanged):
		return "Codex 会话所有权已被并发修改，请重新查询后重试。"
	case errors.Is(err, errCodexSessionAcquireUncertain):
		return "Codex 控制权移交结果未确认，当前禁止继续写入。"
	case isCodexSessionControlTimeout(err):
		return "前一项会话操作仍在处理，本次选择未执行。"
	default:
		return "切换并接管 Codex 会话失败: " + err.Error()
	}
}
```

- [ ] **Step 4: 让 `/cx cd` 只在选中真实 thread 时改变所有权**

`handleCodexCdResult` 只完成访问校验、设置 browse workspace 并调用 `enterCodexWorkspace`；
不再在知道会话数量前调用 `switchCodexWorkspaceForRoute` 或 `setCodexActiveWorkspaceForRoute`。
`enterCodexWorkspace` 使用以下分支顺序：

```go
func (h *Handler) enterCodexWorkspace(req codexWorkspaceCdRequest, group codexWorkspaceGroup, workspaceRoot string) navigationCommandResult {
	sessions := switchableCodexSessions(group.Sessions)
	if len(sessions) == 1 {
		return h.enterCodexWorkspaceWithSingleSessionResult(codexSingleSessionEntryRequest{
			command: req, workspaceName: group.Name,
			workspaceRoot: workspaceRoot, session: sessions[0],
		})
	}
	h.switchCodexWorkspaceForRoute(
		req.ActorUserID, req.UserID, req.AgentName, workspaceRoot, req.Agent,
	)
	h.setCodexActiveWorkspaceForRoute(req.BindingKey, req.OwnerBindingKey, workspaceRoot)
	if len(sessions) == 0 {
		return h.enterCodexWorkspaceWithoutSessionsResult(req, group.Name, workspaceRoot)
	}
	return cardNavigationResult(wechatCommandText(
		"工作空间: "+group.Name,
		h.renderCodexSessionList(req.BindingKey, workspaceRoot),
	))
}
```

`enterCodexWorkspaceWithSingleSessionResult` 构造 target route 并直接调用
`acquireCodexSessionWithBindingLocked`。成功和失败都返回 `textNavigationResult`，确保
`ShowCard=false`；成功文本首行为“已进入工作空间并接管唯一会话。”。零会话和
多会话分支只更新导航/active workspace，不读写 control intents。

- [ ] **Step 5: 运行 switch、导航与卡片回归**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestCodexSwitch|TestCodexCxSwitch|TestCodexShort|TestFeishuCodexWorkspaceChoice|TestFeishuCodexStaleSessionChoice' -count=1 -timeout 60s`

Expected: PASS；唯一会话飞书路径只发一条成功回复。

- [ ] **Step 6: 提交**

```bash
git add messaging/codex_session_switch.go messaging/codex_browser.go \
  messaging/handler_codex_live_switch_test.go \
  messaging/handler_codex_navigation_switch_test.go \
  messaging/handler_codex_browse_feishu_test.go \
  messaging/codex_short_navigation_card_test.go messaging/handler_codex_browse_test.go
git commit -m "统一 Codex 会话选择接管入口"
```

## Task 7: 让 `/cx new` 与全局 `/new` 创建后原子接管

**Files:**
- Create: `messaging/codex_session_new.go`
- Modify: `messaging/codex_session_switch.go:48-73`
- Modify: `messaging/codex_session_command_dispatch.go:163-174`
- Modify: `messaging/default_session.go:30-106`
- Modify: `messaging/platform_commands.go:49-62`
- Test: `messaging/handler_codex_session_test.go`
- Test: `messaging/handler_codex_binding_race_test.go`
- Test: `messaging/handler_claude_agent_binding_test.go`
- Test: `messaging/handler_feishu_session_test.go`

**Interfaces:**
- Consumes: `Agent.ResetSession`、`CodexThreadAgent.UseCodexThread/ClearCodexThread`、统一 acquire saga。
- Produces:
  - `createAndAcquireCodexSessionWithBindingLocked(req codexSessionCreateRequest) (codexSessionCreateResult, error)`
  - `restoreCodexSessionAfterCreateFailure(req codexSessionRestoreRequest) error`
  - `/cx new` 和 Codex 默认 Agent 的 `/new` 共用同一方法。

- [ ] **Step 1: 写创建成功、失败恢复和全局入口测试**

在 `handler_codex_session_test.go` 增加或改写：

- `TestHandleCodexNewCreatesSelectsAndAcquiresThread`：新 B 为当前 thread 和 remote owner，旧 A
  变 desktop，回复包含“已创建并接管”。
- `TestHandleCodexNewAcquireFailureRestoresPreviousThread`：注入 B handoff 失败，断言
  session store 和 ACP mapping 都恢复 A，A 的 control intent 不变，回复说明 B 仍在
  Codex 历史中。
- `TestHandleCodexNewAcquireFailureClearsMappingWithoutPreviousThread`：原本无 thread 时调用
  `ClearCodexThread(conversationID)`，不把 B 写入 store。
- `TestHandleGlobalNewCreatesSelectsAndAcquiresCodexThread`：默认 Agent 为 Codex 时的
  `/new` 返回同样所有权结果，且使用真实 actor/route/platform/account/reply。
- `TestHandleGlobalNewKeepsClaudeResetBehavior`：Claude 分支回归不变。

在 `handler_codex_binding_race_test.go` 将 `TestCodexNewUsesBindingLock` 扩展为：持有 route
binding 锁时 `/cx new` 不调用 `ResetSession`；释放后创建与接管只执行一次。

- [ ] **Step 2: 运行用例确认旧实现会提前写 store**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestHandle(CodexNew|GlobalNew)|TestCodexNewUsesBindingLock' -count=1 -timeout 60s`

Expected: FAIL；旧 `/cx new` 和 `/new` 只记录 thread，不接管，且接管失败无恢复契约。

- [ ] **Step 3: 实现创建后接管编排**

`codex_session_new.go` 定义：

```go
type codexSessionCreateRequest struct {
	acquire codexSessionAcquireRequest
}

type codexSessionCreateResult struct {
	acquireResult codexSessionAcquireResult
	createdThread string
}

type codexSessionRestoreRequest struct {
	ctx              context.Context
	agent            agent.CodexThreadAgent
	conversationID   string
	previousThreadID string
}

func (h *Handler) createAndAcquireCodexSessionWithBindingLocked(req codexSessionCreateRequest) (codexSessionCreateResult, error) {
	if err := h.validateCodexSessionCreatePreflight(req.acquire); err != nil {
		return codexSessionCreateResult{}, err
	}
	threadAgent, ok := req.acquire.agent.(agent.CodexThreadAgent)
	if !ok {
		return codexSessionCreateResult{}, errCodexSessionAcquireUnsupported
	}
	previousThreadID, pending := h.ensureCodexSessions().getThread(
		req.acquire.route.bindingKey, req.acquire.route.workspaceRoot,
	)
	if pending {
		previousThreadID = ""
	}
	h.bindConversationCwd(
		req.acquire.agent, req.acquire.route.conversationID,
		req.acquire.route.workspaceRoot,
	)
	created, createErr := req.acquire.agent.ResetSession(
		req.acquire.ctx, req.acquire.route.conversationID,
	)
	created = strings.TrimSpace(created)
	if createErr != nil || created == "" {
		cause := codexSessionCreateError(createErr, created)
		restoreErr := restoreCodexSessionAfterCreateFailure(codexSessionRestoreRequest{
			ctx: req.acquire.ctx, agent: threadAgent,
			conversationID: req.acquire.route.conversationID,
			previousThreadID: previousThreadID,
		})
		if restoreErr != nil {
			return codexSessionCreateResult{},
				errors.Join(errCodexSessionAcquireUncertain, cause, restoreErr)
		}
		return codexSessionCreateResult{}, cause
	}
	req.acquire.route.threadID = created
	result, acquireErr := h.acquireCodexSessionWithBindingLocked(req.acquire)
	if acquireErr == nil {
		return codexSessionCreateResult{acquireResult: result, createdThread: created}, nil
	}
	restoreErr := restoreCodexSessionAfterCreateFailure(codexSessionRestoreRequest{
		ctx: req.acquire.ctx, agent: threadAgent,
		conversationID: req.acquire.route.conversationID,
		previousThreadID: previousThreadID,
	})
	if restoreErr != nil {
		return codexSessionCreateResult{createdThread: created},
			errors.Join(errCodexSessionAcquireUncertain, acquireErr, restoreErr)
	}
	return codexSessionCreateResult{createdThread: created}, acquireErr
}

func restoreCodexSessionAfterCreateFailure(req codexSessionRestoreRequest) error {
	if strings.TrimSpace(req.previousThreadID) == "" {
		req.agent.ClearCodexThread(req.conversationID)
		return nil
	}
	return req.agent.UseCodexThread(req.ctx, req.conversationID, req.previousThreadID)
}
```

`validateCodexSessionCreatePreflight` 读取当前 route 的 `RouteOwned`；任一非终态 active task
即返回 `errCodexSessionAcquireActiveOld`，必须在 `ResetSession` 之前停止。
`codexSessionCreateError` 将非空 ResetSession error 包装为“创建新的 Codex 会话失败”；
thread ID 为空且 error 为空时返回“Codex 未返回新会话 ID”。

- [ ] **Step 4: 两个入口透传完整上下文**

`codexNewRequest` 增加 `actorUserID/platform/accountID/reply/taskContext`，
`handleCodexNewForRoute` 只构造 route/acquire request、调用新 helper 并渲染；删除
`recordResetCodexThread`。`dispatchCodexMutationCommand` 从 `runtime.req` 透传上述字段。

`defaultSessionResetRequest` 增加 `reply platform.Replier`；`platform_commands.go` 传入 `req.Reply`。
`resetDefaultCodexSessionForRoute` 保留现有 `codexBindingExecutionKey(bindingKey)` 外层锁，构造与
`/cx new` 相同的 `codexSessionCreateRequest`。接管失败且 `createdThread != ""` 时回复：

```text
新 Codex 会话已创建，但接管失败；原会话已恢复。
新会话仍保留在 Codex 历史中。
```

结果为 `errCodexSessionAcquireUncertain` 时不宣称“原会话已恢复”，改用 fail-closed
回复。

- [ ] **Step 5: 运行新建、路由 Agent 和 binding race 回归**

Run: `GOTOOLCHAIN=local GOPROXY=off go test -race ./messaging -run 'TestHandle(CodexNew|GlobalNew)|TestCodexSessionNewBindsWindowToCodex|TestCodexNewUsesBindingLock|TestFeishuNewUsesSessionDefaultAgent' -count=5 -timeout 60s`

Expected: PASS；Codex 新建成功时只有 B 被选中且归当前 route，Claude 分支不变。

- [ ] **Step 6: 提交**

```bash
git add messaging/codex_session_new.go messaging/codex_session_switch.go \
  messaging/codex_session_command_dispatch.go messaging/default_session.go \
  messaging/platform_commands.go messaging/handler_codex_session_test.go \
  messaging/handler_codex_binding_race_test.go \
  messaging/handler_claude_agent_binding_test.go messaging/handler_feishu_session_test.go
git commit -m "统一 Codex 新建会话接管事务"
```

## Task 8: 收口 owner 命令、显式释放和活动任务控制

**Files:**
- Modify: `messaging/codex_owner_command.go:18-143`
- Modify: `messaging/codex_runtime_binding.go:143-165`
- Test: `messaging/codex_owner_command_test.go`
- Test: `messaging/codex_owner_message_test.go`
- Test: `messaging/handler_codex_external_task_test.go`
- Test: `messaging/handler_codex_external_control_test.go`
- Test: `messaging/handler_codex_live_message_control_test.go`

**Interfaces:**
- `/cx owner remote` 使用统一 acquire saga。
- `/cx owner desktop` 保留单 thread 显式释放，不清除选择。
- 普通消息只验证当前持久化 owner，不自动接管。

- [ ] **Step 1: 先改写 owner 与活动 Desktop 控制用例**

- 将 `TestCodexOwnerRemoteCommitsAfterSuccessfulHandoff` 改为
  `TestCodexOwnerRemoteUsesSelectionAcquireTransaction`，同时断言同 route 的其他旧 thread
  已释放。
- 保留并加强 `TestCodexOwnerDesktopRejectsActiveRemoteTask`：拒绝时选择仍保留，
  intent/runtime 不变。
- 增加 `TestCodexOwnerDesktopReleasesButKeepsSelection`：释放成功后 thread 仍为当前选择，
  owner=desktop。
- 增加 `TestCodexReselectAfterDesktopReleaseAcquiresAgain`：再次 `/cx switch` 同一 thread 后
  owner=remote。
- 在 `handler_codex_external_control_test.go` 增加
  `TestCodexSelectedDesktopTaskImmediatelyAcceptsGuideAndStop`：选择活动 Desktop B 后，
  `/guide` 调用 `SteerCodexThread` 的 B/active turn，`/stop` 调用
  `InterruptCodexThread` 的同一 B/turn，两者 actor 必须与所有者一致。

- [ ] **Step 2: 运行用例确认 owner remote 仍走旧路径**

Run: `GOTOOLCHAIN=local GOPROXY=off go test ./messaging -run 'TestCodexOwner|TestCodexReselect|TestCodexSelectedDesktopTask' -count=1 -timeout 60s`

Expected: FAIL；旧 owner remote 只更新当前 thread，不执行单窗口单所有权归一化。

- [ ] **Step 3: 让 owner remote 复用统一事务**

`handleCodexOwnerCommand` 的 remote 分支改为：

```go
case "remote":
	result, err := h.acquireCodexSessionWithBindingLocked(runtime.acquireRequest(threadID))
	if err != nil {
		return textNavigationResult(renderCodexSessionAcquireFailure(err))
	}
	return textNavigationResult(h.renderCodexSessionAcquireSuccess(result))
case "desktop":
	return textNavigationResult(h.releaseCodexOwnerToDesktop(runtime, threadID))
```

`codexSessionCommandRuntime.acquireRequest(threadID string)` 构造与 switch 相同的 actor/route/
platform/account/reply/task context。将旧 `handoffCodexOwner` 缩减为
`releaseCodexOwnerToDesktop`：只接受 desktop 目标，保留现有 thread 锁、active task
校验、handoff、CAS 与 resync。删除 `codexOwnerHandoffValidation.target` 和已无调用的
remote proposed 分支，控制 `codex_owner_command.go` 在 300 行内。

- [ ] **Step 4: 保持释放后的普通消息门禁**

`ensureCodexRouteOwnsControl` 的两条提示改为：

```go
case agent.CodexControlUnclaimed:
	return fmt.Errorf("当前 Codex 会话未由本窗口控制；请重新选择会话或发送 /cx owner remote")
case agent.CodexControlDesktop:
	return fmt.Errorf("当前 Codex 会话已归还 Codex Desktop；请重新选择会话或发送 /cx owner remote")
```

不在 `prepareCodexConversationRoute`、`ensureCodexRuntimeReady` 或普通消息入口中调用 acquire。

- [ ] **Step 5: 运行 owner、消息门禁、guide 和 stop 回归**

Run: `GOTOOLCHAIN=local GOPROXY=off go test -race ./messaging -run 'TestCodex(Owner|UnclaimedMessage|DesktopOwnedMessage|Reselect|SelectedDesktopTask|Guide|Stop)' -count=5 -timeout 60s`

Expected: PASS；显式释放后普通消息仍拒绝，只有重新选择或 owner remote 恢复权限。

- [ ] **Step 6: 提交**

```bash
git add messaging/codex_owner_command.go messaging/codex_runtime_binding.go \
  messaging/codex_owner_command_test.go messaging/codex_owner_message_test.go \
  messaging/handler_codex_external_task_test.go \
  messaging/handler_codex_external_control_test.go \
  messaging/handler_codex_live_message_control_test.go
git commit -m "收口 Codex 显式释放与远程控制"
```

## Task 9: 补齐平台、路由、并发与重启行为矩阵

**Files:**
- Test: `messaging/handler_platform_session_test.go`
- Test: `messaging/handler_codex_browse_feishu_test.go`
- Test: `messaging/handler_codex_session_test.go`
- Test: `messaging/handler_codex_binding_race_test.go`
- Test: `messaging/codex_remote_selection_store_test.go`

- [ ] **Step 1: 补平台与路由隔离测试**

增加以下精确用例：

- `TestCodexSelectionAcquiresForWeChatAndFeishu`：表驱动 `platform.PlatformWeChat` 和
  `platform.PlatformFeishu`，使用同一 live agent 行为契约；两者均在成功回复前得到
  remote owner。
- `TestFeishuSessionButtonAcquiresOriginalRouteOnly`：点击携带 session metadata 的
  `/cx switch <thread>` 按钮后，只更新卡片原 route 的 binding，不更新 actor 的私聊
  binding。
- `TestCodexReadOnlyCommandsDoNotChangeOwnership`：依次执行 `/cx ls`、`/cx pwd`、
  `/cx status`、`/cx whoami`，每次都断言 target revision、owner、route、conversation 不变。

- [ ] **Step 2: 补并发和重启一致性测试**

- `TestCodexRemoteSelectionConcurrentRoutesHaveOneIntegratedWinner`：两个不同
  `RouteBindingKey` 并发选择 B，只一个返回成功；失败方原 thread/owner 不变，
  B 的 persisted route 与成功方一致。
- `TestCodexRemoteSelectionReloadKeepsSingleRouteOwner`：成功 A→B 后从同一
  `codex-sessions.json` 新建 store，断言 active workspace/B/remote route 恢复，A 为 desktop，
  revision 与写盘前一致。
- `TestCodexAcquireAndTaskAdmissionSerializeOnBinding`：选择事务持有 binding 锁时
  新普通消息不能登记 active task；提交后普通消息使用 B 和新 owner revision。

- [ ] **Step 3: 高频运行状态矩阵**

Run: `GOTOOLCHAIN=local GOPROXY=off go test -race ./messaging -run 'TestCodex(SelectionAcquiresFor|ReadOnlyCommands|RemoteSelectionConcurrentRoutes|RemoteSelectionReload|AcquireAndTaskAdmission)|TestFeishuSessionButton' -count=20 -timeout 60s`

Expected: PASS；无 race、无双赢家、无重启后多 owner。

- [ ] **Step 4: 提交测试矩阵**

```bash
git add messaging/handler_platform_session_test.go \
  messaging/handler_codex_browse_feishu_test.go \
  messaging/handler_codex_session_test.go \
  messaging/handler_codex_binding_race_test.go \
  messaging/codex_remote_selection_store_test.go
git commit -m "补齐 Codex 选择接管行为矩阵"
```

## Task 10: 同步公开语义并执行全量 Review Gate

**Files:**
- Modify: `messaging/codex_session_status.go:178-196`
- Modify: `messaging/handler_command_status_test.go:249-264`
- Modify: `README_CN.md:11-65,141-147`
- Modify: `README.md:11-65,141-147`
- Modify: `docs/AI_CONTEXT.md:75-90`
- Modify: `tasks/lessons.md`
- Modify: `tasks/todo.md`

- [ ] **Step 1: 先改帮助文本测试**

`TestBuildCodexSessionHelpTextIncludesDescriptions` 断言帮助文本包含：

```text
/cx switch <编号> 切换当前工作空间会话并接管
/cx new 新建当前工作空间会话并接管
/cx owner remote 重新接管当前会话
/cx owner desktop 显式释放当前会话给 Codex Desktop
```

并断言不再出现“选择后还需发送 owner remote”的两步语义。

- [ ] **Step 2: 同步 README、上下文与 lessons**

- `README_CN.md` 将产品摘要改为“选择即接管，`owner desktop` 显式释放”；
  快速体验不再把 `/cx owner remote` 列为每次必做步骤，但在命令表中保留其
  “重新接管”兼容用途。
- `README.md` 同步为等价英文语义：selection takes ownership, `owner desktop`
  explicitly releases, `owner remote` reacquires。
- `docs/AI_CONTEXT.md` 记录真实入口集合、外层 binding 锁、有序 thread 锁、
  copy-on-write store 与 observer reservation 的事实。
- `tasks/lessons.md` 新增规则：“显式选择是 thread 权威状态，ACP 只能在
  session store 为空时回填；选择、唯一 owner 和 active observer 必须作为同一
  saga 验收”。
- `tasks/todo.md` 按任务 1–10 勾选状态和实际验证结果回填 Review 小结。

- [ ] **Step 3: 格式化并执行最小充分验证**

Run:

```bash
gofmt -w messaging/agent_conversation.go \
  messaging/codex_sessions.go messaging/codex_session_persistence.go \
  messaging/codex_remote_selection_store.go messaging/codex_session_locks.go \
  messaging/codex_external_task.go messaging/codex_external_task_reservation.go \
  messaging/codex_session_acquire.go messaging/codex_session_acquire_runtime.go \
  messaging/codex_session_switch.go messaging/codex_browser.go \
  messaging/codex_session_new.go messaging/codex_session_command_dispatch.go \
  messaging/default_session.go messaging/platform_commands.go \
  messaging/codex_owner_command.go messaging/codex_runtime_binding.go \
  messaging/codex_live_fakes_test.go messaging/codex_remote_selection_store_test.go \
  messaging/codex_session_locks_test.go messaging/codex_session_acquire_test.go \
  messaging/handler_codex_thread_state_test.go \
  messaging/handler_codex_external_task_test.go \
  messaging/handler_codex_external_control_test.go \
  messaging/handler_codex_live_switch_test.go \
  messaging/handler_codex_navigation_switch_test.go \
  messaging/handler_codex_browse_feishu_test.go \
  messaging/codex_short_navigation_card_test.go \
  messaging/handler_codex_browse_test.go messaging/handler_codex_session_test.go \
  messaging/handler_codex_binding_race_test.go \
  messaging/handler_claude_agent_binding_test.go messaging/handler_feishu_session_test.go \
  messaging/codex_owner_command_test.go messaging/codex_owner_message_test.go \
  messaging/handler_codex_live_message_control_test.go \
  messaging/handler_platform_session_test.go messaging/handler_command_status_test.go
GOTOOLCHAIN=local GOPROXY=off go test ./messaging ./agent -count=1 -timeout 60s
GOTOOLCHAIN=local GOPROXY=off go test -race ./messaging ./agent -count=1 -timeout 60s
GOTOOLCHAIN=local GOPROXY=off go test ./... -count=1 -timeout 60s
GOTOOLCHAIN=local GOPROXY=off go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

Expected: 全部 PASS；无 race、vet 诊断、文档契约错误或空白差异。

- [ ] **Step 4: 执行 `review-gate` 交付前审查**

实施会话必须读取并执行 `review-gate` skill，逐项核对：

- 设计文档第 4 节全部入口都有自动化保护。
- 所有失败路径均不留下半提交 store/runtime/observer。
- 跨窗口错误不泄露 route、conversation 或用户标识。
- 新函数、文件、嵌套、参数和圈复杂度满足本仓硬约束。
- `git diff --stat` 只包含本计划文件地图中列出的代码、测试与文档，没有无关模块变更。

若审查发现与已确认设计不一致，先停止执行、回到设计/Plan 修正并重新等待确认。

- [ ] **Step 5: 提交文档与验收记录**

```bash
git add messaging/codex_session_status.go messaging/handler_command_status_test.go \
  README_CN.md README.md docs/AI_CONTEXT.md tasks/lessons.md tasks/todo.md
git commit -m "同步 Codex 选择即接管语义"
```

## 实施顺序与并行边界

1. Task 1→5 严格串行：先清除 stale 回填，再建 store/锁/observer，最后组合 saga。
2. Task 6→8 严格串行：共享 session/runtime/active-task 文件，存在写冲突。
3. Task 9 只在生产语义稳定后补齐矩阵，不与 Task 6–8 并行改测试 fixture。
4. Task 10 最后串行同步文档、全量验证和 Review Gate。
5. 本计划实施阶段不拆并行写任务；只允许独立的测试命令并行运行，结果由主流程统一整合。

## 停止条件

- 任一 runtime handoff 超时后无法通过只读 inspect 确认唯一 writer。
- 任一补偿失败，无法证明持久化 intent 与 runtime 一致。
- 发现需要修改 Agent/Desktop IPC 公开契约或 `codex-sessions.json` schema。
- 新发现的入口会在未持有 binding 锁时调用 acquire/new 事务。

命中任一条时停止编码，先更新设计与本计划，再请用户重新确认。
