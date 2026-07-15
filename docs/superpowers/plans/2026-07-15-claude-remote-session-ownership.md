# Claude Remote Session Ownership Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Claude ACP session 增加唯一远程窗口所有权、选择即接管、显式本地释放和 fail-closed 任务门禁。

**Architecture:** `claudeSessionStore` 升级为同时持久化 route binding 与 session control intent；消息层在 route binding 锁外层、session 有序锁内执行选择/释放 saga。Claude ACP 继续是唯一远程运行时与目录来源，原生 Claude CLI 只接受协作式本地交接，不增加 transcript、进程或文件启发式探测。

**Tech Stack:** Go 1.26、ACP JSON-RPC、现有 `executionLock`、copy-on-write JSON 状态文件、飞书交互卡片、Go test/race/vet/staticcheck。

## Global Constraints

- 同一个 Claude session 同时最多由一个远程窗口控制；同一个远程窗口同时最多控制一个 Claude session。
- `/cc switch`、飞书会话卡片、`/cc new`、默认 Claude 的全局 `/new` 与 `/cc owner remote` 必须复用统一接管事务。
- `/cc cli` 必须先释放远程控制再打开 Terminal；`/cc owner local` 只释放，不打开 Terminal。
- `local` 与 `unclaimed` 状态下，普通消息和 session 配置写入必须 fail-closed。
- 不读取 `~/.claude` transcript，不扫描进程或文件锁，不声称观察或停止独立 Claude CLI 活跃任务。
- Claude 远程聊天保持 ACP-only；不重新引入 Claude CLI fallback。
- 错误不得泄露其他窗口的用户、平台、route、binding key 或 conversation ID。
- 不修改 Codex 所有权、Desktop runtime、Companion 和 rollout watcher 的行为。
- 新增生产 Go 文件控制在 300 行以内；只在职责确实独立时拆文件。
- 每个任务先完成 RED，再写最小实现，完成定向验证后单独提交。

## File Map

- `messaging/claude_control_store.go`：Claude control owner、intent、规范化和基础快照类型。
- `messaging/claude_sessions.go`：route binding 与 store 容器，保留现有绑定/status API。
- `messaging/claude_session_persistence.go`：v1/v2/v3 decode、迁移、原子持久化。
- `messaging/claude_remote_selection_store.go`：选择、释放、CAS、copy-on-write 和补偿快照。
- `messaging/claude_session_locks.go`：按 session ID 去重排序、共享预算加锁和逆序释放。
- `messaging/claude_session_acquire.go`：统一选择接管 saga、错误收敛和 runtime 补偿。
- `messaging/claude_session_release.go`：保留选择释放、清除选择释放与 workspace 切换。
- `messaging/claude_owner_command.go`：`/cc owner`、`remote`、`local`。
- `messaging/claude_workspace_handler.go`：目标解析、switch/new/cd 入口适配和结果渲染。
- `messaging/claude_cli_handler.go`：本地 CLI 交接 saga。
- `messaging/agent_conversation.go`、`messaging/agent_task.go`：普通消息与任务准入 owner/revision 门禁。
- `messaging/model_claude_session.go`：Claude session 配置写入 owner 门禁。
- `messaging/claude_render.go`：状态、帮助和所有权文案。
- `messaging/cwd_command.go`：全局 `/cwd` 清除 Claude 选择时同步释放 owner。
- `messaging/claude_feishu_cards.go`：继续回放 `/cc switch`，只补 owner 展示回归。
- `messaging/handler_claude_fakes_test.go`：并发安全的 Claude fake 与调用记录。
- `messaging/claude_*_test.go`、`messaging/handler_claude_*_test.go`：store、saga、入口、CLI、并发和重启回归。
- `README_CN.md`、`README.md`、`docs/AI_CONTEXT.md`、`tasks/lessons.md`、`tasks/todo.md`：公开语义与实施状态。

---

### Task 1: 升级 Claude 状态模型并完成 v2 安全迁移

**Files:**
- Create: `messaging/claude_control_store.go`
- Modify: `messaging/claude_sessions.go`
- Modify: `messaging/claude_session_persistence.go`
- Modify: `messaging/claude_session_persistence_test.go`
- Modify: `tasks/todo.md`

**Interfaces:**
- Produces: `claudeControlOwner`、`claudeControlIntent`、`claudeSessionStateVersion`。
- Produces: `controlIntent(sessionID string) claudeControlIntent`。
- Produces: `newClaudeSessionState(bindings map[string]claudeSessionBinding, controls map[string]claudeControlIntent) claudeSessionState`。
- Produces: `decodeClaudeSessionState(data []byte) (map[string]claudeSessionBinding, map[string]claudeControlIntent, bool, error)`。
- Consumes: `claudeBindingKey`、`buildClaudeConversationID`、`normalizeClaudeWorkspaceRoot`。

- [ ] **Step 1: 把当前任务记录切换为 Claude 治理并写迁移 RED 测试**

将 `tasks/todo.md` 的目标改为本计划，列出 Task 1–8，只有 Task 1 标记进行中。然后在
`messaging/claude_session_persistence_test.go` 增加：

```go
func TestClaudeSessionStoreMigratesV2SingleBindingToRemoteOwner(t *testing.T) {
	workspace := t.TempDir()
	key := claudeBindingKey("route-a", "claude")
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	state := map[string]any{
		"version": 2,
		"bindings": map[string]any{
			key: map[string]any{
				"workspace_root": workspace,
				"session_id": "session-a",
				"status": "ready",
				"updated_at": "2026-07-15T00:00:00Z",
			},
		},
		"updated": "2026-07-15T00:00:00Z",
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	intent := store.controlIntent("session-a")
	wantConversation := buildClaudeConversationID("route-a", "claude", workspace)
	if intent.Owner != claudeOwnerRemote || intent.BindingKey != key ||
		intent.ConversationID != wantConversation || intent.Revision != 1 {
		t.Fatalf("intent=%+v", intent)
	}
}

func TestClaudeSessionStoreMigratesV2ConflictingBindingsToUnclaimed(t *testing.T) {
	workspace := t.TempDir()
	bindings := map[string]claudeSessionBinding{
		claudeBindingKey("route-a", "claude"): newClaudeBinding(workspace, "session-shared", claudeBindingReady),
		claudeBindingKey("route-b", "claude"): newClaudeBinding(workspace, "session-shared", claudeBindingReady),
	}
	data, err := json.Marshal(claudeSessionState{Version: 2, Bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	decodedBindings, controls, migrated, err := decodeClaudeSessionState(data)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || len(decodedBindings) != 2 {
		t.Fatalf("migrated=%v bindings=%+v", migrated, decodedBindings)
	}
	intent := controls["session-shared"]
	if intent.Owner != claudeOwnerUnclaimed || intent.BindingKey != "" || intent.ConversationID != "" {
		t.Fatalf("intent=%+v", intent)
	}
}

func TestClaudeSessionStoreReloadPreservesLocalOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.controls["session-local"] = claudeControlIntent{Owner: claudeOwnerLocal, Revision: 4}
	state := newClaudeSessionState(store.bindings, store.controls)
	store.mu.Unlock()
	if err := persistClaudeSessionState(path, state); err != nil {
		t.Fatal(err)
	}
	reloaded := newClaudeSessionStore()
	if err := reloaded.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.controlIntent("session-local"); got.Owner != claudeOwnerLocal || got.Revision != 4 {
		t.Fatalf("intent=%+v", got)
	}
}
```

