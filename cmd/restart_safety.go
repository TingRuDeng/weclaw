package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

const restartSafetyTimeout = 2 * time.Second
const restartDrainTimeout = 60 * time.Second

var restartSafetyHTTPClient = &http.Client{Timeout: restartSafetyTimeout}
var restartDrainHTTPClient = &http.Client{Timeout: restartDrainTimeout}

type runtimeStatusResponse struct {
	Status      string `json:"status"`
	ActiveTasks *int   `json:"active_tasks"`
}

type runtimeDrainResponse struct {
	Status         string `json:"status"`
	Draining       bool   `json:"draining"`
	ActiveTasks    int    `json:"active_tasks"`
	RemainingTasks int    `json:"remaining_tasks"`
	Message        string `json:"message"`
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
	activeTasks := *status.ActiveTasks
	if activeTasks == 0 {
		return nil
	}
	return fmt.Errorf("当前还有 %d 个运行中的任务，已取消重启；请等待完成或在飞书发送 /stop 后重试，如确认要中断可加 --force", activeTasks)
}

func beginRestartDrainWithConfig(ctx context.Context, force bool, cfg *config.Config) error {
	state, err := readRuntimeState()
	if err != nil || !processExists(state.PID) {
		return nil
	}
	endpoint, err := runtimeAPIURL(cfg.APIAddr, "/api/runtime/restart/prepare")
	if err != nil {
		return fmt.Errorf("无法连接安全重启排空入口: %w", err)
	}
	if force {
		endpoint += "?force=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	setRuntimeAPIToken(req, cfg.APIToken)
	resp, err := restartDrainHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("无法确认安全重启排空状态，已取消重启: %w", err)
	}
	defer resp.Body.Close()
	var result runtimeDrainResponse
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("安全重启排空响应无效: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("安全重启排空响应包含多余内容")
	}
	if resp.StatusCode == http.StatusConflict {
		if message := strings.TrimSpace(result.Message); message != "" {
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("当前还有 %d 个运行中的任务，已取消重启；请等待完成或在飞书发送 /stop 后重试，如确认要中断可加 --force", result.ActiveTasks)
	}
	if resp.StatusCode != http.StatusOK {
		if message := strings.TrimSpace(result.Message); message != "" {
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("安全重启排空入口返回异常状态 %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || result.Status != "ok" || !result.Draining || result.ActiveTasks < 0 || result.RemainingTasks < 0 {
		return fmt.Errorf("安全重启排空入口返回异常状态 %d", resp.StatusCode)
	}
	return nil
}

func cancelRestartDrain(ctx context.Context, cfg *config.Config) error {
	endpoint, err := runtimeAPIURL(cfg.APIAddr, "/api/runtime/restart/prepare")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	setRuntimeAPIToken(req, cfg.APIToken)
	resp, err := restartDrainHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result runtimeDrainResponse
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("恢复重启事务响应无效: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("恢复重启事务响应包含多余内容")
	}
	if resp.StatusCode != http.StatusOK || result.Status != "ok" || result.Draining {
		if message := strings.TrimSpace(result.Message); message != "" {
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("恢复重启事务返回异常状态 %d", resp.StatusCode)
	}
	return nil
}

func setRuntimeAPIToken(req *http.Request, token string) {
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("X-WeClaw-Token", token)
	}
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
	setRuntimeAPIToken(req, token)
	resp, err := restartSafetyHTTPClient.Do(req)
	if err != nil {
		return runtimeStatusResponse{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runtimeStatusResponse{}, false
	}
	var status runtimeStatusResponse
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&status); err != nil {
		return runtimeStatusResponse{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return runtimeStatusResponse{}, false
	}
	if status.Status != "ok" || status.ActiveTasks == nil || *status.ActiveTasks < 0 {
		return runtimeStatusResponse{}, false
	}
	return status, true
}

func runtimeStatusURL(apiAddr string) (string, error) {
	return runtimeAPIURL(apiAddr, "/api/runtime")
}

func runtimeAPIURL(apiAddr string, path string) (string, error) {
	requestPath, err := url.ParseRequestURI(path)
	if err != nil || requestPath.IsAbs() || !strings.HasPrefix(requestPath.Path, "/") {
		return "", fmt.Errorf("invalid runtime API path %q", path)
	}
	addr := strings.TrimSpace(apiAddr)
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		parsed, err := url.Parse(addr)
		if err != nil {
			return "", err
		}
		host, err := loopbackDialAddr(parsed.Host)
		if err != nil {
			return "", err
		}
		parsed.Host = host
		parsed.Path = requestPath.Path
		parsed.RawQuery = requestPath.RawQuery
		return parsed.String(), nil
	}
	host, err := loopbackDialAddr(addr)
	if err != nil {
		return "", err
	}
	return "http://" + host + requestPath.RequestURI(), nil
}

func loopbackDialAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.EqualFold(strings.TrimSpace(addr), "localhost") {
			return "127.0.0.1", nil
		}
		if isRuntimeLoopbackHost(addr) {
			return addr, nil
		}
		return "", fmt.Errorf("runtime API address %q is not loopback", addr)
	}
	host = strings.Trim(host, "[]")
	switch {
	case strings.EqualFold(host, "localhost"):
		return net.JoinHostPort("127.0.0.1", port), nil
	case host == "" || host == "0.0.0.0" || host == "::":
		return net.JoinHostPort("127.0.0.1", port), nil
	default:
		if isRuntimeLoopbackHost(host) {
			return addr, nil
		}
		return "", fmt.Errorf("runtime API address %q is not loopback", addr)
	}
}

func isRuntimeLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
