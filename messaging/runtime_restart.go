package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

var ErrRuntimeRestartBlocked = errors.New("运行时重启条件不满足")

type RuntimeRestartResult struct {
	ActiveTasks    int                        `json:"active_tasks"`
	RemainingTasks int                        `json:"remaining_tasks"`
	Codex          bool                       `json:"codex"`
	CodexHost      agent.CodexRestartSnapshot `json:"codex_host"`
}

type runtimeRestartState struct {
	Version             int                        `json:"version"`
	PreparedAt          time.Time                  `json:"prepared_at"`
	Codex               bool                       `json:"codex"`
	CodexHost           agent.CodexRestartSnapshot `json:"codex_host"`
	OfflineForcePending bool                       `json:"offline_force_pending,omitempty"`
	ServiceManager      string                     `json:"service_manager,omitempty"`
}

const runtimeRestartStateVersion = 1

type codexRestartOptionsController interface {
	PrepareCodexRestartWithOptions(
		context.Context,
		func(agent.CodexRestartSnapshot) error,
		agent.CodexRestartOptions,
	) (agent.CodexRestartSnapshot, error)
}

type codexForceRestartController interface {
	ForceCompleteCodexRestart(
		context.Context,
		func(agent.CodexRestartSnapshot) error,
	) (agent.CodexRestartSnapshot, error)
}

// PrepareRuntimeRestart atomically closes message admission, drains tasks and
// stops the verified Codex Host before the outer CLI replaces the service.
func (h *Handler) PrepareRuntimeRestart(ctx context.Context, force bool) (RuntimeRestartResult, error) {
	return h.PrepareRuntimeRestartWithOptions(ctx, force, false, false)
}