- [ ] **Step 2: 运行迁移测试确认 RED**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestClaudeSessionStore(MigratesV2|ReloadPreservesLocalOwner)' -count=1 -timeout 60s
```

Expected: FAIL，编译错误包含 `claudeOwnerRemote`、`controlIntent` 或新 decode 签名尚未定义。

- [ ] **Step 3: 增加 control 类型、v3 store 和迁移实现**

在 `messaging/claude_control_store.go` 定义：

```go
package messaging

import (
	"strings"
	"time"
)

const claudeSessionStateVersion = 3

type claudeControlOwner string

const (
	claudeOwnerUnclaimed claudeControlOwner = "unclaimed"
	claudeOwnerLocal     claudeControlOwner = "local"
	claudeOwnerRemote    claudeControlOwner = "remote"
)

type claudeControlIntent struct {
	Owner          claudeControlOwner `json:"owner"`
	BindingKey     string             `json:"binding_key,omitempty"`
	ConversationID string             `json:"conversation_id,omitempty"`
	Revision       uint64             `json:"revision"`
	UpdatedAt      string             `json:"updated_at"`
}

func normalizeClaudeControlIntent(intent claudeControlIntent) claudeControlIntent {
	intent.BindingKey = strings.TrimSpace(intent.BindingKey)
	intent.ConversationID = strings.TrimSpace(intent.ConversationID)
	switch intent.Owner {
	case claudeOwnerRemote:
		if intent.BindingKey == "" || intent.ConversationID == "" {
			return claudeControlIntent{Owner: claudeOwnerUnclaimed, Revision: intent.Revision, UpdatedAt: intent.UpdatedAt}
		}
	case claudeOwnerLocal, claudeOwnerUnclaimed:
		intent.BindingKey = ""
		intent.ConversationID = ""
	default:
		intent = claudeControlIntent{Owner: claudeOwnerUnclaimed, Revision: intent.Revision, UpdatedAt: intent.UpdatedAt}
	}
	return intent
}

func newMigratedClaudeControl(owner claudeControlOwner, key string, conversationID string, updatedAt string) claudeControlIntent {
	if strings.TrimSpace(updatedAt) == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return normalizeClaudeControlIntent(claudeControlIntent{
		Owner: owner, BindingKey: key, ConversationID: conversationID, Revision: 1, UpdatedAt: updatedAt,
	})
}
```

在 `claudeSessionState` 增加 `Controls`，在 `claudeSessionStore` 增加 `controls` 并于构造器初始化。
所有 `updateBindings` 持久化时复制并保留 controls，不能用空 map 覆盖所有权。

把 decode 改为四返回值：v1 迁移 controls 为空；v2 调用 `migrateClaudeControlsV2`；v3 规范化
bindings/controls。`migrateClaudeControlsV2` 必须先按 session ID 聚合有效 binding：唯一引用生成
remote，多个引用生成 unclaimed。加载完成后把 bindings 与 controls 一次发布到 store。

- [ ] **Step 4: 运行持久化包测试并修正所有旧调用点**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestClaudeSessionStore' -count=1 -timeout 60s
```

Expected: PASS，旧 v1/v2 测试与新增迁移测试全部通过。

- [ ] **Step 5: 提交状态模型**

```bash
git add messaging/claude_control_store.go messaging/claude_sessions.go messaging/claude_session_persistence.go messaging/claude_session_persistence_test.go tasks/todo.md
git commit -m "增加 Claude 会话控制状态"
```

---

### Task 2: 实现原子选择、释放和 session 有序锁

**Files:**
- Create: `messaging/claude_remote_selection_store.go`
- Create: `messaging/claude_remote_selection_store_test.go`
- Create: `messaging/claude_session_locks.go`
- Create: `messaging/claude_session_locks_test.go`
- Modify: `messaging/execution_lock.go`
- Modify: `tasks/todo.md`

**Interfaces:**
- Consumes: Task 1 的 `claudeControlIntent`、`claudeSessionStore.controls`。
- Produces: `claudeRemoteSelectionSnapshot`、`claudeRemoteSelectionUpdate`、`claudeRemoteReleaseUpdate`。
- Produces: `commitRemoteSelection`、`commitRemoteRelease`、`rollbackRemoteMutation`。
- Produces: `lockClaudeSessionControls(claudeSessionLockRequest) (func(), error)`。

- [ ] **Step 1: 写原子交换、CAS、写盘失败和有序锁 RED 测试**

在 `messaging/claude_remote_selection_store_test.go` 增加核心断言：

```go
func TestClaudeCommitRemoteSelectionAtomicallySwapsAForB(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("route-a", "claude", workspace), Revision: 3,
	}
	expected := store.remoteSelectionSnapshot(key, "session-b")
	mutation, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b",
		ConversationID: buildClaudeConversationID("route-a", "claude", workspace), Expected: expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.binding(key); got.SessionID != "session-b" {
		t.Fatalf("binding=%+v", got)
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal || got.Revision != 4 {
		t.Fatalf("old=%+v", got)
	}
	if got := store.controlIntent("session-b"); got.Owner != claudeOwnerRemote || got.BindingKey != key {
		t.Fatalf("target=%+v", got)
	}
	if mutation.Before.Bindings[key].SessionID != "session-a" || mutation.After.Bindings[key].SessionID != "session-b" {
		t.Fatalf("mutation=%+v", mutation)
	}
}

func TestClaudeCommitRemoteSelectionRejectsOtherRoute(t *testing.T) {
	store := newClaudeSessionStore()
	ownerKey := claudeBindingKey("route-owner", "claude")
	requestKey := claudeBindingKey("route-request", "claude")
	store.controls["session-b"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: ownerKey, ConversationID: "owner-conversation", Revision: 2,
	}
	expected := store.remoteSelectionSnapshot(requestKey, "session-b")
	_, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: requestKey, WorkspaceRoot: t.TempDir(), TargetSessionID: "session-b",
		ConversationID: "request-conversation", Expected: expected,
	})
	if !errors.Is(err, errClaudeRemoteSelectionOtherRoute) {
		t.Fatalf("error=%v", err)
	}
}

func TestClaudeCommitRemoteSelectionSaveFailureKeepsLiveState(t *testing.T) {
	store := newClaudeSessionStore()
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }
	expected := store.remoteSelectionSnapshot(key, "session-b")
	_, err := store.commitRemoteSelection(claudeRemoteSelectionUpdate{
		BindingKey: key, WorkspaceRoot: workspace, TargetSessionID: "session-b",
		ConversationID: "conversation-b", Expected: expected,
	})
	if err == nil || store.binding(key).SessionID != "session-a" {
		t.Fatalf("error=%v binding=%+v", err, store.binding(key))
	}
}
```

