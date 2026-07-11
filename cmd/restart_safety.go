package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

const restartSafetyTimeout = 2 * time.Second

var restartSafetyHTTPClient = &http.Client{Timeout: restartSafetyTimeout}

type runtimeStatusResponse struct {
	ActiveTasks int `json:"active_tasks"`
}

type restartSafetyOptions struct {
	apiAddr       string
	apiToken      string
	processExists bool
	force         bool
}

// ensureConfiguredRestartSafe 从当前配置读取 API 地址，避免重启时直接杀掉飞书长任务。
func ensureConfiguredRestartSafe(ctx context.Context, force bool) error {
	state, err := readRuntimeState()
	if err != nil || !processExists(state.PID) {
		return nil
	}
	if force {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("无法读取当前配置以确认运行中任务状态，已取消重启；修复配置后重试，如确认要中断可加 --force: %w", err)
	}
	return ensureRestartSafe(ctx, restartSafetyOptions{
		apiAddr:       cfg.APIAddr,
		apiToken:      cfg.APIToken,
		processExists: true,
		force:         force,
	})
}

func ensureRestartSafe(ctx context.Context, opts restartSafetyOptions) error {
	if opts.force || !opts.processExists {
		return nil
	}
	status, ok := fetchRuntimeStatus(ctx, opts.apiAddr, opts.apiToken)
	if !ok {
		return fmt.Errorf("无法确认运行中任务状态，已取消重启；请检查 WeClaw API 和配置，如确认要中断可加 --force")
	}
	if status.ActiveTasks <= 0 {
		return nil
	}
	return fmt.Errorf("当前还有 %d 个运行中的任务，已取消重启；请等待完成或在飞书发送 /stop 后重试，如确认要中断可加 --force", status.ActiveTasks)
}

func fetchRuntimeStatus(ctx context.Context, apiAddr string, token string) (runtimeStatusResponse, bool) {
	endpoint, err := runtimeStatusURL(apiAddr)
	if err != nil {
		return runtimeStatusResponse{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return runtimeStatusResponse{}, false
	}
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("X-WeClaw-Token", token)
	}
	resp, err := restartSafetyHTTPClient.Do(req)
	if err != nil {
		return runtimeStatusResponse{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runtimeStatusResponse{}, false
	}
	var status runtimeStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return runtimeStatusResponse{}, false
	}
	return status, true
}

func runtimeStatusURL(apiAddr string) (string, error) {
	addr := strings.TrimSpace(apiAddr)
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		parsed, err := url.Parse(addr)
		if err != nil {
			return "", err
		}
		parsed.Host = loopbackDialAddr(parsed.Host)
		parsed.Path = "/api/runtime"
		parsed.RawQuery = ""
		return parsed.String(), nil
	}
	return "http://" + loopbackDialAddr(addr) + "/api/runtime", nil
}

func loopbackDialAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0", "::":
		return net.JoinHostPort("127.0.0.1", port)
	default:
		return addr
	}
}
