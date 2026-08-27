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

const restartSafetyTimeout = 2 * time.Second
const restartDrainTimeout = 60 * time.Second

var restartSafetyHTTPClient = &http.Client{Timeout: restartSafetyTimeout}
var restartDrainHTTPClient = &http.Client{Timeout: restartDrainTimeout}

var errCoordinatedRestartUnsupported = errors.New("运行中的 WeClaw 不支持协调重启接口")

type runtimeStatusResponse struct {
	Status      string `json:"status"`
	ActiveTasks *int   `json:"active_tasks"`
}

type runtimeDrainResponse struct {
	Status         string `json:"status"`
	Code           string `json:"code"`
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
	return beginRestartDrainWithControl(ctx, force, false, false, cfg)
}

func beginRestartDrainWithConfigOptions(
	ctx context.Context,
	force bool,
	stopConflictingCodexHosts bool,
	cfg *config.Config,
) error {
	return beginRestartDrainWithControl(ctx, force, stopConflictingCodexHosts, force, cfg)
}

func beginRestartDrainWithControl(
	ctx context.Context,
	forceDrain bool,
	stopConflictingCodexHosts bool,
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
	if forceDrain || stopConflictingCodexHosts || forceTerminateCodex {
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
		if stopConflictingCodexHosts {
			query.Set("stop_conflicting_codex_hosts", "true")
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
		"%w（运行版本 %s），未停止任何进程；为保持 Codex Host 单写入，不能降级为普通重启。"+
			"请先等待所有任务完成并完整退出 Codex App、受控 CLI，然后依次执行 weclaw stop、weclaw start、weclaw restart 完成一次性迁移",
		errCoordinatedRestartUnsupported, version,
	)
}

// stopLegacyRuntime 完成旧服务的一次性迁移停止。旧服务不认识
// /api/runtime/restart/prepare，不能假装已经完成 Codex Host 轮换；但它仍
// 支持 runtime/drain，因此先关闭消息准入并确认 WeClaw 任务为空，再停止
// WeClaw 自身。Codex Host 保留到新服务启动后由正式 restart 事务处理。
func stopLegacyRuntime(ctx context.Context, cfg *config.Config, stop func() error) error {
	if cfg == nil {
		return fmt.Errorf("旧版服务迁移停止缺少配置")
	}
	if stop == nil {
		return fmt.Errorf("旧版服务迁移停止缺少停止操作")
	}
	lease, err := agent.AcquireCodexRestartLease()
	if err != nil {
		return fmt.Errorf("旧版服务迁移停止无法取得 Codex frontend 租约: %w", err)
	}
	defer lease.Close()
	if err := ensureOfflineCodexRestartSafeWithOptions(cfg, false, false); err != nil {
		return fmt.Errorf("旧版服务迁移停止前 Codex App 检查失败: %w", err)
	}
	if err := beginLegacyRuntimeDrain(ctx, cfg); err != nil {
		return err
	}
	if err := stop(); err != nil {
		stopErr := fmt.Errorf("旧版服务迁移停止失败: %w", err)
		if cancelErr := cancelLegacyRuntimeDrain(context.Background(), cfg); cancelErr != nil {
			return errors.Join(stopErr, fmt.Errorf("恢复旧版服务排空失败: %w", cancelErr))
		}
		return stopErr
	}
	return nil
}

func beginLegacyRuntimeDrain(ctx context.Context, cfg *config.Config) error {
	endpoint, err := runtimeAPIURL(cfg.APIAddr, "/api/runtime/drain")
	if err != nil {
		return fmt.Errorf("无法连接旧版服务排空入口: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	setRuntimeAPIToken(req, cfg.APIToken)
	resp, err := restartSafetyHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("无法确认旧版服务排空状态，已取消迁移停止: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("旧版服务不支持安全排空入口，已取消迁移停止")
	}
	var result runtimeDrainResponse
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("旧版服务排空响应无效: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("旧版服务排空响应包含多余内容")
	}
	if resp.StatusCode == http.StatusConflict {
		if message := strings.TrimSpace(result.Message); message != "" {
			return fmt.Errorf("迁移停止已取消：%s", message)
		}
		return fmt.Errorf("迁移停止已取消：旧版服务仍有 %d 个运行中的任务", result.ActiveTasks)
	}
	if resp.StatusCode != http.StatusOK || result.Status != "ok" || !result.Draining || result.ActiveTasks != 0 || result.RemainingTasks != 0 {
		return fmt.Errorf("旧版服务排空未确认成功（状态 %d，active_tasks=%d，remaining_tasks=%d）", resp.StatusCode, result.ActiveTasks, result.RemainingTasks)
	}
	return nil
}

func cancelLegacyRuntimeDrain(ctx context.Context, cfg *config.Config) error {
	endpoint, err := runtimeAPIURL(cfg.APIAddr, "/api/runtime/drain")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	setRuntimeAPIToken(req, cfg.APIToken)
	resp, err := restartSafetyHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("恢复旧版服务排空返回异常状态 %d", resp.StatusCode)
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