在 `messaging/claude_session_locks_test.go` 增加 A/B 与 B/A 并发获取，使用 waiter barrier 验证两边最终
都能完成且 `h.taskLocks` 回收为空；再增加 context 超时后已取得锁被逆序释放的测试。

- [ ] **Step 2: 运行 store/lock 测试确认 RED**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestClaude(CommitRemote|SessionLocks)' -count=1 -timeout 60s
```

Expected: FAIL，缺少 remote selection 类型、提交方法和 session lock API。

- [ ] **Step 3: 实现 copy-on-write mutation 与 CAS**

`messaging/claude_remote_selection_store.go` 定义以下稳定接口：

```go
var (
	errClaudeRemoteSelectionChanged    = errors.New("Claude 会话选择状态已变化")
	errClaudeRemoteSelectionOtherRoute = errors.New("Claude 会话由其他远程窗口控制")
)

type claudeSessionStoreImage struct {
	Bindings map[string]claudeSessionBinding
	Controls map[string]claudeControlIntent
}

type claudeRemoteSelectionSnapshot struct {
	TargetSessionID string
	Binding        claudeSessionBinding
	Target         claudeControlIntent
	RouteOwned     map[string]claudeControlIntent
}

type claudeRemoteSelectionUpdate struct {
	BindingKey     string
	WorkspaceRoot  string
	TargetSessionID string
	ConversationID string
	Expected       claudeRemoteSelectionSnapshot
}

type claudeRemoteReleaseUpdate struct {
	BindingKey    string
	WorkspaceRoot string
	KeepSelection bool
	Expected      claudeRemoteSelectionSnapshot
}

type claudeRemoteMutation struct {
	Before   claudeSessionStoreImage
	After    claudeSessionStoreImage
	Target   claudeControlIntent
	Released map[string]claudeControlIntent
}
```

实现规则：

1. `remoteSelectionSnapshot` 深拷贝 binding、target 和当前 binding key 的全部 remote controls。
2. `commitRemoteSelection` 在 `saveMu`、`mu` 内重新比较完整 expected；其他 route remote 直接返回专用错误。
3. 候选副本中先更新 binding，再把当前 route 的非目标 remote controls 变为 local，最后认领目标。
4. tuple 未变化时不递增 revision；发生 owner/binding/conversation 变化时递增一次。
5. `commitRemoteRelease` 把当前 route 的全部 remote controls 变为 local；`KeepSelection=false` 时把 binding
   更新为目标 workspace 的 unbound。
6. `rollbackRemoteMutation` 只在当前完整 store image 等于 `After` 时持久化 `Before`；不相等返回
   `errClaudeRemoteSelectionChanged`，不能覆盖并发新状态。

- [ ] **Step 4: 实现 session 有序锁**

在 `execution_lock.go` 增加：

```go
const claudeSessionControlExecutionPrefix = "claude-session-control\x00"

func (h *Handler) lockClaudeSessionControlContext(ctx context.Context, sessionID string) (func(), error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return func() {}, nil
	}
	return h.lockAgentExecutionContext(ctx, claudeSessionControlExecutionPrefix+sessionID)
}
```

在 `claude_session_locks.go` 实现 `sortedUniqueClaudeSessionIDs`、共享 context 预算、逐个获取和逆序释放；
等待预算复用 `codexSessionLockWaitTimeoutValue()`，因为它是 Handler 当前唯一的会话控制锁预算，不新增配置面。

- [ ] **Step 5: 运行定向测试和 race**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestClaude(CommitRemote|SessionLocks)' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test -race ./messaging -run 'TestClaude(CommitRemote|SessionLocks)' -count=10 -timeout 120s
```

Expected: 两条命令均 PASS，无 race、死锁或残留 execution lock。

- [ ] **Step 6: 提交原子 store 与锁**

```bash
git add messaging/claude_remote_selection_store.go messaging/claude_remote_selection_store_test.go messaging/claude_session_locks.go messaging/claude_session_locks_test.go messaging/execution_lock.go tasks/todo.md
git commit -m "增加 Claude 选择所有权原子存储"
```

---

### Task 3: 实现统一 Claude 选择接管 saga

**Files:**
- Create: `messaging/claude_session_acquire.go`
- Create: `messaging/claude_session_acquire_test.go`
- Modify: `messaging/claude_workspace_handler.go`
- Modify: `messaging/handler_claude_fakes_test.go`
- Modify: `messaging/handler_claude_acp_navigation_test.go`
- Modify: `tasks/todo.md`

**Interfaces:**
- Consumes: Task 2 的 snapshot、mutation、session locks。
- Produces: `acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest) (claudeSessionAcquireResult, error)`。
- Produces: `renderClaudeSessionAcquireFailure(error) string`。
- Produces: `ensureClaudeRuntimeSelection` 与 `rollbackClaudeSessionAcquire`。
- Test helper: `newClaudeAcquireRoute(context.Context, actorUserID, routeUserID, agentName string, agent.Agent, workspaceRoot string) claudeSessionRoute`。

- [ ] **Step 1: 写 A→B、跨 route、旧任务和补偿 RED 测试**

在 `messaging/claude_session_acquire_test.go` 建立 fixture，至少覆盖：

