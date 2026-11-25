package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
)

const restartDrainTimeout = 60 * time.Second

var restartDrainHTTPClient = &http.Client{Timeout: restartDrainTimeout}

var errCoordinatedRestartUnsupported = errors.New("运行中的 WeClaw 不支持协调重启接口")

type runtimeDrainResponse struct {
	Status         string `json:"status"`
	Code           string `json:"code"`
	Draining       bool   `json:"draining"`
	ActiveTasks    int    `json:"active_tasks"`
	RemainingTasks int    `json:"remaining_tasks"`
	Message        string `json:"message"`
}

func beginRestartDrainWithConfig(ctx context.Context, force bool, cfg *config.Config) error {
	return beginRestartDrainWithControl(ctx, force, false, cfg)
}

func beginRestartDrainWithConfigOptions(
	ctx context.Context,
	force bool,
	cfg *config.Config,
) error {
	return beginRestartDrainWithControl(ctx, force, force, cfg)
}

func beginRestartDrainWithControl(
	ctx context.Context,
	forceDrain bool,
	forceTerminateCodex bool,
	cfg *config.Config,
) error {
	state, err := readRuntimeState()
	if err != nil || !processExists(state.PID) {
		return nil
	}
	endpoint, err := runtimeAPIURL(cfg.APIAddr, "/api/runtime/restart/prepare")
	if err != nil {
		return fmt.Errorf("无法连接安全重启排空入口: %w", err)
	}
	if forceDrain || forceTerminateCodex {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return fmt.Errorf("解析安全重启排空入口: %w", err)
		}
		query := parsed.Query()
		if forceTerminateCodex {
			query.Set("force", "true")
		} else if forceDrain {
			query.Set("force_drain", "true")
		}
		parsed.RawQuery = query.Encode()
		endpoint = parsed.String()
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
	if resp.StatusCode == http.StatusNotFound {
		return legacyRuntimeRestartError(state.Version)
	}
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
			if result.Code == "runtime_restart_host_outcome_unknown" {
				return fmt.Errorf("%w: %s", agent.ErrCodexRestartUnsafe, message)
			}
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("安全重启排空入口返回异常状态 %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || result.Status != "ok" || !result.Draining || result.ActiveTasks < 0 || result.RemainingTasks < 0 {
		return fmt.Errorf("安全重启排空入口返回异常状态 %d", resp.StatusCode)
	}
	return nil
}

func legacyRuntimeRestartError(version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "旧版本"
	}
	return fmt.Errorf(
		"%w（运行版本 %s），未停止任何进程；请先手动停止旧版 WeClaw 服务，确认任务与 Codex App、受控 CLI 已退出后再重试",
		errCoordinatedRestartUnsupported, version,
	)
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
