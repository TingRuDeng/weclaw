package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

const (
	openCodeHost            = "127.0.0.1"
	openCodeServerReadyWait = 20 * time.Second
)

type openCodeCompanionRuntime struct {
	endpoint  agent.CompanionEndpoint
	serverURL string
	client    *http.Client

	mu        sync.Mutex
	serverCmd *exec.Cmd
	attachCmd *exec.Cmd
	sessionID string
}

func newOpenCodeCompanionRuntime(endpoint agent.CompanionEndpoint) *openCodeCompanionRuntime {
	return &openCodeCompanionRuntime{
		endpoint: endpoint,
		client:   &http.Client{},
	}
}

func (r *openCodeCompanionRuntime) HandleCompanionRequest(ctx context.Context, req agent.CompanionRequest, progress func(string)) (string, error) {
	if err := r.ensureStarted(ctx); err != nil {
		return "", err
	}
	sessionID, err := r.ensureSession(ctx)
	if err != nil {
		return "", err
	}
	_ = r.selectVisibleSession(ctx, sessionID)

	resultCh := make(chan openCodeTurnResult, 1)
	collectCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.collectTurnEvents(collectCtx, sessionID, progress, resultCh)

	if err := r.promptAsync(ctx, sessionID, req.Text); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			return "", result.err
		}
		if strings.TrimSpace(result.text) == "" {
			return "", errors.New("OpenCode 返回空回复")
		}
		return result.text, nil
	}
}

func (r *openCodeCompanionRuntime) ensureStarted(ctx context.Context) error {
	r.mu.Lock()
	if r.serverCmd != nil {
		r.mu.Unlock()
		return nil
	}
	port, err := reserveLoopbackPort()
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.serverURL = fmt.Sprintf("http://%s:%d", openCodeHost, port)
	if err := r.startServerLocked(ctx, port); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	if err := r.waitServerReady(ctx); err != nil {
		return err
	}
	return r.startVisibleAttach(ctx)
}

func (r *openCodeCompanionRuntime) startServerLocked(ctx context.Context, port int) error {
	args := append([]string{}, r.endpoint.Args...)
	args = append(args, "serve", "--hostname", openCodeHost, "--port", fmt.Sprintf("%d", port))
	cmd := exec.CommandContext(ctx, r.endpoint.Command, args...)
	cmd.Dir = r.endpoint.Cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 OpenCode server 失败: %w", err)
	}
	r.serverCmd = cmd
	return nil
}

func (r *openCodeCompanionRuntime) waitServerReady(ctx context.Context) error {
	deadline := time.Now().Add(openCodeServerReadyWait)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.serverURL+"/doc", nil)
		resp, err := r.client.Do(req)
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
	return errors.New("等待 OpenCode server 就绪超时")
}

func (r *openCodeCompanionRuntime) startVisibleAttach(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attachCmd != nil {
		return nil
	}
	args := append([]string{}, r.endpoint.Args...)
	args = append(args, "attach", r.serverURL, "--dir", r.endpoint.Cwd)
	cmd := exec.CommandContext(ctx, r.endpoint.Command, args...)
	cmd.Dir = r.endpoint.Cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 OpenCode 可见终端失败: %w", err)
	}
	r.attachCmd = cmd
	go func() { _ = cmd.Wait() }()
	return nil
}

func (r *openCodeCompanionRuntime) ensureSession(ctx context.Context) (string, error) {
	r.mu.Lock()
	if r.sessionID != "" {
		defer r.mu.Unlock()
		return r.sessionID, nil
	}
	r.mu.Unlock()

	var session struct {
		ID string `json:"id"`
	}
	if err := r.doJSON(ctx, http.MethodPost, "/session", map[string]string{}, &session); err != nil {
		return "", err
	}
	if session.ID == "" {
		return "", errors.New("OpenCode session/create 返回空 session id")
	}
	r.mu.Lock()
	r.sessionID = session.ID
	r.mu.Unlock()
	return session.ID, nil
}

func (r *openCodeCompanionRuntime) selectVisibleSession(ctx context.Context, sessionID string) error {
	body := map[string]string{"sessionID": sessionID}
	var ignored bool
	return r.doJSON(ctx, http.MethodPost, "/tui/select-session", body, &ignored)
}

func (r *openCodeCompanionRuntime) promptAsync(ctx context.Context, sessionID string, text string) error {
	body := map[string]any{
		"parts": []map[string]string{{"type": "text", "text": text}},
	}
	path := "/session/" + url.PathEscape(sessionID) + "/prompt_async"
	return r.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (r *openCodeCompanionRuntime) doJSON(ctx context.Context, method string, path string, body any, out any) error {
	requestURL := r.withDirectory(path)
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("OpenCode API %s %s 请求失败: %s %s", method, path, resp.Status, strings.TrimSpace(string(detail)))
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (r *openCodeCompanionRuntime) withDirectory(path string) string {
	values := url.Values{}
	values.Set("directory", r.endpoint.Cwd)
	return r.serverURL + path + "?" + values.Encode()
}