```go
func TestAcquireClaudeSessionReleasesAAndOwnsB(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("user-1", "claude", workspace), Revision: 1,
	}
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-b", Cwd: workspace}}
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(key))
	result, err := h.acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest{
		Route: route, Selected: fake.catalogSessions[0], Command: "switch",
	})
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	if result.Control.Owner != claudeOwnerRemote || store.binding(key).SessionID != "session-b" {
		t.Fatalf("result=%+v binding=%+v", result, store.binding(key))
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal {
		t.Fatalf("old=%+v", got)
	}
}

func TestAcquireClaudeSessionRejectsOtherRemoteRouteBeforeResume(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	ownerKey := claudeBindingKey("owner", "claude")
	h.ensureClaudeSessions().controls["session-b"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: ownerKey, ConversationID: "owner-conversation", Revision: 1,
	}
	route := newClaudeAcquireRoute(context.Background(), "request", "request", "claude", fake, workspace)
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(route.BindingKey))
	_, err := h.acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest{
		Route: route, Selected: agent.ClaudeSession{ID: "session-b", Cwd: workspace}, Command: "switch",
	})
	unlock()
	if !errors.Is(err, errClaudeRemoteSelectionOtherRoute) || len(fake.useCalls) != 0 {
		t.Fatalf("error=%v useCalls=%+v", err, fake.useCalls)
	}
}

func TestAcquireClaudeSessionStoreFailureRestoresOldRuntime(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	fake.sessionID = "session-a"
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-b", Cwd: workspace}}
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }
	route := newClaudeAcquireRoute(context.Background(), "user-1", "user-1", "claude", fake, workspace)
	unlock := h.lockAgentExecution(claudeBindingExecutionKey(key))
	_, err := h.acquireClaudeSessionWithBindingLocked(claudeSessionAcquireRequest{
		Route: route, Selected: fake.catalogSessions[0], Command: "switch",
	})
	unlock()
	if err == nil || fake.sessionID != "session-a" || store.binding(key).SessionID != "session-a" {
		t.Fatalf("error=%v runtime=%q binding=%+v", err, fake.sessionID, store.binding(key))
	}
}

func newClaudeAcquireRoute(ctx context.Context, actorUserID string, routeUserID string, agentName string, ag agent.Agent, workspaceRoot string) claudeSessionRoute {
	return claudeSessionRoute{
		Context: ctx, ActorUserID: actorUserID, UserID: routeUserID,
		AgentName: agentName, Agent: ag, WorkspaceRoot: workspaceRoot,
		BindingKey: claudeBindingKey(routeUserID, agentName),
	}
}
```

另加当前 route 旧 session 有 active task 时拒绝、同 session 同 owner 幂等不重复 resume、
`agentSessions.Set` 失败执行 store/runtime 补偿的测试。

- [ ] **Step 2: 运行 acquire 测试确认 RED**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestAcquireClaudeSession' -count=1 -timeout 60s
```

Expected: FAIL，统一 acquire 请求、结果和实现尚不存在。

- [ ] **Step 3: 实现 acquire 请求、锁内计划和错误分类**

定义：

```go
var (
	errClaudeSessionAcquireActiveOld = errors.New("当前 Claude 远程任务仍在执行")
	errClaudeSessionAcquireUncertain = errors.New("Claude 控制权移交结果未确认")
)

type claudeSessionAcquireRequest struct {
	Route    claudeSessionRoute
	Selected agent.ClaudeSession
	Command  string
}

type claudeSessionAcquireResult struct {
	Control      claudeControlIntent
	ConversationID string
	Mutation     claudeRemoteMutation
}
```

`acquireClaudeSessionWithBindingLocked` 必须：

1. 读取 initial snapshot，按 `claudeRemoteSelectionSessionIDs` 获取 session 锁。
2. 锁内重读 snapshot；锁集合变化返回 `errClaudeRemoteSelectionChanged`。
3. 其他 route owner 或当前 route 旧 remote conversation 存在 active task时，在任何 ACP 调用前拒绝。
4. 绑定目标 cwd；如果 `CurrentClaudeSession(conversationID)` 已是目标且 owner 已是当前 route，则跳过 resume，
   否则调用 `UseClaudeSession`。
5. `commitRemoteSelection` 只调用一次。
6. 调用 `agentSessions.Set`；失败时先 `rollbackRemoteMutation`，再恢复旧 ACP runtime。
7. 成功后清理 mutation 中已释放 session 的旧 conversation runtime mapping。

`renderClaudeSessionAcquireFailure` 对 other route、active old、CAS/lock timeout、uncertain 和默认错误分别返回
固定用户文案，日志保留内部 error，但回复不包含身份或内部 key。

- [ ] **Step 4: 让 `/cc switch` 只负责解析、调用 saga 和渲染**

保留 `handleClaudeSwitch` 的 binding 外层锁，把 `commitClaudeSelection` 替换为统一 acquire；成功首行改为
“已切换并接管 Claude 会话。”。删除不再使用的旧提交函数，但保留可被 acquire 补偿复用的
`rollbackClaudeRuntime`，并调整签名使其明确接收 previous binding。

- [ ] **Step 5: 运行 acquire、导航和 Agent 绑定回归**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'Test(AcquireClaudeSession|ClaudeSwitch|FeishuClaudeSessionSwitch|FailedClaudeSessionSwitch)' -count=1 -timeout 60s
```

Expected: PASS；失败切换保留旧 session、旧 owner 和旧默认 Agent。

- [ ] **Step 6: 提交统一接管 saga**

```bash
git add messaging/claude_session_acquire.go messaging/claude_session_acquire_test.go messaging/claude_workspace_handler.go messaging/handler_claude_fakes_test.go messaging/handler_claude_acp_navigation_test.go tasks/todo.md
git commit -m "实现 Claude 会话选择接管事务"
```

---

### Task 4: 收口工作空间切换与飞书入口

**Files:**
- Create: `messaging/claude_session_release.go`
- Create: `messaging/claude_session_release_test.go`
- Modify: `messaging/claude_workspace_handler.go`
- Modify: `messaging/cwd_command.go`
- Modify: `messaging/claude_feishu_cards.go`
- Modify: `messaging/handler_claude_session_test.go`
- Modify: `messaging/handler_claude_browse_feishu_test.go`
- Modify: `tasks/todo.md`

**Interfaces:**
- Consumes: Task 2 的 `commitRemoteRelease` 与 session locks。
- Produces: `releaseClaudeSelectionWithBindingLocked(claudeSessionReleaseRequest) (claudeRemoteMutation, error)`。
- Produces: `releaseClaudeSelectionForWorkspaceWithBindingLocked`。
- Produces: `releaseClaudeWorkspaceForUser(context.Context, string, string, agent.Agent, string) error`，供全局 `/cwd` 在修改 Agent cwd 前调用。

- [ ] **Step 1: 写 `/cc cd`、全局 `/cwd` 和飞书卡片 RED 测试**

增加测试证明：

