package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/observability"
)

// ACPAgent communicates with ACP-compatible agents (claude-agent-acp, codex-acp, cursor agent, etc.) via stdio JSON-RPC 2.0.
type ACPAgent struct {
	configuredName   string
	command          string
	localCommand     string
	args             []string
	model            string
	effort           string
	serviceTier      string
	approvalPolicy   string
	approvalReviewer string
	sandboxMode      string
	systemPrompt     string
	cwd              string
	env              map[string]string
	runAs            runAsUserSpec
	protocol         string // "legacy_acp" or "codex_app_server"

	mu sync.Mutex
	// writeMu serializes outbound ACP frames without coupling blocking I/O to
	// runtime state. Stop must remain able to close stdin while a write stalls.
	writeMu sync.Mutex
	// wireDispatchMu serializes connection generation changes with inbound wire
	// dispatch, so an old app-server reader cannot mutate state after reconnect.
	wireDispatchMu sync.Mutex
	wireEpoch      uint64
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	scanner        *bufio.Scanner
	// acpProcessDone 由 readLoop 的唯一 Wait 所有者关闭；Stop/启动失败只
	// 负责发出终止信号并等待它，避免双重 Wait 或自然退出留下 zombie。
	acpProcessDone chan error
	// codexHostSocket is the stable Unix socket shared by every Codex frontend.
	// hostCmd/hostDone are only populated when this ACPAgent launched the host;
	// attaching to an already running host never gives this client lifecycle
	// ownership of the external process.
	codexHostSocket string
	// codexHostMode is resolved once during construction. "daemon" uses the
	// official Codex lifecycle and never falls back to a WeClaw-owned process;
	// "managed" preserves the compatibility backend.
	codexHostMode   string
	hostCmd         *exec.Cmd
	hostDone        <-chan error
	codexAutoUpdate string
	// Codex 兼容性启动错误只有连续出现后才触发更新，避免把瞬时锁争用或
	// IO 抖动误判为版本不兼容。更新时间用于抑制同一故障上的更新风暴。
	codexCompatibilityFailures  int
	codexLastAutoUpdateAt       time.Time
	codexCompatibilityRetryWait time.Duration
	// codexHostConnectTimeout is a test seam for the fixed production startup
	// deadline. It must be set before Start and remains immutable afterwards.
	codexHostConnectTimeout time.Duration
	started                 bool
	starting                bool
	startDone               chan struct{}
	startErr                error
	nextID                  atomic.Int64
	wireSequence            atomic.Uint64
	sessions                map[string]string // conversationID -> sessionID (legacy ACP)
	// pendingPersistedSessions 在标准 ACP 握手确认身份前隔离磁盘中的旧 session。
	pendingPersistedSessions   map[string]string
	legacyRuntimeGeneration    uint64
	sessionGenerations         map[string]uint64 // conversationID -> legacy runtime generation
	bindingRevisions           map[string]uint64 // conversationID -> latest binding intent revision
	bindingRevisionCounter     uint64
	threads                    map[string]string // conversationID -> threadID (codex app-server)
	codexThreadConfigs         map[string]CodexThreadConfig
	codexThreadConfigRevisions map[string]uint64
	codexThreadProviders       map[string]string
	// resumeOnFirstUse marks restored thread mappings that should trigger a
	// best-effort thread/resume call before first turn.
	resumeOnFirstUse      map[string]bool // conversationID -> resume needed
	conversationCwds      map[string]string
	stateFile             string // optional persisted state file path
	claudeModels          []ClaudeModel
	claudeSessionConfigs  map[string][]acpSessionConfigOption
	claudeConfigRevisions map[string]uint64
	capabilities          acpCapabilitySnapshot
	stateSaveMu           sync.Mutex
	claudeConfigMu        sync.Mutex
	claudeQuotaMu         sync.Mutex
	claudeHostControlMu   sync.Mutex
	claudeLoadedSessions  map[string]claudeLoadedSessionState
	claudeSessionCommands map[string]claudeSessionCommandState
	claudeSessionTitles   map[string]claudeSessionTitleState
	claudeCommandChanged  chan struct{}

	// pending tracks in-flight JSON-RPC requests without owning transport or
	// runtime lifecycle state.
	pending rpcPendingRegistry

	// notifications channel for session/update events
	notifyMu                sync.Mutex
	notifyCh                map[string]chan *sessionUpdate // sessionID -> channel
	turnCh                  map[string]chan *codexTurnEvent
	turnObservers           map[string]map[uint64]*codexTurnObserverMailbox
	turnObserverNext        uint64
	pendingTurnInteractions map[string]map[string]*codexTurnEvent

	codexThreadArchiveHandlerMu sync.RWMutex
	codexThreadArchivedHandler  func(string)
	codexThreadActivityMu       sync.RWMutex
	codexThreadActivityHandler  func(string)

	unhandledLogMu sync.Mutex
	unhandledLogAt map[string]time.Time

	stderr *acpStderrWriter // captures stderr for error reporting

	// rpcCall allows tests to stub JSON-RPC interactions without a subprocess.
	rpcCall func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
	// Claude quota hooks let tests replace local credential discovery and the fixed Anthropic request.
	claudeQuotaOAuthToken func(context.Context) (string, error)
	claudeQuotaOAuthQuery func(context.Context, string) (ClaudeQuota, error)

	desktopProbe   codexDesktopOwnerProbe
	codexOwners    *codexRuntimeOwnerRegistry
	desktopRuntime *codexDesktopRuntime
	// Desktop 协调能力与 Host 选择权必须分离：daemon 也需要通过
	// App IPC 探测 frontend 并回交 thread，但只有 auto 允许把 App Host
	// 设为全局写入权威。
	codexDesktopCoordination  bool
	codexDesktopHostSelection bool
	codexRuntimeMode          CodexRuntimeHolder
	appServerGate             *codexAppServerGate
	// codexAdmissionMu serializes turn preflight with host-level account
	// maintenance. A turn releases it only after holding both the app-server
	// permit and writer lease; account operations then either run before the
	// preflight or observe the admitted turn and fail busy.
	codexAdmissionMu           sync.Mutex
	codexAccountSafetyOnce     sync.Once
	restartCodexAppServerCall  func(context.Context) error
	codexAccountStoreCall      func() (*codexauth.Store, error)
	stopManagedHostCall        func(context.Context, string) error
	startManagedHostCall       func(context.Context, string) error
	updateHostIdentityCall     func(string, codexauth.Profile) error
	codexHostLockContendedCall func()
	codexCLIUpdaterCall        func(context.Context) (codexCLIUpdateResult, error)
	codexDaemonLifecycleCall   func(context.Context, string) (codexDaemonLifecycleOutput, error)
	codexDaemonMetadataCall    func(context.Context, codexDaemonLifecycleOutput, string) (codexHostMetadata, error)
	codexProviderMigrationCall func(context.Context, codexProviderMigrationRequest) (codexProviderMigrationResult, error)
	codexProviderReadCall      func(context.Context, string) (string, error)
	stopDesktopHostCall        func(context.Context) error
	startDesktopHostCall       func(context.Context) error
	protocolTrace              observability.ProtocolRecorder
}

