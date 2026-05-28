package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

const (
	codexAppHost            = "127.0.0.1"
	codexAppServerReadyWait = 20 * time.Second
	codexUpdateCheckConfig  = "check_for_update_on_startup"
)

type codexAppSessionClient interface {
	Connect(context.Context) error
	Initialize(context.Context) error
	StartThread(context.Context, string) (string, error)
	StartTurn(context.Context, string, string) (string, error)
	WaitTurn(context.Context, string, string, func(string)) (string, error)
	Close() error
}

// codexAppCompanionRuntime 通过 Codex app-server 支持可见本地 Codex 终端。
type codexAppCompanionRuntime struct {
	endpoint  agent.CompanionEndpoint
	serverURL string

	mu        sync.Mutex
	client    codexAppSessionClient
	serverCmd *exec.Cmd
	attachCmd *exec.Cmd
	threadID  string

	reservePortFn func() (int, error)
	startServerFn func(context.Context, int) error
	waitReadyFn   func(context.Context) error
	startAttachFn func(context.Context, string) error
	newClientFn   func(string) codexAppSessionClient
}

func newCodexAppCompanionRuntime(endpoint agent.CompanionEndpoint) *codexAppCompanionRuntime {
	runtime := &codexAppCompanionRuntime{endpoint: endpoint}
	runtime.reservePortFn = reserveLoopbackPort
	runtime.startServerFn = runtime.startServer
	runtime.waitReadyFn = runtime.waitServerReady
	runtime.startAttachFn = runtime.startVisibleAttach
	runtime.newClientFn = func(url string) codexAppSessionClient { return newCodexAppClient(url) }
	return runtime
}

func (r *codexAppCompanionRuntime) HandleCompanionRequest(ctx context.Context, req agent.CompanionRequest, progress func(string)) (string, error) {
	if err := r.ensureStarted(ctx); err != nil {
		return "", err
	}
	r.mu.Lock()
	client := r.client
	threadID := r.threadID
	r.mu.Unlock()

	turnID, err := client.StartTurn(ctx, threadID, req.Text)
	if err != nil {
		return "", err
	}
	reply, err := client.WaitTurn(ctx, threadID, turnID, progress)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply) == "" {
		return "", errors.New("Codex app-server 返回空回复")
	}
	return reply, nil
}

// Start 在 Companion 握手后立即启动 Codex 可见端，避免终端只显示 weclaw companion 空等。
func (r *codexAppCompanionRuntime) Start(ctx context.Context) error {
	return r.ensureStarted(ctx)
}

func (r *codexAppCompanionRuntime) ensureStarted(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil && r.threadID != "" {
		return nil
	}
	port, err := r.reservePortFn()
	if err != nil {
		return err
	}
	r.serverURL = fmt.Sprintf("ws://%s:%d", codexAppHost, port)
	if err := r.startServerFn(ctx, port); err != nil {
		return err
	}
	if err := r.waitReadyFn(ctx); err != nil {
		return err
	}
	client := r.newClientFn(r.serverURL)
	if err := client.Connect(ctx); err != nil {
		return err
	}
	if err := client.Initialize(ctx); err != nil {
		client.Close()
		return err
	}
	threadID, err := client.StartThread(ctx, r.endpoint.Cwd)
	if err != nil {
		client.Close()
		return err
	}
	if err := r.startAttachFn(ctx, r.serverURL); err != nil {
		client.Close()
		return err
	}
	r.client = client
	r.threadID = threadID
	return nil
}

func (r *codexAppCompanionRuntime) startServer(ctx context.Context, port int) error {
	cmd := exec.CommandContext(ctx, r.endpoint.Command, codexAppServerArgs(r.endpoint, port)...)
	cmd.Dir = r.endpoint.Cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Codex app-server 失败: %w", err)
	}
	r.serverCmd = cmd
	return nil
}

func (r *codexAppCompanionRuntime) waitServerReady(ctx context.Context) error {
	deadline := time.Now().Add(codexAppServerReadyWait)
	readyURL := strings.Replace(r.serverURL, "ws://", "http://", 1) + "/readyz"
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return errors.New("等待 Codex app-server 就绪超时")
}

func (r *codexAppCompanionRuntime) startVisibleAttach(ctx context.Context, url string) error {
	cmd := exec.CommandContext(ctx, r.endpoint.Command, codexAppAttachArgs(r.endpoint, url)...)
	cmd.Dir = r.endpoint.Cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Codex 可见终端失败: %w", err)
	}
	r.attachCmd = cmd
	go func() { _ = cmd.Wait() }()
	return nil
}

func codexAppServerArgs(endpoint agent.CompanionEndpoint, port int) []string {
	args := codexAppBaseArgs(endpoint.Args)
	return append(args, "app-server", "--listen", fmt.Sprintf("ws://%s:%d", codexAppHost, port))
}

func codexAppAttachArgs(endpoint agent.CompanionEndpoint, url string) []string {
	args := codexAppBaseArgs(endpoint.Args)
	return append(args, "--remote", url, "--cd", endpoint.Cwd)
}

func codexAppBaseArgs(args []string) []string {
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != "app-server" {
			cleaned = append(cleaned, args[i])
			continue
		}
		i = skipCodexAppServerArgs(args, i+1)
	}
	return codexAppArgsWithUpdateCheckDisabled(cleaned)
}

func codexAppArgsWithUpdateCheckDisabled(args []string) []string {
	if hasCodexUpdateCheckConfig(args) {
		return args
	}
	// Companion 模式由 WeClaw 管理 Codex 生命周期，避免启动升级提示阻塞可见终端。
	return append([]string{"-c", codexUpdateCheckConfig + "=false"}, args...)
}

func hasCodexUpdateCheckConfig(args []string) bool {
	prefix := codexUpdateCheckConfig + "="
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" || args[i] == "--config" {
			if i+1 < len(args) && strings.HasPrefix(args[i+1], prefix) {
				return true
			}
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-c"+prefix) || strings.HasPrefix(args[i], "--config="+prefix) {
			return true
		}
	}
	return false
}

func skipCodexAppServerArgs(args []string, start int) int {
	i := start
	for i < len(args) {
		if args[i] == "--listen" && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(args[i], "--listen=") {
			i++
			continue
		}
		break
	}
	return i - 1
}

func (r *codexAppCompanionRuntime) Close() error {
	r.mu.Lock()
	client := r.client
	attach := r.attachCmd
	server := r.serverCmd
	r.client = nil
	r.attachCmd = nil
	r.serverCmd = nil
	r.threadID = ""
	r.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	stopCommand(attach)
	stopCommand(server)
	return nil
}