```go
func TestClaudeCdReleasesSelectedRemoteSession(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("user-1", "claude", workspace), Revision: 1,
	}
	other := t.TempDir()
	fake.catalogSessions = []agent.ClaudeSession{{ID: "session-b", Cwd: other}}
	result := h.handleClaudeSessionCommandForRouteResult(context.Background(), "user-1", "user-1", true, "/cc cd 0")
	if result.Reply == "" || store.binding(key).SessionID != "" {
		t.Fatalf("result=%+v binding=%+v", result, store.binding(key))
	}
	if got := store.controlIntent("session-a"); got.Owner != claudeOwnerLocal {
		t.Fatalf("intent=%+v", got)
	}
}

func TestHandleCwdReleasesClaudeOwnerBeforeChangingRuntimeCwd(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	h.ensureClaudeSessions().bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	h.ensureClaudeSessions().controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("user-1", "claude", workspace), Revision: 1,
	}
	newWorkspace := t.TempDir()
	h.SetAllowedWorkspaceRoots([]string{newWorkspace})
	text := h.handleCwd("/cwd "+newWorkspace, "user-1")
	if !strings.Contains(text, canonicalTestPath(t, newWorkspace)) {
		t.Fatalf("text=%q", text)
	}
	if got := h.ensureClaudeSessions().controlIntent("session-a"); got.Owner != claudeOwnerLocal {
		t.Fatalf("intent=%+v fake=%+v", got, fake)
	}
}
```

更新飞书卡片测试，点击 `/cc switch session-b` 后断言 `session-b` remote、旧 session local；工作空间卡片
本身不取得 session owner。

- [ ] **Step 2: 运行 workspace/Feishu 测试确认 RED**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'Test(ClaudeCdReleases|HandleCwdReleasesClaude|FeishuClaude.*Owner)' -count=1 -timeout 60s
```

Expected: FAIL，workspace commit 仍只清 binding，没有同步 control。

- [ ] **Step 3: 实现统一 release helper**

定义：

```go
type claudeSessionReleaseRequest struct {
	Route         claudeSessionRoute
	WorkspaceRoot string
	KeepSelection bool
	Command       string
}
```

helper 在调用方持有 binding 锁时读取 current session、取得当前 route 全部 owned session 锁、检查 active task，
调用一次 `commitRemoteRelease`，成功后对 mutation.Released 中的旧 conversation 调用 `ClearClaudeSession`。
无 session 时 `KeepSelection=false` 仍原子更新 workspace unbound；`KeepSelection=true` 返回“当前没有会话”。

- [ ] **Step 4: 接入 `/cc cd` 和 `/cwd`**

`handleClaudeCdResult` 用 release helper 代替 `commitWorkspace`。

把 `handleCwdWithAccess` 调整为：持有全部 binding locks 后先执行 Claude release；任何 release 失败都在
`updateAgentWorkingDirectories` 前返回。成功后再更新 Agent cwd，并只为 Codex 记录 active workspace。
`recordActiveWorkspaceForUser` 不再调用 Claude `commitWorkspace`，避免第二次非事务写入。

- [ ] **Step 5: 运行 workspace 与平台回归**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'Test(ClaudeCd|HandleCwd|FeishuClaude)' -count=1 -timeout 60s
```

Expected: PASS；只读 `/cc ls` 不改 owner，`/cc cd` 与 `/cwd` 清选择时释放旧 owner。

- [ ] **Step 6: 提交 workspace/平台收口**

```bash
git add messaging/claude_session_release.go messaging/claude_session_release_test.go messaging/claude_workspace_handler.go messaging/cwd_command.go messaging/claude_feishu_cards.go messaging/handler_claude_session_test.go messaging/handler_claude_browse_feishu_test.go tasks/todo.md
git commit -m "收口 Claude 工作空间所有权释放"
```

---

### Task 5: 让 `/cc new` 与全局 `/new` 创建后原子接管

**Files:**
- Modify: `messaging/claude_workspace_handler.go`
- Create: `messaging/claude_session_new_test.go`
- Modify: `messaging/default_session.go`
- Modify: `messaging/handler_claude_acp_navigation_test.go`
- Modify: `messaging/handler_claude_agent_binding_test.go`
- Modify: `messaging/handler_feishu_session_new_test.go`
- Modify: `tasks/todo.md`

**Interfaces:**
- Consumes: Task 3 的统一 acquire saga。
- Produces: `createAndAcquireClaudeSessionWithBindingLocked(route claudeSessionRoute) (claudeSessionAcquireResult, error)`。

- [ ] **Step 1: 写创建成功、store 失败、默认 Agent 和孤立 session RED 测试**

至少增加：

```go
func TestClaudeNewCreatesAndOwnsSessionAtomically(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	fake.resetSessionID = "session-new"
	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
	key := claudeBindingKey("user-1", "claude")
	if !strings.Contains(text, "已创建并接管") {
		t.Fatalf("text=%q", text)
	}
	if got := h.ensureClaudeSessions().controlIntent("session-new"); got.Owner != claudeOwnerRemote || got.BindingKey != key {
		t.Fatalf("intent=%+v", got)
	}
	if h.ensureClaudeSessions().binding(key).WorkspaceRoot != workspace {
		t.Fatalf("binding=%+v", h.ensureClaudeSessions().binding(key))
	}
}

func TestClaudeNewStoreFailureLeavesCreatedSessionUnclaimedAndRestoresOld(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-old", claudeBindingReady)
	store.controls["session-old"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("user-1", "claude", workspace), Revision: 1,
	}
	fake.sessionID = "session-old"
	fake.resetSessionID = "session-new"
	store.persist = func(claudeSessionState) error { return errors.New("disk full") }
	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc new")
	if !strings.Contains(text, "失败") || store.binding(key).SessionID != "session-old" || fake.sessionID != "session-old" {
		t.Fatalf("text=%q binding=%+v runtime=%q", text, store.binding(key), fake.sessionID)
	}
	if got := store.controlIntent("session-new"); got.Owner == claudeOwnerRemote {
		t.Fatalf("orphan=%+v", got)
	}
}
```

全局 `/new` 与飞书 `/cc new` 测试都必须断言 owner，不只断言 binding。