// ACPAgentConfig holds configuration for the ACP agent.
type ACPAgentConfig struct {
	ConfiguredName     string   // 配置 map 中的 Agent 名称，用于稳定识别业务身份
	Command            string   // path to ACP agent binary (claude-agent-acp, codex-acp, cursor agent, etc.)
	LocalCommand       string   // 原生 Claude 命令，仅用于账号额度查询回退
	Args               []string // extra args for command (e.g. ["acp"] for cursor)
	Model              string
	Effort             string
	ApprovalPolicy     string
	ApprovalReviewer   string
	SandboxMode        string
	SystemPrompt       string
	Cwd                string                         // working directory
	Env                map[string]string              // extra environment variables
	StateFile          string                         // optional persisted mapping file path
	AppServerSocket    string                         // Codex app-server shared Unix socket; empty uses the WeClaw runtime directory
	CodexHostMode      string                         // auto / daemon / managed
	CodexAutoUpdate    string                         // Codex CLI 自动更新策略：off / incompatible
	CodexDesktopBridge bool                           // 启用本机 App IPC 协调；auto 及旧版省略 Host mode 可选择 App Host
	RunAsUser          string                         // 以独立 Unix 用户运行（文件系统隔离）
	RunAsEnv           []string                       // run_as_user 时透传的环境变量名白名单
	ProtocolTrace      observability.ProtocolRecorder // 显式启用的 Codex 线协议诊断记录器
}

func (a *ACPAgent) codexHostSocketSnapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.codexHostSocket
}

func (a *ACPAgent) stderrSnapshot() *acpStderrWriter {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stderr
}
