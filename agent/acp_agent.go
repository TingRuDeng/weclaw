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
	command        string
	args           []string
	model          string
	effort         string
	approvalPolicy string
	sandboxMode    string
	systemPrompt   string
	cwd            string
	env            map[string]string
	runAs          runAsUserSpec
	protocol       string // "legacy_acp" or "codex_app_server"

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	scanner  *bufio.Scanner
	started  bool
	nextID   atomic.Int64
	sessions map[string]string // conversationID -> sessionID (legacy ACP)
	threads  map[string]string // conversationID -> threadID (codex app-server)
	// resumeOnFirstUse marks restored thread mappings that should trigger a
	// best-effort thread/resume call before first turn.
	resumeOnFirstUse            map[string]bool // conversationID -> resume needed
	usageLimitRefreshOnNextTurn map[string]bool // conversationID -> refresh runtime before next turn
	conversationCwds            map[string]string
	stateFile                   string // optional persisted state file path
	history                     map[string][]acpHistoryMessage

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
}

// ACPAgentConfig holds configuration for the ACP agent.
type ACPAgentConfig struct {
	Command        string   // path to ACP agent binary (claude-agent-acp, codex-acp, cursor agent, etc.)
	Args           []string // extra args for command (e.g. ["acp"] for cursor)
	Model          string
	Effort         string
	ApprovalPolicy string
	SandboxMode    string
	SystemPrompt   string
	Cwd            string            // working directory
	Env            map[string]string // extra environment variables
	StateFile      string            // optional persisted mapping file path
	RunAsUser      string            // 以独立 Unix 用户运行（文件系统隔离）
	RunAsEnv       []string          // run_as_user 时透传的环境变量名白名单
}