- [ ] **Step 2: 运行新建入口测试确认 RED**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'Test(ClaudeNew|HandleGlobalNew|FeishuClaudeNew)' -count=1 -timeout 60s
```

Expected: FAIL，现有新建只提交 binding，不产生 remote control。

- [ ] **Step 3: 实现 create-and-acquire**

`createAndAcquireClaudeSessionWithBindingLocked` 在外层 binding 锁内：

1. 保存 previous binding。
2. 绑定目标 workspace cwd，调用一次 `ResetSession`。
3. 空 session ID 按失败处理。
4. 构造 `agent.ClaudeSession{ID: sessionID, Cwd: workspace}` 调用统一 acquire。
5. acquire 通过 `CurrentClaudeSession` 识别刚创建 runtime，不能额外调用 `session/resume`。
6. 任意失败调用已有 runtime rollback；新 session 保持无 remote owner。

`handleClaudeNew` 只负责外层锁、active task 拒绝、调用 helper 和渲染。默认 Claude 的全局 `/new`
继续通过 `resetDefaultClaudeSession` 进入同一函数，不复制创建逻辑。

- [ ] **Step 4: 运行新建、绑定和失败回归**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'Test(ClaudeNew|HandleGlobalNew|FeishuClaudeNew|ClaudeSwitchAgentSelectionSaveFailure)' -count=1 -timeout 60s
```

Expected: PASS；创建 RPC 只调用一次，失败恢复旧 runtime/binding/owner。

- [ ] **Step 5: 提交新建接管**

```bash
git add messaging/claude_workspace_handler.go messaging/claude_session_new_test.go messaging/default_session.go messaging/handler_claude_acp_navigation_test.go messaging/handler_claude_agent_binding_test.go messaging/handler_feishu_session_new_test.go tasks/todo.md
git commit -m "统一 Claude 新建会话接管"
```

---

### Task 6: 增加 owner 命令和所有远程写入门禁

**Files:**
- Create: `messaging/claude_owner_command.go`
- Create: `messaging/claude_owner_command_test.go`
- Modify: `messaging/claude_session_handler.go`
- Modify: `messaging/claude_render.go`
- Modify: `messaging/agent_conversation.go`
- Modify: `messaging/agent_task.go`
- Modify: `messaging/model_claude_session.go`
- Modify: `messaging/handler_claude_fakes_test.go`
- Modify: `messaging/handler_claude_acp_navigation_test.go`
- Modify: `messaging/agent_task_test.go`
- Modify: `messaging/model_command_claude_test.go`
- Modify: `tasks/todo.md`

**Interfaces:**
- Consumes: Task 3 acquire 与 Task 4 release。
- Produces: `handleClaudeOwnerCommand(route claudeSessionRoute, args []string) string`。
- Produces: `(*claudeSessionStore).requireRemoteControl(bindingKey string) (claudeSessionBinding, claudeControlIntent, error)`。
- Produces: `renderClaudeControlOwner`。

- [ ] **Step 1: 写 owner 状态、释放/重取和普通消息 RED 测试**

加入：

```go
func TestClaudeOwnerLocalBlocksNormalMessagesUntilRemoteReacquire(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	key := claudeBindingKey("user-1", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[key] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("user-1", "claude", workspace), Revision: 1,
	}
	text := h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner local")
	if !strings.Contains(text, "已释放") || store.controlIntent("session-a").Owner != claudeOwnerLocal {
		t.Fatalf("text=%q intent=%+v", text, store.controlIntent("session-a"))
	}
	_, err := h.resolveAgentConversationIDForRoute(context.Background(), "user-1", "user-1", "claude", fake)
	if err == nil || !strings.Contains(err.Error(), "/cc owner remote") {
		t.Fatalf("error=%v", err)
	}
	text = h.handleClaudeSessionCommand(context.Background(), "user-1", "/cc owner remote")
	if !strings.Contains(text, "已接管") || store.controlIntent("session-a").Owner != claudeOwnerRemote {
		t.Fatalf("text=%q intent=%+v", text, store.controlIntent("session-a"))
	}
}

func TestClaudeOtherRouteOwnerBlocksTaskAdmission(t *testing.T) {
	h, fake, workspace := newClaudeACPNavigationHandler(t)
	requestKey := claudeBindingKey("route-request", "claude")
	store := h.ensureClaudeSessions()
	store.bindings[requestKey] = newClaudeBinding(workspace, "session-a", claudeBindingReady)
	store.controls["session-a"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: claudeBindingKey("route-owner", "claude"),
		ConversationID: "owner-conversation", Revision: 1,
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.startAgentTask(agentTaskOptions{
		ctx: context.Background(), platformName: platform.PlatformFeishu,
		userID: "actor", routeUserID: "route-request", reply: reply,
		agentName: "claude", message: "blocked", agent: fake,
		progressCfg: config.DefaultProgressConfig(),
	})
	if _, active := h.activeTask(buildClaudeConversationID("route-request", "claude", workspace)); active {
		t.Fatal("other route owner must block admission")
	}
}
```

再加 `/cc owner` 不改变状态、local/other remote 下模型设置不调用 `SetClaudeSessionConfig`、控制 revision
在准入后变化时 prompt 不执行的测试。

- [ ] **Step 2: 运行 owner/门禁测试确认 RED**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestClaude(Owner|OtherRouteOwner|.*ControlRevision|.*Model.*Owner)' -count=1 -timeout 60s
```

Expected: FAIL，owner 命令和 owner-aware 写入门禁尚不存在。

- [ ] **Step 3: 接入 `/cc owner` 命令**

在 command token 白名单加入 `owner`；路由规则：

```go
case "owner":
	return textNavigationResult(h.handleClaudeOwnerCommand(route, fields[2:]))
```

`handleClaudeOwnerCommand`：

- 无参数：展示 session、owner、恢复状态和活动任务。
- `local`：持有 binding 锁，调用 `releaseClaudeSelectionWithBindingLocked`，保留选择。
- `remote`：从 ACP catalog 查找当前绑定 session，调用统一 acquire。
- 其他参数：返回 `用法: /cc owner [remote|local]`。

local 已释放时幂等；非当前 remote route 尝试 local 释放时拒绝。活动任务或暂存消息存在时拒绝。

- [ ] **Step 4: 普通消息、任务和模型写入统一校验 owner**

实现：

```go
var (
	errClaudeSessionUnbound        = errors.New("Claude 会话未绑定")
	errClaudeSessionNotRemoteOwner = errors.New("当前窗口没有 Claude 远程控制权")
)

func (s *claudeSessionStore) requireRemoteControl(bindingKey string) (claudeSessionBinding, claudeControlIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := s.bindings[bindingKey]
	if strings.TrimSpace(binding.SessionID) == "" {
		return binding, claudeControlIntent{}, errClaudeSessionUnbound
	}
	intent := normalizeClaudeControlIntent(s.controls[binding.SessionID])
	if intent.Owner == claudeOwnerRemote && intent.BindingKey == bindingKey {
		return binding, intent, nil
	}
	if intent.Owner == claudeOwnerRemote {
		return binding, intent, errClaudeRemoteSelectionOtherRoute
	}
	return binding, intent, errClaudeSessionNotRemoteOwner
}