// PrepareRuntimeRestartWithOptions carries explicit external-Host stop
// authority from the loopback restart endpoint into the Codex transaction.
func (h *Handler) PrepareRuntimeRestartWithOptions(
	ctx context.Context,
	forceDrain bool,
	stopConflictingCodexHosts bool,
	forceTerminateCodex bool,
) (RuntimeRestartResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.runtimeRestartMu.Lock()
	defer h.runtimeRestartMu.Unlock()
	if h.runtimeRestartPrepared {
		if h.runtimeRestartUnsafe {
			if forceTerminateCodex {
				return h.forceCompleteUnsafeRuntimeRestart(ctx)
			}
			return h.runtimeRestartResult, fmt.Errorf(
				"Codex Host 停止结果未知，服务保持不可写；请修复 Host 状态或显式使用 --force 收敛: %w",
				agent.ErrCodexRestartUnsafe,
			)
		}
		return h.runtimeRestartResult, nil
	}
	if h.runtimeRestartLease == nil {
		lease, leaseErr := agent.AcquireCodexRestartLease()
		if leaseErr != nil {
			return RuntimeRestartResult{}, fmt.Errorf("%w: %w", ErrRuntimeRestartBlocked, leaseErr)
		}
		h.runtimeRestartLease = lease
	}

	drain, err := h.Drain(ctx, forceDrain)
	result := RuntimeRestartResult{ActiveTasks: drain.ActiveTasks, RemainingTasks: drain.RemainingTasks}
	if err != nil {
		_ = h.releaseRuntimeRestartLease()
		return result, err
	}

	var controller agent.CodexRestartController
	if _, ok := h.codexAgentName(); ok {
		_, runtimeAgent, agentErr := h.getCodexSessionAgent(ctx)
		if agentErr != nil {
			h.CancelDrain()
			_ = h.releaseRuntimeRestartLease()
			return result, fmt.Errorf("%w: %w", ErrRuntimeRestartBlocked, agentErr)
		}
		var supported bool
		controller, supported = runtimeAgent.(agent.CodexRestartController)
		if !supported {
			h.CancelDrain()
			_ = h.releaseRuntimeRestartLease()
			return result, fmt.Errorf("%w: 当前 Codex Agent 不支持协调重启", ErrRuntimeRestartBlocked)
		}
		preparing := runtimeRestartState{
			Version: runtimeRestartStateVersion, PreparedAt: time.Now().UTC(), Codex: true,
		}
		if writeErr := h.writeRuntimeRestartState(preparing); writeErr != nil {
			h.CancelDrain()
			_ = h.releaseRuntimeRestartLease()
			return result, fmt.Errorf("记录重启准备状态: %w", writeErr)
		}
		persistIntent := func(snapshot agent.CodexRestartSnapshot) error {
			intent := runtimeRestartState{
				Version: runtimeRestartStateVersion, PreparedAt: time.Now().UTC(),
				Codex: true, CodexHost: snapshot,
			}
			if err := h.writeRuntimeRestartState(intent); err != nil {
				return err
			}
			result.Codex = true
			result.CodexHost = snapshot
			return nil
		}
		var snapshot agent.CodexRestartSnapshot
		var prepareErr error
		if optionsController, ok := runtimeAgent.(codexRestartOptionsController); ok {
			snapshot, prepareErr = optionsController.PrepareCodexRestartWithOptions(
				ctx,
				persistIntent,
				agent.CodexRestartOptions{
					StopConflictingCodexHosts: stopConflictingCodexHosts,
					ForceTerminateCodex:       forceTerminateCodex,
				},
			)
		} else if stopConflictingCodexHosts || forceTerminateCodex {
			prepareErr = fmt.Errorf("当前 Codex Agent 不支持显式 Host 终止授权")
		} else {
			snapshot, prepareErr = controller.PrepareCodexRestart(ctx, persistIntent)
		}
		if prepareErr != nil {
			if errors.Is(prepareErr, agent.ErrCodexRestartUnsafe) {
				h.runtimeRestartPrepared = true
				h.runtimeRestartUnsafe = true
				h.runtimeRestartController = controller
				h.runtimeRestartResult = result
				return result, fmt.Errorf("Codex Host 停止结果未知，服务保持不可写: %w", prepareErr)
			}
			if removeErr := h.removeRuntimeRestartState(); removeErr != nil {
				return result, errors.Join(
					fmt.Errorf("%w: %w", ErrRuntimeRestartBlocked, prepareErr),
					fmt.Errorf("清理重启准备状态: %w", removeErr),
				)
			}
			h.CancelDrain()
			_ = h.releaseRuntimeRestartLease()
			return result, fmt.Errorf("%w: %w", ErrRuntimeRestartBlocked, prepareErr)
		}
		result.Codex = true
		result.CodexHost = snapshot
	}

	state := runtimeRestartState{
		Version: runtimeRestartStateVersion, PreparedAt: time.Now().UTC(),
		Codex: result.Codex, CodexHost: result.CodexHost,
	}
	if err := h.writeRuntimeRestartState(state); err != nil {
		if controller != nil {
			if cancelErr := controller.CancelCodexRestart(ctx); cancelErr != nil {
				return result, errors.Join(
					fmt.Errorf("记录重启事务: %w", err),
					fmt.Errorf("恢复 Codex Host: %w", cancelErr),
				)
			}
		}
		if removeErr := h.removeRuntimeRestartState(); removeErr != nil {
			return result, errors.Join(
				fmt.Errorf("记录重启事务: %w", err),
				fmt.Errorf("清理重启准备状态: %w", removeErr),
			)
		}
		h.CancelDrain()
		_ = h.releaseRuntimeRestartLease()
		return result, fmt.Errorf("记录重启事务: %w", err)
	}
	h.runtimeRestartPrepared = true
	h.runtimeRestartUnsafe = false
	h.runtimeRestartController = controller
	h.runtimeRestartResult = result
	return result, nil
}

func (h *Handler) forceCompleteUnsafeRuntimeRestart(ctx context.Context) (RuntimeRestartResult, error) {
	controller, ok := h.runtimeRestartController.(codexForceRestartController)
	if !ok {
		return h.runtimeRestartResult, fmt.Errorf(
			"%w: 当前 Codex Agent 不支持从未知停止结果升级为强制终止",
			ErrRuntimeRestartBlocked,
		)
	}
	result := h.runtimeRestartResult
	persistIntent := func(snapshot agent.CodexRestartSnapshot) error {
		state := runtimeRestartState{
			Version: runtimeRestartStateVersion, PreparedAt: time.Now().UTC(),
			Codex: true, CodexHost: snapshot,
		}
		if err := h.writeRuntimeRestartState(state); err != nil {
			return err
		}
		result.Codex = true
		result.CodexHost = snapshot
		return nil
	}
	snapshot, err := controller.ForceCompleteCodexRestart(ctx, persistIntent)
	if err != nil {
		return h.runtimeRestartResult, errors.Join(
			agent.ErrCodexRestartUnsafe,
			fmt.Errorf("Codex Host 停止结果仍无法确认，服务保持不可写: %w", err),
		)
	}
	result.Codex = true
	result.CodexHost = snapshot
	h.runtimeRestartResult = result
	h.runtimeRestartUnsafe = false
	return result, nil
}

