//go:build darwin

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

func codexAppSharedBundlePath() string {
	for _, bundle := range []string{"/Applications/Codex.app", "/Applications/ChatGPT.app"} {
		if info, err := os.Stat(filepath.Join(bundle, "Contents", "Resources", "cua_node", "bin", "node")); err == nil && info.Mode().IsRegular() {
			return bundle
		}
	}
	return ""
}

func codexAppSharedHostAvailable() bool { return codexAppSharedBundlePath() != "" }

func codexAppSharedNodePath() string {
	return filepath.Join(codexAppSharedBundlePath(), "Contents", "Resources", "cua_node", "bin", "node")
}

func validateCodexAppSharedNode(ctx context.Context) error {
	if !codexAppSharedHostAvailable() {
		return fmt.Errorf("Codex App 共享服务需要安装包含官方 Node 的 macOS Codex App")
	}
	requirement := `anchor apple generic and certificate leaf[subject.OU] = "2DC432GLL2"`
	command := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "-R="+requirement, codexAppSharedNodePath())
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("验证 Codex App 官方 Node 签名: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func prepareSystemCodexAppShared(ctx context.Context, launcher string, expected codexAppDaemonEnvironment) error {
	return securefile.WithExclusiveLock(ctx, filepath.Join(filepath.Dir(launcher), "launch.lock"), func() error {
		return prepareSystemCodexAppSharedLocked(ctx, launcher, expected, true)
	})
}

func configureSystemCodexAppShared(ctx context.Context, launcher string, expected codexAppDaemonEnvironment) error {
	return securefile.WithExclusiveLock(ctx, filepath.Join(filepath.Dir(launcher), "launch.lock"), func() error {
		return prepareSystemCodexAppSharedLocked(ctx, launcher, expected, false)
	})
}

type codexAppSharedLaunchBackup struct {
	Previous map[string]string `json:"previous"`
	Applied  map[string]string `json:"applied"`
}

func prepareSystemCodexAppSharedLocked(ctx context.Context, launcher string, expected codexAppDaemonEnvironment, launchApp bool) error {
	if err := expected.validate(); err != nil {
		return err
	}
	state, err := codexDesktopHostProcessStateFromSystem()
	if err != nil {
		return err
	}
	for _, pid := range state.AppPIDs {
		for key, want := range map[string]string{codexCLIPathEnv: launcher, codexAppCodexHomeEnv: expected.CodexHome} {
			value, found, err := readCodexAppProcessEnvironmentValue(pid, key)
			if err != nil {
				return err
			}
			if !found || value != want {
				return fmt.Errorf("%w：首次启用共享适配需完整退出当前 App，再运行 weclaw codex app；之后释放会话无需重启", ErrCodexAppRestartRequired)
			}
		}
	}
	mutations := make([]codexAppDaemonLaunchMutation, 0, 5)
	backupPath := filepath.Join(filepath.Dir(launcher), "launch-environment.json")
	backup := codexAppSharedLaunchBackup{Previous: make(map[string]string), Applied: make(map[string]string)}
	if data, err := securefile.Read(backupPath); err == nil {
		if err := json.Unmarshal(data, &backup); err != nil || backup.Previous == nil || backup.Applied == nil {
			return fmt.Errorf("Codex App 启动环境备份无效")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, setting := range []struct{ Key, Value string }{
		{codexCLIPathEnv, launcher}, {codexAppUseLocalDaemonEnv, "0"}, {codexAppForceCLIEnv, "1"},
		{codexAppCodexHomeEnv, expected.CodexHome}, {codexAppSQLiteHomeEnv, expected.effectiveSQLiteHome()},
	} {
		prior, err := codexAppLaunchEnvironment(ctx, setting.Key)
		if err != nil {
			return err
		}
		if prior == setting.Value {
			continue
		}
		if prior != "" && (setting.Key == codexCLIPathEnv || setting.Key == codexAppCodexHomeEnv || setting.Key == codexAppSQLiteHomeEnv) {
			return fmt.Errorf("launchd %s 已指向其他配置，不能覆盖；请先统一 Codex 运行目录", setting.Key)
		}
		mutations = append(mutations, codexAppDaemonLaunchMutation{
			apply:    []string{"setenv", setting.Key, setting.Value},
			rollback: codexAppDaemonLaunchRestoreArgs(setting.Key, prior),
		})
		if _, saved := backup.Previous[setting.Key]; !saved {
			backup.Previous[setting.Key] = prior
		}
		backup.Applied[setting.Key] = setting.Value
	}
	if len(mutations) != 0 {
		data, err := json.Marshal(backup)
		if err != nil {
			return err
		}
		if err := securefile.Write(backupPath, data); err != nil {
			return err
		}
	}
	for i, mutation := range mutations {
		if _, err := runCodexAppLaunchctl(ctx, mutation.apply...); err != nil {
			return errors.Join(err, rollbackCodexAppDaemonLaunchMutations(ctx, runCodexAppLaunchctl, mutations[:i+1]))
		}
	}
	if state.AppRunning || !launchApp {
		return nil
	}
	command := exec.CommandContext(ctx, "/usr/bin/open", "-g", "-a", codexAppSharedBundlePath())
	if output, err := command.CombinedOutput(); err != nil {
		return errors.Join(fmt.Errorf("启动共享 Codex App: %w: %s", err, strings.TrimSpace(string(output))), rollbackCodexAppDaemonLaunchMutations(ctx, runCodexAppLaunchctl, mutations))
	}
	return nil
}

func restoreSystemCodexAppSharedEnvironment(ctx context.Context, dir string) error {
	backupPath := filepath.Join(dir, "launch-environment.json")
	if _, err := os.Lstat(backupPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return securefile.WithExclusiveLock(ctx, filepath.Join(dir, "launch.lock"), func() error {
		data, err := securefile.Read(backupPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var backup codexAppSharedLaunchBackup
		if err := json.Unmarshal(data, &backup); err != nil {
			return err
		}
		for key, applied := range backup.Applied {
			current, err := codexAppLaunchEnvironment(ctx, key)
			if err != nil {
				return err
			}
			if current != applied {
				continue
			} // Preserve changes made outside WeClaw.
			if _, err := runCodexAppLaunchctl(ctx, codexAppDaemonLaunchRestoreArgs(key, backup.Previous[key])...); err != nil {
				return err
			}
		}
		return os.Remove(backupPath)
	})
}