type claudeTaskControlSnapshot struct {
	SessionID string
	Revision  uint64
}
```

`resolveClaudeConversationIDForRoute` 在 resume 前调用该方法；local/unclaimed/other route 返回可操作且不泄露身份的错误。

在 `agentTaskOptions` 增加 `claudeControl claudeTaskControlSnapshot`。`startAgentTask` 在登记任务前读取
session ID/revision 并写入该字段；`runAgentTask` 在 prompt 前重新调用 `requireRemoteControl`，并比较
`binding.SessionID` 与 `intent.Revision`。释放或切换因 active task 被拒绝，revision 复核是最后一道竞态门禁。

`setCurrentClaudeSessionSetting` 在调用 config Agent 前使用同一 owner 校验；只读状态与模型列表不变。

- [ ] **Step 5: 更新状态、帮助和列表所有权文案**

`/cc status` 与 `/cc owner` 使用：

```go
func renderClaudeControlOwner(intent claudeControlIntent, bindingKey string) string {
	switch {
	case intent.Owner == claudeOwnerRemote && intent.BindingKey == bindingKey:
		return "当前远程窗口"
	case intent.Owner == claudeOwnerRemote:
		return "其他远程窗口"
	case intent.Owner == claudeOwnerLocal:
		return "本地 Claude CLI"
	default:
		return "未认领"
	}
}
```

帮助文本加入 `/cc owner [remote|local]`，明确 local 后普通消息会拒绝、重新接管前应结束本地 CLI。

- [ ] **Step 6: 运行 owner、任务、模型和现有队列回归**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'Test(ClaudeOwner|ClaudeOtherRouteOwner|ClaudeTask|AgentTask|FeishuClaudeModel|ClaudeSettings)' -count=1 -timeout 60s
```

Expected: PASS；owner 失败不登记任务、不调用 prompt、不修改模型。

- [ ] **Step 7: 提交 owner 与写入门禁**

```bash
git add messaging/claude_owner_command.go messaging/claude_owner_command_test.go messaging/claude_session_handler.go messaging/claude_render.go messaging/agent_conversation.go messaging/agent_task.go messaging/model_claude_session.go messaging/handler_claude_fakes_test.go messaging/handler_claude_acp_navigation_test.go messaging/agent_task_test.go messaging/model_command_claude_test.go tasks/todo.md
git commit -m "增加 Claude 会话所有权命令与门禁"
```

---

### Task 7: 把 `/cc cli` 改为可补偿的本地交接 saga

**Files:**
- Modify: `messaging/claude_cli_handler.go`
- Modify: `messaging/handler_claude_entry_test.go`
- Modify: `messaging/claude_owner_command_test.go`
- Modify: `tasks/todo.md`

**Interfaces:**
- Consumes: Task 4 release 与 Task 3 acquire。
- Produces: `handoffClaudeSessionToCLIWithBindingLocked(route claudeSessionRoute) error`。

- [ ] **Step 1: 把现有 CLI 测试改成释放语义并增加补偿 RED**

修改成功测试断言：opener 调用时 control 已是 local、runtime mapping 已清理；opener 返回后普通任务不能登记。
增加：

```go
func TestHandleClaudeCLIOpenerFailureRestoresRemoteOwner(t *testing.T) {
	workspace := t.TempDir()
	h, route := newClaudeCLIEntryRoute(t, workspace, "session-current")
	store := h.ensureClaudeSessions()
	store.controls["session-current"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: route.BindingKey,
		ConversationID: buildClaudeConversationID(route.UserID, route.AgentName, workspace), Revision: 1,
	}
	h.SetClaudeCLIResumeOpener(func(context.Context, ClaudeCLIResumeRequest) error {
		return errors.New("terminal unavailable")
	})
	reply := h.handleClaudeCLI(route)
	intent := store.controlIntent("session-current")
	if !strings.Contains(reply, "打开 Claude CLI 失败") || intent.Owner != claudeOwnerRemote ||
		intent.BindingKey != route.BindingKey {
		t.Fatalf("reply=%q intent=%+v", reply, intent)
	}
}

func TestHandleClaudeCLICompensationFailureStaysFailClosed(t *testing.T) {
	workspace := t.TempDir()
	h, route := newClaudeCLIEntryRoute(t, workspace, "session-current")
	store := h.ensureClaudeSessions()
	store.controls["session-current"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: route.BindingKey,
		ConversationID: buildClaudeConversationID(route.UserID, route.AgentName, workspace), Revision: 1,
	}
	ag := route.Agent.(*fakeClaudeSessionAgent)
	ag.catalogSessions = []agent.ClaudeSession{{ID: "session-current", Cwd: workspace}}
	ag.useErr = errors.New("resume failed")
	h.SetClaudeCLIResumeOpener(func(context.Context, ClaudeCLIResumeRequest) error {
		return errors.New("terminal unavailable")
	})
	reply := h.handleClaudeCLI(route)
	if !strings.Contains(reply, "远程恢复未确认") || store.controlIntent("session-current").Owner == claudeOwnerRemote {
		t.Fatalf("reply=%q intent=%+v", reply, store.controlIntent("session-current"))
	}
}
```

- [ ] **Step 2: 运行 CLI 测试确认 RED**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestHandleClaudeCLI' -count=1 -timeout 60s
```

Expected: FAIL，当前 `/cc cli` 不改变 owner，成功后仍允许任务。

- [ ] **Step 3: 实现 release→clear runtime→open→compensate**

`handleClaudeCLI` 继续持有 binding 外层锁并完成现有 workspace/session/command 校验。随后：

1. 调用 `releaseClaudeSelectionWithBindingLocked`，`KeepSelection=true`。
2. 确认 mutation 后 owner 为 local。
3. 调用 opener。
4. opener 成功返回“已释放远程控制并打开 Claude CLI”。
5. opener 失败时，从当前 binding 构造 selected session，调用统一 acquire 恢复 remote。
6. acquire 失败时保持 local/unclaimed，返回 errors.Join 后的固定 fail-closed 文案。

禁止在 opener 失败后直接写 control map；恢复必须走 ACP resume 与统一 acquire，确保 remote owner 不会在
runtime 仍未恢复时提前发布。

- [ ] **Step 4: 运行 CLI、owner、任务竞态回归**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'Test(HandleClaudeCLI|ClaudeOwner|ClaudeTask)' -count=1 -timeout 60s
```

Expected: PASS；成功交接后任务拒绝，明确 opener 失败可恢复，补偿失败保持非 remote。

- [ ] **Step 5: 提交本地交接 saga**

