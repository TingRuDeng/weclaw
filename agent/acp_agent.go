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
)

// ACPAgent communicates with ACP-compatible agents (claude-agent-acp, codex-acp, cursor agent, etc.) via stdio JSON-RPC 2.0.
type ACPAgent struct {
	configuredName   string
	command          string
	localCommand     string
	args             []string
	model            string
	effort           string
	approvalPolicy   string
	approvalReviewer string
	sandboxMode      string
	systemPrompt     string
	cwd              string
	env              map[string]string
	runAs            runAsUserSpec
	protocol         string // "legacy_acp" or "codex_app_server"

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	// codexHostSocket is the stable Unix socket shared by every Codex frontend.
	// hostCmd/hostDone are only populated when this ACPAgent launched the host;
	// attaching to an already running host never gives this client lifecycle
	// ownership of the external process.
	codexHostSocket string
	hostCmd         *exec.Cmd
	hostDone        <-chan error
	started         bool
	starting        bool
	startDone       chan struct{}
	startErr        error
	nextID          atomic.Int64
	wireSequence    atomic.Uint64
	sessions        map[string]string // conversationID -> sessionID (legacy ACP)
	// pendingPersistedSessions 在标准 ACP 握手确认身份前隔离磁盘中的旧 session。
	pendingPersistedSessions map[string]string
	legacyRuntimeGeneration  uint64
	sessionGenerations       map[string]uint64 // conversationID -> legacy runtime generation
	bindingRevisions         map[string]uint64 // conversationID -> latest binding intent revision
	bindingRevisionCounter   uint64
	threads                  map[string]string // conversationID -> threadID (codex app-server)
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

	// pending tracks in-flight JSON-RPC requests
	pendingMu sync.Mutex
	pending   map[int64]chan *rpcResponse

	// notifications channel for session/update events
	notifyMu sync.Mutex
	notifyCh map[string]chan *sessionUpdate // sessionID -> channel
	turnCh   map[string]chan *codexTurnEvent

	unhandledLogMu sync.Mutex
	unhandledLogAt map[string]time.Time

	stderr *acpStderrWriter // captures stderr for error reporting

	// rpcCall allows tests to stub JSON-RPC interactions without a subprocess.
	rpcCall func(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
	// Claude quota hooks let tests replace local credential discovery and the fixed Anthropic request.
	claudeQuotaOAuthToken func(context.Context) (string, error)
	claudeQuotaOAuthQuery func(context.Context, string) (ClaudeQuota, error)

	desktopProbe              codexDesktopOwnerProbe
	codexOwners               *codexRuntimeOwnerRegistry
	desktopRuntime            *codexDesktopRuntime
	appServerGate             *codexAppServerGate
	restartCodexAppServerCall func(context.Context) error
}

// ACPAgentConfig holds configuration for the ACP agent.
type ACPAgentConfig struct {
	ConfiguredName   string   // 配置 map 中的 Agent 名称，用于稳定识别业务身份
	Command          string   // path to ACP agent binary (claude-agent-acp, codex-acp, cursor agent, etc.)
	LocalCommand     string   // 原生 Claude 命令，用于本地可见交接和账号额度查询回退
	Args             []string // extra args for command (e.g. ["acp"] for cursor)
	Model            string
	Effort           string
	ApprovalPolicy   string
	ApprovalReviewer string
	SandboxMode      string
	SystemPrompt     string
	Cwd              string            // working directory
	Env              map[string]string // extra environment variables
	StateFile        string            // optional persisted mapping file path
	AppServerSocket  string            // Codex app-server shared Unix socket; empty uses the WeClaw runtime directory
	RunAsUser        string            // 以独立 Unix 用户运行（文件系统隔离）
	RunAsEnv         []string          // run_as_user 时透传的环境变量名白名单
}
