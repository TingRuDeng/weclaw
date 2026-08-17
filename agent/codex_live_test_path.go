package agent

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

var errCodexLiveTestPathLeak = errors.New("Codex 配置引用了 WeClaw 真实协议测试临时目录")

func (a *ACPAgent) validateCodexLiveTestPathIsolation() error {
	if a.protocol != protocolCodexAppServer || a.allowCodexLiveTestPaths {
		return nil
	}
	command := strings.TrimSpace(a.command)
	if isCodexLiveTestTemporaryPath(command) {
		return newCodexLiveTestPathLeakError("command", command)
	}
	if strings.ContainsAny(command, `/\\`) {
		candidate := command
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(a.cwd, candidate)
		}
		if isCodexLiveTestTemporaryPath(candidate) {
			return newCodexLiveTestPathLeakError("command", candidate)
		}
	}
	resolvedCommand, _ := exec.LookPath(command)
	if strings.TrimSpace(resolvedCommand) != "" {
		if isCodexLiveTestTemporaryPath(resolvedCommand) {
			return newCodexLiveTestPathLeakError("解析后的 command", resolvedCommand)
		}
		if realCommand, err := filepath.EvalSymlinks(resolvedCommand); err == nil &&
			isCodexLiveTestTemporaryPath(realCommand) {
			return newCodexLiveTestPathLeakError("command 软链接目标", realCommand)
		}
	}
	if configuredPath, ok := a.env["PATH"]; ok {
		for _, entry := range filepath.SplitList(configuredPath) {
			candidate := strings.TrimSpace(entry)
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(a.cwd, candidate)
			}
			if isCodexLiveTestTemporaryPath(candidate) {
				return newCodexLiveTestPathLeakError("PATH", candidate)
			}
			if realEntry, err := filepath.EvalSymlinks(candidate); err == nil &&
				isCodexLiveTestTemporaryPath(realEntry) {
				return newCodexLiveTestPathLeakError("PATH 目录软链接目标", realEntry)
			}
		}
	}
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err == nil && isCodexLiveTestTemporaryPath(codexHome) {
		return newCodexLiveTestPathLeakError("CODEX_HOME", codexHome)
	}
	if err == nil {
		if realCodexHome, evalErr := filepath.EvalSymlinks(codexHome); evalErr == nil &&
			isCodexLiveTestTemporaryPath(realCodexHome) {
			return newCodexLiveTestPathLeakError("CODEX_HOME 软链接目标", realCodexHome)
		}
	}
	return nil
}

func newCodexLiveTestPathLeakError(source string, path string) error {
	return fmt.Errorf(
		"%w：%s=%s。请改为持久化的正式 Codex 安装路径后重试；本次启动已在任何 Codex Host 或 App 配置变更前失败关闭，未启动或停止任何 Codex 进程",
		errCodexLiveTestPathLeak,
		source,
		filepath.Clean(path),
	)
}

func isCodexLiveTestTemporaryPath(path string) bool {
	normalized := strings.ReplaceAll(filepath.Clean(strings.TrimSpace(path)), "\\", "/")
	for _, component := range strings.Split(normalized, "/") {
		if strings.HasPrefix(component, "weclaw-codex-live") {
			return true
		}
	}
	return false
}

func codexPathWithinRoot(root string, candidate string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(candidate) == "" {
		return false
	}
	root = canonicalCodexPath(root)
	candidate = canonicalCodexPath(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func canonicalCodexPath(path string) string {
	cleaned := filepath.Clean(path)
	if absolute, err := filepath.Abs(cleaned); err == nil {
		cleaned = absolute
	}
	if realPath, err := filepath.EvalSymlinks(cleaned); err == nil {
		return filepath.Clean(realPath)
	}
	return cleaned
}