```bash
git add messaging/claude_cli_handler.go messaging/handler_claude_entry_test.go messaging/claude_owner_command_test.go tasks/todo.md
git commit -m "收口 Claude CLI 本地交接所有权"
```

---

### Task 8: 补齐并发、重启、平台矩阵、文档和 Review Gate

**Files:**
- Create: `messaging/claude_selection_concurrency_test.go`
- Create: `messaging/claude_selection_route_matrix_test.go`
- Modify: `messaging/claude_session_persistence_test.go`
- Modify: `messaging/handler_claude_agent_binding_test.go`
- Modify: `messaging/handler_claude_browse_feishu_test.go`
- Modify: `README_CN.md`
- Modify: `README.md`
- Modify: `docs/AI_CONTEXT.md`
- Modify: `tasks/lessons.md`
- Modify: `tasks/todo.md`

**Interfaces:**
- Consumes: Task 1–7 的最终行为。
- Produces: 完整平台/route/并发/重启证据与公开语义。

- [ ] **Step 1: 写双 route 单赢家和重启行为测试**

`messaging/claude_selection_concurrency_test.go` 使用两个 route 同时选择 `session-shared`，fake 的
`UseClaudeSession` 允许两个调用都到达 store CAS；最终断言一条成功、一条返回其他远程窗口或 CAS 冲突，
store 只有一个 remote owner。测试循环 20 次并在 race 模式执行。

`messaging/claude_session_persistence_test.go` 增加：

```go
func TestClaudeRemoteAndLocalOwnersSurviveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-sessions.json")
	store := newClaudeSessionStore()
	if err := store.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	key := claudeBindingKey("route-a", "claude")
	workspace := t.TempDir()
	store.bindings[key] = newClaudeBinding(workspace, "session-remote", claudeBindingReady)
	store.controls["session-remote"] = claudeControlIntent{
		Owner: claudeOwnerRemote, BindingKey: key,
		ConversationID: buildClaudeConversationID("route-a", "claude", workspace), Revision: 7,
	}
	store.controls["session-local"] = claudeControlIntent{Owner: claudeOwnerLocal, Revision: 3}
	if err := persistClaudeSessionState(path, newClaudeSessionState(store.bindings, store.controls)); err != nil {
		t.Fatal(err)
	}
	reloaded := newClaudeSessionStore()
	if err := reloaded.SetFilePath(path); err != nil {
		t.Fatal(err)
	}
	if reloaded.controlIntent("session-remote").BindingKey != key ||
		reloaded.controlIntent("session-local").Owner != claudeOwnerLocal {
		t.Fatalf("remote=%+v local=%+v", reloaded.controlIntent("session-remote"), reloaded.controlIntent("session-local"))
	}
}
```

- [ ] **Step 2: 写入口/route 行为矩阵**

矩阵必须逐项验证：

- 微信 `/cc switch`。
- 飞书卡片 `/cc switch <sessionId>`。
- `/cc new`。
- 默认 Claude 的全局 `/new`。
- `/cc owner remote`。
- `/cc owner local`。
- `/cc cli`。
- `/cc cd` 与 `/cwd`。
- `/cc ls`、`/cc pwd`、`/cc status`、模型列表只读不改 owner。
- 另一个 route 控制时的错误不包含 route/user/platform 原文。

每个写入口断言 binding、owner、revision、Agent 调用次数和旧 session 释放；每个只读入口断言完整 control
快照前后相等。

- [ ] **Step 3: 运行矩阵与 race，处理真实失败**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestClaude(SelectionConcurrency|SelectionRouteMatrix|RemoteAndLocalOwners)' -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go test -race ./messaging -run 'TestClaude(SelectionConcurrency|SelectionRouteMatrix)' -count=20 -timeout 180s
```

Expected: PASS；任何 race、死锁、双赢家或状态泄漏都必须修复根因，不能降低循环次数或放宽断言。

- [ ] **Step 4: 同步帮助、README、AI 上下文和 lessons**

公开文案必须明确：

- Claude 选择或新建即由当前远程窗口接管。
- `/cc owner local` 显式释放，`/cc owner remote` 重新接管。
- `/cc cli` 会先释放远程控制；本地 CLI 结束前不要重新接管。
- 独立 Claude CLI 活跃任务不提供观察、进度回传、`/guide` 或远程停止。
- `session/list` 仍是目录事实源，control intent 是 WeClaw 远程写入事实源。
- v2 冲突迁移为 unclaimed，不静默选赢家。

`tasks/todo.md` 标记 Task 1–8 完成并记录验证；`tasks/lessons.md` 只沉淀可复用所有权规则，不写提交流水账。

- [ ] **Step 5: 执行 scoped 与全量验证**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go test ./agent -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go test -race ./messaging ./agent -count=1 -timeout 180s
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
staticcheck ./...
GOCACHE=/private/tmp/weclaw-go-cache go build ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

Expected: 全部退出码 0。若环境限制本地监听或状态目录写入，保留原始错误并按仓库权限规则在允许环境
重跑同一命令；不得用跳过测试或删除断言规避。

- [ ] **Step 6: 复核覆盖率与实际 diff**

Run:

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -coverprofile=/private/tmp/weclaw-claude-owner-cover.out -count=1 -timeout 120s
go tool cover -func=/private/tmp/weclaw-claude-owner-cover.out
git diff --stat bfafe9a..HEAD
git diff --check bfafe9a..HEAD
git status --short
```

Expected: Claude owner/store/acquire/release 新增核心函数的语句覆盖率逐函数不低于 80%；diff 只包含本计划
文件，无调试残留、绝对机器路径或敏感信息，工作树只保留准备提交的文档尾项。

- [ ] **Step 7: 使用 `review-gate` 完成交付审查**

审查必须逐项核对 Spec 目标、非目标、所有权不变量、补偿失败、跨 route 信息隔离、测试证据、复杂度和
文档同步。发现 Critical、Important 或 Minor 行为问题时继续修复并重跑受影响验证；只有无阻止交付问题
时才能记录“通过”。

- [ ] **Step 8: 提交最终矩阵和文档**

```bash
git add messaging/claude_selection_concurrency_test.go messaging/claude_selection_route_matrix_test.go messaging/claude_session_persistence_test.go messaging/handler_claude_agent_binding_test.go messaging/handler_claude_browse_feishu_test.go README_CN.md README.md docs/AI_CONTEXT.md tasks/lessons.md tasks/todo.md
git commit -m "完成 Claude 选择即接管治理"
```

提交后重新运行 `git status --short`，必须为空；不得在本计划内发布、推送或创建 PR，除非用户另行要求。
