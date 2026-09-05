package agent

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/internal/securefile"
	"github.com/gorilla/websocket"
)

//go:embed assets/codex_app_host.cjs
var codexAppHostScript []byte

//go:embed assets/codex_app_mcp_env.mjs
var codexAppMCPEnvironmentScript []byte

var codexAppPipeOverride = regexp.MustCompile(`\bCODEX_APP_TOOLS_PIPE_PATH\s*=\s*("(?:[^"\\]|\\.)*")`)

func (a *ACPAgent) resolveCodexAppSharedSocket() (string, error) {
	home, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return "", err
	}
	path := codexAppSharedSocketForHome(home)
	a.mu.Lock()
	a.codexHostSocket = path
	a.mu.Unlock()
	return path, nil
}

func codexAppSharedSocketForHome(home string) string {
	path := filepath.Join(home, "weclaw-shared", "app-server.sock")
	if len([]byte(path)) > codexHostSocketMaxBytes {
		return fallbackCodexHostSocket(path)
	}
	return path
}

func codexAppBridgeDirectory(socket string) string {
	return filepath.Join(filepath.Dir(socket), filepath.Base(socket)+".bridge")
}

func (a *ACPAgent) restoreCodexAppSharedPreference(ctx context.Context) error {
	home, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return err
	}
	return restoreSystemCodexAppSharedEnvironment(ctx, codexAppBridgeDirectory(codexAppSharedSocketForHome(home)))
}

