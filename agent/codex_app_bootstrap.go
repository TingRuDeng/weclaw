package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

const codexAppToolsEnabledKey = "plugins.codex-app-tools@openai-bundled.mcp_servers.codex_app.enabled="
const codexAppReviewEnabledKey = "plugins.code-review@openai-bundled.mcp_servers.code-review.enabled="

func codexAppHasConfiguration(args []string) bool {
	for _, arg := range args {
		if isCodexAppDynamicConfigArg(arg) || strings.HasPrefix(arg, codexAppReviewEnabledKey) {
			return true
		}
	}
	return false
}

func codexAppBootstrapArgs(args []string) []string {
	result := codexAppStaticArgs(args)
	filtered := make([]string, 0, len(result))
	for i := 0; i < len(result); i++ {
		if result[i] == "-c" && i+1 < len(result) && strings.HasPrefix(result[i+1], codexAppReviewEnabledKey) {
			i++
			continue
		}
		if !strings.HasPrefix(result[i], codexAppReviewEnabledKey) {
			filtered = append(filtered, result[i])
		}
	}
	return filtered
}

type codexAppBootstrapRecord struct {
	Starting bool               `json:"starting,omitempty"`
	Host     *codexHostMetadata `json:"host,omitempty"`
}

func codexAppBootstrapPath(socket string) string {
	return filepath.Join(codexAppBridgeDirectory(socket), "app-bootstrap.json")
}

func writeCodexAppBootstrap(socket string, host *codexHostMetadata) error {
	data, err := json.Marshal(codexAppBootstrapRecord{Starting: host == nil, Host: host})
	if err != nil {
		return err
	}
	return securefile.Write(codexAppBootstrapPath(socket), data)
}

func (a *ACPAgent) readCodexAppBootstrap(socket string) (*codexAppBootstrapRecord, error) {
	data, err := securefile.Read(codexAppBootstrapPath(socket))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record codexAppBootstrapRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Starting == (record.Host != nil) {
		return nil, fmt.Errorf("Codex App 初始化记录无效")
	}
	if record.Host != nil {
		metadata, err := a.readCodexHostMetadata(socket)
		if err != nil {
			return nil, err
		}
		if !sameCodexHostGeneration(metadata, *record.Host) {
			return nil, fmt.Errorf("Codex App 初始化 Host 已变化；请协调重启共享服务")
		}
	}
	return &record, nil
}

// replaceCodexAppBootstrapLocked only retires the provisional Host started by
// this bridge. Other frontends wait for the protected record to disappear;
// bootstrap RPCs cannot load a thread. A legacy or confirmed Host is never
// silently restarted merely because a new App sends different settings.
func (a *ACPAgent) replaceCodexAppBootstrapLocked(ctx context.Context, socket string, record *codexAppBootstrapRecord) error {
	if record == nil || record.Host == nil {
		return fmt.Errorf("Codex App 启动配置已变化；请在任务结束后协调重启共享服务")
	}
	// CLI launchers can already hold their frontend lease while waiting for
	// App readiness. The startup lock and provisional record fence their actual
	// attachment; acquiring a restart lease here would deadlock that bootstrap.
	// This exception applies only to our identity-proven, thread-free provisional
	// Host. Confirmed Hosts still require a coordinated restart.
	metadata, err := a.validateManagedCodexHost(socket)
	if err != nil {
		return err
	}
	if !sameCodexHostGeneration(metadata, *record.Host) {
		return fmt.Errorf("Codex App 初始化 Host 进程身份已变化")
	}
	if err := codexAppBootstrapHasNoThreads(ctx, socket); err != nil {
		return err
	}
	if err := a.stopManagedCodexHostLocked(ctx, socket); err != nil {
		return err
	}
	return writeCodexAppBootstrap(socket, nil)
}

func codexAppBootstrapHasNoThreads(ctx context.Context, socket string) error {
	conn, err := dialCodexHost(ctx, socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)
	for i, method := range []string{"initialize", "thread/loaded/list"} {
		var params any = map[string]any{"limit": 1}
		if i == 0 {
			params = codexInitializeParams()
		}
		if err := conn.WriteJSON(map[string]any{"id": i + 1, "method": method, "params": params}); err != nil {
			return err
		}
		for {
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := conn.ReadJSON(&response); err != nil {
				return err
			}
			if response.ID != i+1 {
				continue
			}
			if len(response.Error) != 0 && string(response.Error) != "null" {
				return fmt.Errorf("无法确认 Codex App 初始化 Host 空闲")
			}
			if i == 1 {
				var loaded struct {
					Data       []string `json:"data"`
					NextCursor *string  `json:"nextCursor"`
				}
				if json.Unmarshal(response.Result, &loaded) != nil || loaded.Data == nil || len(loaded.Data) != 0 || loaded.NextCursor != nil {
					return fmt.Errorf("Codex App 初始化 Host 已有会话或状态未知，保留当前共享任务")
				}
			}
			break
		}
		if i == 0 {
			if err := conn.WriteJSON(map[string]string{"method": "initialized"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCodexAppBootstrapMessage(message []byte) error {
	var request struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(message, &request); err != nil {
		return err
	}
	switch request.Method {
	case "initialize", "initialized", "configRequirements/read", "config/read", "account/logout":
		return nil
	default:
		return fmt.Errorf("Codex App 配置初始化连接不能执行 %q；请等待正式共享连接", request.Method)
	}
}

// Plugin overlays deliberately exclude transport/env fields. Keep the signed
// Node runtime and inject our existing pipe preload at Node startup instead,
// so the official plugin manifest and native peer authorization stay intact.
func writeCodexAppMCPNodeLauncher(dir string) (string, error) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	node := quote(codexAppSharedNodePath())
	pipe := quote(filepath.Join(dir, "app-pipe.json"))
	preload := quote("--import=" + (&url.URL{Scheme: "file", Path: filepath.Join(dir, "mcp-env.mjs")}).String())
	contents := "#!/bin/sh\nif [ -e " + pipe + " ] || [ -L " + pipe + " ]; then\n  exec " + node + " " + preload + " \"$@\"\nfi\nexec " + node + " \"$@\"\n"
	launcher := filepath.Join(dir, "mcp-node")
	return launcher, writeCodexAppBridgeArtifact(launcher, []byte(contents), true)
}
