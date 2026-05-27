package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const companionConnectWait = 800 * time.Millisecond

// CompanionAgentConfig 描述 WeClaw 后台侧的 Companion 代理配置。
type CompanionAgentConfig struct {
	Name    string
	Command string
	Args    []string
	Cwd     string
	Env     map[string]string
	Model   string
}

type pendingCompanionCall struct {
	onProgress func(string)
	response   chan companionResponse
}

// CompanionAgent 通过本地 loopback socket 把微信输入转发给可见终端里的 Companion。
type CompanionAgent struct {
	name    string
	command string
	args    []string
	cwd     string
	env     map[string]string
	model   string

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
	return nil
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

func (a *CompanionAgent) ResetSession(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (a *CompanionAgent) Info() AgentInfo {
	return AgentInfo{Name: a.name, Type: "companion", Model: a.model, Command: a.command}
}

func (a *CompanionAgent) SetCwd(cwd string) {
	a.mu.Lock()
	a.cwd = normalizeCompanionCwd(cwd)
	a.mu.Unlock()
}

func (a *CompanionAgent) Cwd() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cwd
}

func (a *CompanionAgent) Stop() {
	a.mu.Lock()
	listener := a.listener
	conn := a.conn
	a.listener = nil
	a.conn = nil
	a.encoder = nil
	cwd := a.cwd
	name := a.name
	a.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	removeCompanionEndpoint(name, cwd)
	a.failPending("Companion 已停止")
}

var _ Agent = (*CompanionAgent)(nil)