// CancelRuntimeRestart compensates a failed outer service restart. Admission
// is reopened only after a stopped Codex Host has been reconstructed.
func (h *Handler) CancelRuntimeRestart(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	h.runtimeRestartMu.Lock()
	defer h.runtimeRestartMu.Unlock()
	if h.runtimeRestartController != nil {
		if err := h.runtimeRestartController.CancelCodexRestart(ctx); err != nil {
			return err
		}
	}
	if err := h.removeRuntimeRestartState(); err != nil {
		return err
	}
	leaseErr := h.releaseRuntimeRestartLease()
	h.runtimeRestartPrepared = false
	h.runtimeRestartUnsafe = false
	h.runtimeRestartController = nil
	h.runtimeRestartResult = RuntimeRestartResult{}
	h.CancelDrain()
	return leaseErr
}

// RecoverRuntimeRestart is called before platform listeners start. A persisted
// Host generation must be replaced and verified before messages are admitted.
func (h *Handler) RecoverRuntimeRestart(ctx context.Context) error {
	state, exists, err := h.readRuntimeRestartState()
	if err != nil || !exists {
		return err
	}
	if state.OfflineForcePending {
		return fmt.Errorf("离线强制停止尚未完成，请运行 weclaw stop --force 或 weclaw restart --force 重试")
	}
	if state.Version != runtimeRestartStateVersion || state.PreparedAt.IsZero() {
		return fmt.Errorf("重启事务状态无效")
	}
	if state.Codex {
		if _, ok := h.codexAgentName(); !ok {
			return fmt.Errorf("上次重启停止了 Codex Host，但当前配置没有 Codex Agent")
		}
		_, runtimeAgent, err := h.getCodexSessionAgent(ctx)
		if err != nil {
			return fmt.Errorf("恢复 Codex Agent: %w", err)
		}
		controller, ok := runtimeAgent.(agent.CodexRestartController)
		if !ok {
			return fmt.Errorf("当前 Codex Agent 不支持启动后拓扑验证")
		}
		if _, err := controller.VerifyCodexRestart(ctx, state.CodexHost); err != nil {
			return fmt.Errorf("验证重启后的 Codex Host: %w", err)
		}
	}
	return h.removeRuntimeRestartState()
}

func (h *Handler) runtimeRestartStatePath() (string, error) {
	if path := strings.TrimSpace(h.runtimeRestartStateFile); path != "" {
		return filepath.Clean(path), nil
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "state", "runtime-restart.json"), nil
}

func (h *Handler) writeRuntimeRestartState(state runtimeRestartState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path, err := h.runtimeRestartStatePath()
	if err != nil {
		return err
	}
	return securefile.Write(path, data)
}

func (h *Handler) readRuntimeRestartState() (runtimeRestartState, bool, error) {
	path, err := h.runtimeRestartStatePath()
	if err != nil {
		return runtimeRestartState{}, false, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return runtimeRestartState{}, false, nil
	} else if err != nil {
		return runtimeRestartState{}, false, err
	}
	data, err := securefile.Read(path)
	if err != nil {
		return runtimeRestartState{}, false, err
	}
	var state runtimeRestartState
	if err := json.Unmarshal(data, &state); err != nil {
		return runtimeRestartState{}, false, err
	}
	return state, true, nil
}

func (h *Handler) removeRuntimeRestartState() error {
	path, err := h.runtimeRestartStatePath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// releaseRuntimeRestartLease is called with runtimeRestartMu held.
func (h *Handler) releaseRuntimeRestartLease() error {
	lease := h.runtimeRestartLease
	h.runtimeRestartLease = nil
	if lease == nil {
		return nil
	}
	return lease.Close()
}