// prepareCodexAppBridge installs only WeClaw-owned launcher files. App keeps
// sending its complete arguments; bridge and message clients share one socket.
func (a *ACPAgent) prepareCodexAppBridge(ctx context.Context) (string, string, error) {
	if err := a.validateCodexLiveTestPathIsolation(); err != nil {
		return "", "", err
	}
	socket, err := a.resolveCodexAppSharedSocket()
	if err != nil {
		return "", "", err
	}
	if err := a.prepareCodexHostSocket(socket); err != nil {
		return "", "", err
	}
	dir := codexAppBridgeDirectory(socket)
	if err := securefile.EnsureDir(dir); err != nil {
		return "", "", err
	}
	for name, contents := range map[string][]byte{
		"host.cjs": codexAppHostScript, "mcp-env.mjs": codexAppMCPEnvironmentScript,
	} {
		if err := writeCodexAppBridgeArtifact(filepath.Join(dir, name), contents, false); err != nil {
			return "", "", err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "", err
	}
	launcher := filepath.Join(dir, "codex")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	contents := []byte("#!/bin/sh\nexec " + quote(executable) + " codex app-bridge \"$@\"\n")
	if err := writeCodexAppBridgeArtifact(launcher, contents, true); err != nil {
		return "", "", err
	}
	if err := validateCodexAppSharedNode(ctx); err != nil {
		return "", "", err
	}
	return socket, launcher, nil
}

func writeCodexAppBridgeArtifact(path string, data []byte, executable bool) error {
	prior, err := securefile.ReadForUpdate(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	mode := os.FileMode(0o600)
	if executable {
		mode = 0o700
	}
	if err == nil && bytes.Equal(prior, data) {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() == mode {
			return nil
		}
	}
	// Publish the launcher with its executable bit already set, so another
	// App launch never observes a partially installed entry point.
	temp, err := os.CreateTemp(filepath.Dir(path), ".app-bridge-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// PrepareCodexApp starts the regular App with its shared-service adapter.
// Existing App processes are never killed or silently handed another home.
func (a *ACPAgent) PrepareCodexApp(ctx context.Context) error {
	if a.codexHostMode != codexHostModeShared {
		return fmt.Errorf("Codex App 共享入口需要 codex_host_mode shared 或支持 App 的 auto 模式")
	}
	socket, launcher, err := a.prepareCodexAppBridge(ctx)
	if err != nil {
		return err
	}
	if conn, err := dialCodexHost(ctx, socket); err == nil {
		_ = conn.Close()
		if err := a.preflightConnectedManagedCodexHost(ctx, socket); err != nil {
			return err
		}
	} else if err := a.preflightCodexHostConflicts(ctx, 0); err != nil {
		return err
	}
	environment, err := a.resolveCodexAppDaemonEnvironment()
	if err != nil {
		return err
	}
	return prepareSystemCodexAppShared(ctx, launcher, environment)
}

func (a *ACPAgent) launchCodexAppSharedClient(ctx context.Context) (int, error) {
	socket, _, err := a.prepareCodexAppBridge(ctx)
	if err != nil {
		return 0, err
	}
	if conn, err := dialCodexHost(ctx, socket); err == nil {
		lock, err := a.acquireCodexHostStartupLock(ctx, socket)
		if err != nil {
			_ = conn.Close()
			return 0, err
		}
		err = a.preflightConnectedManagedCodexHost(ctx, socket)
		releaseCodexHostStartupLock(lock)
		if err != nil {
			_ = conn.Close()
			return 0, err
		}
		if err := a.attachCodexHostConnection(conn); err != nil {
			_ = conn.Close()
			return 0, err
		}
		return 0, nil
	}
	if err := a.PrepareCodexApp(ctx); err != nil {
		return 0, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if conn, err := dialCodexHost(waitCtx, socket); err == nil {
			// The bridge records process identity while holding this same lock.
			lock, err := a.acquireCodexHostStartupLock(waitCtx, socket)
			if err != nil {
				_ = conn.Close()
				return 0, err
			}
			err = a.preflightConnectedManagedCodexHost(waitCtx, socket)
			releaseCodexHostStartupLock(lock)
			if err != nil {
				_ = conn.Close()
				return 0, err
			}
			if err := a.attachCodexHostConnection(conn); err != nil {
				_ = conn.Close()
				return 0, err
			}
			return 0, nil
		}
		select {
		case <-waitCtx.Done():
			return 0, fmt.Errorf("等待 Codex App 共享服务: %w", waitCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// normalizeCodexAppBridgeArgs keeps App's permissions, feature flags and MCP
// settings intact. Only its per-launch pipe is resolved at MCP startup.
func normalizeCodexAppBridgeArgs(args []string, dir string) ([]string, string, error) {
	result := make([]string, 0, len(args)+2)
	pipe := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--stdio" {
			continue
		}
		if arg == "--listen" || strings.HasPrefix(arg, "--listen=") {
			return nil, "", fmt.Errorf("Codex App bridge transport cannot be overridden")
		}
		if strings.HasPrefix(arg, "mcp_servers.codex_app=") {
			match := codexAppPipeOverride.FindStringSubmatch(arg)
			if len(match) != 0 {
				if strings.Contains(arg, "NODE_OPTIONS") {
					return nil, "", fmt.Errorf("Codex App MCP already defines NODE_OPTIONS; refusing to discard it")
				}
				if err := json.Unmarshal([]byte(match[1]), &pipe); err != nil || !filepath.IsAbs(pipe) {
					return nil, "", fmt.Errorf("Codex App MCP pipe is invalid")
				}
				arg = codexAppPipeOverride.ReplaceAllString(arg, `CODEX_APP_TOOLS_PIPE_PATH="weclaw-current-app"`)
			}
		}
		result = append(result, arg)
	}
	if pipe != "" {
		preload := "--import=" + (&url.URL{Scheme: "file", Path: filepath.Join(dir, "mcp-env.mjs")}).String()
		// Node's NODE_OPTIONS parser accepts a quoted option, including spaces.
		quoted, _ := json.Marshal(preload)
		value, _ := json.Marshal(string(quoted))
		result = append(result, "-c", "mcp_servers.codex_app.env.NODE_OPTIONS="+string(value))
	}
	return result, pipe, nil
}

// RunCodexAppBridge is the App's stdio entry point. It never resumes or releases
// a thread itself, and EOF disconnects only this frontend, not the shared Host.
func (a *ACPAgent) RunCodexAppBridge(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer) error {
	if a.codexHostMode != codexHostModeShared {
		return fmt.Errorf("Codex App bridge requires shared Host mode")
	}
	binary, err := a.resolveCodexDaemonLifecycleCommand()
	if err != nil {
		return err
	}
	if !slices.Contains(args, "app-server") || slices.Contains(args, "generate-ts") || slices.Contains(args, "generate-json-schema") {
		command := exec.CommandContext(ctx, binary, args...)
		command.Stdin, command.Stdout, command.Stderr = input, output, stderr
		return command.Run()
	}
	if slices.Contains(args, "daemon") || slices.Contains(args, "proxy") {
		return fmt.Errorf("Codex App bridge does not accept daemon or proxy commands")
	}
	socket, _, err := a.prepareCodexAppBridge(ctx)
	if err != nil {
		return err
	}
	dir := codexAppBridgeDirectory(socket)
	normalized, pipe, err := normalizeCodexAppBridgeArgs(args, dir)
	if err != nil {
		return err
	}
	lock, err := a.acquireCodexHostStartupLock(ctx, socket)
	if err != nil {
		return err
	}
	conn, err := a.connectCodexAppBridgeLocked(ctx, socket, binary, normalized, pipe)
	releaseCodexHostStartupLock(lock)
	if err != nil {
		return err
	}
	defer conn.Close()
	return relayCodexAppBridge(ctx, conn, input, output)
}

func (a *ACPAgent) connectCodexAppBridgeLocked(ctx context.Context, socket, binary string, args []string, pipe string) (*websocket.Conn, error) {
	dir := codexAppBridgeDirectory(socket)
	argsPath := filepath.Join(dir, "app-args.json")
	if conn, dialErr := dialCodexHost(ctx, socket); dialErr == nil {
		if err := a.preflightConnectedManagedCodexHost(ctx, socket); err != nil {
			_ = conn.Close()
			return nil, err
		}
		previous, err := securefile.Read(argsPath)
		var prior []string
		if err != nil || json.Unmarshal(previous, &prior) != nil || !slices.Equal(prior, args) {
			_ = conn.Close()
			return nil, fmt.Errorf("Codex App 启动配置已变化；当前共享任务保留，请在任务结束后重启共享服务")
		}
		_, err = saveCodexAppPipe(dir, pipe)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		applied, readErr := securefile.Read(filepath.Join(dir, "app-pipe-applied.json"))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			_ = conn.Close()
			return nil, readErr
		}
		current, _ := json.Marshal(pipe)
		if pipe != "" && !bytes.Equal(applied, current) {
			if err := reloadCodexAppMCP(ctx, socket); err != nil {
				_ = conn.Close()
				return nil, err
			}
			if err := securefile.Write(filepath.Join(dir, "app-pipe-applied.json"), current); err != nil {
				_ = conn.Close()
				return nil, err
			}
		}
		return conn, nil
	}
	if err := a.preflightCodexHostConflicts(ctx, 0); err != nil {
		return nil, err
	}
	if err := a.removeStaleCodexHostSocket(socket); err != nil {
		return nil, err
	}
	if _, err := saveCodexAppPipe(dir, pipe); err != nil {
		return nil, err
	}
	data, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	if err := securefile.Write(argsPath, data); err != nil {
		return nil, err
	}
	conn, err := a.startCodexAppSharedHostLocked(ctx, socket, binary, args)
	if err != nil {
		return nil, err
	}
	if pipe != "" {
		current, _ := json.Marshal(pipe)
		if err := securefile.Write(filepath.Join(dir, "app-pipe-applied.json"), current); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func saveCodexAppPipe(dir, pipe string) (bool, error) {
	if pipe == "" {
		return false, nil
	}
	path := filepath.Join(dir, "app-pipe.json")
	data, _ := json.Marshal(pipe)
	prior, err := securefile.Read(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if bytes.Equal(prior, data) {
		return false, nil
	}
	return true, securefile.Write(path, data)
}

func (a *ACPAgent) restartCodexAppSharedClientLocked(ctx context.Context, socket string) (int, error) {
	data, err := securefile.Read(filepath.Join(codexAppBridgeDirectory(socket), "app-args.json"))
	if err != nil {
		return 0, fmt.Errorf("读取已确认的 Codex App 启动配置: %w", err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		return 0, err
	}
	binary, err := a.resolveCodexDaemonLifecycleCommand()
	if err != nil {
		return 0, err
	}
	conn, err := a.connectCodexAppBridgeLocked(ctx, socket, binary, args, "")
	if err != nil {
		return 0, err
	}
	if err := a.attachCodexHostConnection(conn); err != nil {
		_ = conn.Close()
		return 0, err
	}
	return 0, nil
}

func (a *ACPAgent) startCodexAppSharedHostLocked(ctx context.Context, socket, binary string, args []string) (*websocket.Conn, error) {
	if err := validateCodexAppSharedNode(ctx); err != nil {
		return nil, err
	}
	dir := codexAppBridgeDirectory(socket)
	logFile, err := securefile.OpenAppend(filepath.Join(dir, "host.log"))
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	spec, err := json.Marshal(struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Cwd     string   `json:"cwd"`
	}{binary, codexSharedHostArgs(args, socket), a.cwd})
	if err != nil {
		return nil, err
	}
	// The signed launcher must outlive the frontend that starts the shared Host.
	command := exec.CommandContext(context.WithoutCancel(ctx), codexAppSharedNodePath(), filepath.Join(dir, "host.cjs"))
	configureACPProcess(command)
	command.Dir, command.Stdin, command.Stderr = a.cwd, bytes.NewReader(spec), logFile
	command.Env, err = mergeEnv(os.Environ(), a.env)
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	ready := make(chan struct {
		PID int
		Err error
	}, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		var result struct {
			PID int `json:"pid"`
		}
		err := json.NewDecoder(stdout).Decode(&result)
		ready <- struct {
			PID int
			Err error
		}{result.PID, err}
		_, _ = io.Copy(io.Discard, stdout)
		_ = command.Wait()
	}()
	var pid int
	select {
	case result := <-ready:
		if result.Err != nil {
			return nil, fmt.Errorf("启动 Codex App 共享服务: %w", result.Err)
		}
		pid = result.PID
	case <-ctx.Done():
		// Reap our launcher before releasing the startup lock. It must not
		// spawn a late Host after another frontend begins a fresh attempt.
		_ = command.Process.Kill()
		<-exited
		return nil, ctx.Err()
	case <-time.After(codexHostConnectTimeout):
		_ = command.Process.Kill()
		<-exited
		return nil, fmt.Errorf("Codex App 共享服务启动响应超时；已停止本次启动器，请检查 host.log")
	}
	if pid <= 0 {
		return nil, fmt.Errorf("Codex shared Host returned an invalid PID")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	metadata, err := a.newManagedCodexHostMetadata(&exec.Cmd{Process: process}, socket)
	if err != nil {
		return nil, err
	}
	if metadata.ProcessGroupID != pid {
		return nil, fmt.Errorf("Codex shared Host has no independent process group")
	}
	metadata.ManagedCodexPath = binary
	if err := a.writeManagedCodexHostMetadata(socket, metadata); err != nil {
		return nil, err
	}
	conn, err := waitForCodexHost(ctx, socket, pid, nil, a.effectiveCodexHostConnectTimeout())
	if err != nil {
		return nil, err
	}
	if err := a.preflightConnectedManagedCodexHost(ctx, socket); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func relayCodexAppBridge(ctx context.Context, conn *websocket.Conn, input io.Reader, output io.Writer) error {
	done := make(chan error, 2)
	go func() {
		scanner := newACPScanner(input)
		for scanner.Scan() {
			if err := conn.WriteMessage(websocket.TextMessage, scanner.Bytes()); err != nil {
				done <- err
				return
			}
		}
		done <- scanner.Err()
	}()
	go func() {
		writer := bufio.NewWriter(output)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				done <- err
				return
			}
			if _, err = writer.Write(append(message, '\n')); err == nil {
				err = writer.Flush()
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func reloadCodexAppMCP(ctx context.Context, socket string) error {
	conn, err := dialCodexHost(ctx, socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)
	for i, request := range []struct {
		Method string
		Params any
	}{
		{"initialize", codexInitializeParams()},
		{"config/mcpServer/reload", map[string]any{}},
	} {
		if err := conn.WriteJSON(map[string]any{"id": i + 1, "method": request.Method, "params": request.Params}); err != nil {
			return err
		}
		for {
			var response struct {
				ID    int              `json:"id"`
				Error *json.RawMessage `json:"error"`
			}
			if err := conn.ReadJSON(&response); err != nil {
				return err
			}
			if response.ID != i+1 {
				continue
			}
			if response.Error != nil {
				return fmt.Errorf("刷新 Codex App 工具连接: %s", *response.Error)
			}
			break
		}
		if i == 0 {
			if err := conn.WriteJSON(map[string]any{"method": "initialized"}); err != nil {
				return err
			}
		}
	}
	return nil
}
