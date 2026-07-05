package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const companionConnectWait = 800 * time.Millisecond

// CompanionAgentConfig 描述 WeClaw 后台侧的 Companion 代理配置。
type CompanionAgentConfig struct {
	Name       string
	Command    string
	Args       []string
	Cwd        string
	Env        map[string]string
	Model      string
	AutoLaunch bool
	Launch     CompanionLauncher
}

// CompanionLaunchRequest 描述需要在本机可见终端里启动的 companion 命令。
type CompanionLaunchRequest struct {
	Executable string
	Agent      string
	Cwd        string
}

// CompanionLauncher 负责打开本地可见终端并运行 companion 命令。
type CompanionLauncher func(context.Context, CompanionLaunchRequest) error

type pendingCompanionCall struct {
	onProgress func(string)
	response   chan companionResponse
}

// CompanionAgent 通过本地 loopback socket 把微信输入转发给可见终端里的 Companion。
type CompanionAgent struct {
	name       string
	command    string
	args       []string
	cwd        string
	env        map[string]string
	model      string
	autoLaunch bool
	launch     CompanionLauncher

	mu          sync.Mutex
	listener    net.Listener
	conn        net.Conn
	encoder     *json.Encoder
	endpoint    CompanionEndpoint
	connectedCh chan struct{}
	nextID      atomic.Int64
	pendingMu   sync.Mutex
	pending     map[string]*pendingCompanionCall
}

func NewCompanionAgent(cfg CompanionAgentConfig) *CompanionAgent {
	cwd := normalizeCompanionCwd(cfg.Cwd)
	name := cfg.Name
	if name == "" {
		name = "companion"
	}
	return &CompanionAgent{
		name:        name,
		command:     cfg.Command,
		args:        append([]string(nil), cfg.Args...),
		cwd:         cwd,
		env:         cfg.Env,
		model:       cfg.Model,
		autoLaunch:  cfg.AutoLaunch,
		launch:      cfg.Launch,
		connectedCh: make(chan struct{}),
		pending:     make(map[string]*pendingCompanionCall),
	}
}

func (a *CompanionAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.listener != nil {
		a.mu.Unlock()
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		a.mu.Unlock()
		return fmt.Errorf("start companion listener: %w", err)
	}
	token, err := buildCompanionToken()
	if err != nil {
		listener.Close()
		a.mu.Unlock()
		return err
	}
	endpoint := a.buildEndpoint(listener, token)
	if err := writeCompanionEndpoint(endpoint); err != nil {
		listener.Close()
		a.mu.Unlock()
		return err
	}
	a.listener = listener
	a.endpoint = endpoint
	a.mu.Unlock()

	go a.acceptLoop(ctx, listener, token)
	a.launchVisibleCompanion(ctx)
	return nil
}

func (a *CompanionAgent) launchVisibleCompanion(ctx context.Context) {
	if !a.autoLaunch {
		return
	}
	if err := a.OpenVisibleCompanion(ctx); err != nil {
		log.Printf("[companion] auto launch failed agent=%s cwd=%s: %v", a.name, a.cwd, err)
	}
}

// OpenVisibleCompanion 打开本地可见 companion，用于 /cx attach 等显式接手场景。
func (a *CompanionAgent) OpenVisibleCompanion(ctx context.Context) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable for companion launch: %w", err)
	}
	req := CompanionLaunchRequest{
		Executable: executable,
		Agent:      a.name,
		Cwd:        a.cwd,
	}
	launcher := a.launch
	if launcher == nil {
		launcher = LaunchCompanionTerminal
	}
	return launcher(ctx, req)
}

// DetachVisibleCompanion 断开当前可见 companion，但保留后台 endpoint，便于之后再次 attach。
func (a *CompanionAgent) DetachVisibleCompanion() bool {
	a.mu.Lock()
	conn := a.conn
	if conn == nil {
		a.mu.Unlock()
		return false
	}
	a.conn = nil
	a.encoder = nil
	a.connectedCh = make(chan struct{})
	a.mu.Unlock()

	_ = conn.Close()
	a.failPending("Companion 已断开")
	return true
}

func (a *CompanionAgent) buildEndpoint(listener net.Listener, token string) CompanionEndpoint {
	addr := listener.Addr().(*net.TCPAddr)
	return CompanionEndpoint{
		ProtocolVersion: companionProtocolVersion,
		Agent:           a.name,
		Host:            "127.0.0.1",
		Port:            addr.Port,
		Token:           token,
		Cwd:             a.cwd,
		Command:         a.command,
		Args:            append([]string(nil), a.args...),
		CreatedAt:       time.Now(),
	}
}

func (a *CompanionAgent) acceptLoop(ctx context.Context, listener net.Listener, token string) {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go a.handleConn(conn, token)
	}
}

func (a *CompanionAgent) handleConn(conn net.Conn, token string) {
	decoder := json.NewDecoder(conn)
	var hello companionEnvelope
	if err := decoder.Decode(&hello); err != nil {
		_ = conn.Close()
		return
	}
	if hello.Type != companionMessageHello || hello.Token != token {
		_ = conn.Close()
		return
	}
	a.setConn(conn)
	a.readLoop(conn, decoder)
}

func (a *CompanionAgent) setConn(conn net.Conn) {
	a.mu.Lock()
	if a.conn != nil && a.conn != conn {
		_ = a.conn.Close()
	}
	a.conn = conn
	a.encoder = json.NewEncoder(conn)
	select {
	case <-a.connectedCh:
	default:
		close(a.connectedCh)
	}
	a.mu.Unlock()
	log.Printf("[companion] connected agent=%s remote=%s", a.name, conn.RemoteAddr())
}

func (a *CompanionAgent) readLoop(conn net.Conn, decoder *json.Decoder) {
	for {
		var message companionEnvelope
		if err := decoder.Decode(&message); err != nil {
			a.clearConn(conn)
			return
		}
		a.handleMessage(message)
	}
}

func (a *CompanionAgent) handleMessage(message companionEnvelope) {
	if message.Type == companionMessageEvent && message.Event != nil {
		a.deliverProgress(message.ID, message.Event.Text)
		return
	}
	if message.Type != companionMessageResponse || message.Response == nil {
		return
	}
	call := a.takePending(message.ID)
	if call == nil {
		return
	}
	select {
	case call.response <- *message.Response:
	default:
	}
}

func (a *CompanionAgent) clearConn(conn net.Conn) {
	a.mu.Lock()
	if a.conn == conn {
		a.conn = nil
		a.encoder = nil
		a.connectedCh = make(chan struct{})
	}
	a.mu.Unlock()
	_ = conn.Close()
	a.failPending("Companion 连接已断开")
}
