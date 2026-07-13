package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	testACPCapabilitiesEnv  = "WECLAW_TEST_ACP_CAPABILITIES"
	testACPPayloadEnv       = "WECLAW_TEST_ACP_INITIALIZE_PAYLOAD"
	testACPPIDFileEnv       = "WECLAW_TEST_ACP_PID_FILE"
	testACPListCapability   = "agentCapabilities.sessionCapabilities.list"
	testACPResumeCapability = "agentCapabilities.sessionCapabilities.resume"
)

func TestClaudeACPStartupRequiresListAndResume(t *testing.T) {
	tests := []struct {
		name       string
		capability string
		missing    string
	}{
		{"缺少 list", `"resume":{}`, testACPListCapability},
		{"缺少 resume", `"list":{}`, testACPResumeCapability},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := claudeInitializePayload(tc.capability)
			a, pidFile := newCapabilityTestAgent(t, "", payload)
			t.Cleanup(a.Stop)

			err := a.Start(context.Background())

			assertMissingCapability(t, err, tc.missing)
			assertCapabilityTestProcessExited(t, pidFile)
		})
	}
}

func TestClaudeACPIdentityRequiresCapabilities(t *testing.T) {
	tests := []struct {
		name           string
		configuredName string
		payload        string
	}{
		{"canonical 配置名", "claude", `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"resume":{}}}}`},
		{"官方 scoped 握手名", "generic", `{"protocolVersion":1,"agentInfo":{"name":"@agentclientprotocol/claude-agent-acp"},"agentCapabilities":{"sessionCapabilities":{"resume":{}}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, pidFile := newCapabilityTestAgent(t, "", tc.payload)
			a.configuredName = tc.configuredName
			t.Cleanup(a.Stop)

			err := a.Start(context.Background())

			assertMissingCapability(t, err, testACPListCapability)
			assertCapabilityTestProcessExited(t, pidFile)
		})
	}
}

// claudeInitializePayload 构造只声明单项 session 能力的标准握手结果。
func claudeInitializePayload(capability string) string {
	return fmt.Sprintf(`{"protocolVersion":%d,`, acpProtocolVersion) +
		`"agentInfo":{"name":"claude-agent-acp","title":"Claude ACP","version":"1.2.3"},` +
		`"agentCapabilities":{"sessionCapabilities":{` + capability + `}}}`
}

// assertMissingCapability 只检查错误中的缺失项子句，避免命中后续固定说明。
func assertMissingCapability(t *testing.T, err error, missing string) {
	t.Helper()
	if err == nil {
		t.Fatal("Start error=nil, want missing capability error")
	}
	clause := strings.SplitN(err.Error(), "；", 2)[0]
	if !strings.Contains(clause, "缺少必需能力 "+missing) {
		t.Fatalf("missing clause=%q, want %s", clause, missing)
	}
	other := testACPListCapability
	if missing == testACPListCapability {
		other = testACPResumeCapability
	}
	if strings.Contains(clause, other) {
		t.Fatalf("missing clause=%q, must not identify %s as missing", clause, other)
	}
}

func TestACPInitializeCachesAgentInfo(t *testing.T) {
	a, _ := newCapabilityTestAgent(t, "generic-agent-acp", `{
		"protocolVersion":1,
		"agentInfo":{"name":"generic-agent","title":"Generic Agent","version":"2.0.0"},
		"agentCapabilities":{"sessionCapabilities":{"list":{},"resume":{}}}
	}`)
	t.Cleanup(a.Stop)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start error=%v", err)
	}
	snapshot := a.acpCapabilitiesSnapshot()
	if snapshot.AgentInfo.Name != "generic-agent" || snapshot.AgentInfo.Title != "Generic Agent" || snapshot.AgentInfo.Version != "2.0.0" {
		t.Fatalf("agentInfo=%+v, want initialized metadata", snapshot.AgentInfo)
	}
	if !snapshot.Session.List || !snapshot.Session.Resume {
		t.Fatalf("session capabilities=%+v, want list and resume", snapshot.Session)
	}
}

func TestNonClaudeACPDoesNotRequireClaudeCapabilities(t *testing.T) {
	a, _ := newCapabilityTestAgent(t, "claude-wrapper", `{
			"protocolVersion":1,
		"agentInfo":{"name":"generic-agent","version":"1.0.0"},
		"agentCapabilities":{}
	}`)
	a.configuredName = "generic"
	t.Cleanup(a.Stop)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("non-Claude Start error=%v, want nil", err)
	}
}

func TestACPInitializeRejectsProtocolVersionMismatch(t *testing.T) {
	tests := []int{0, 2}
	for _, version := range tests {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			payload := fmt.Sprintf(`{"protocolVersion":%d,"agentInfo":{"name":"generic-agent"},"agentCapabilities":{}}`, version)
			_, err := parseACPCapabilitySnapshot(json.RawMessage(payload))
			if err == nil || !containsAll(err.Error(), "protocolVersion", strconv.Itoa(version), strconv.Itoa(acpProtocolVersion)) {
				t.Fatalf("parse error=%v, want protocol version mismatch", err)
			}
		})
	}
}

func TestACPSessionCapabilitiesRequireObjects(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"null", "null"}, {"false", "false"}, {"string", `"yes"`},
		{"number", "1"}, {"array", "[]"},
	}
	for _, capability := range []string{"list", "resume"} {
		for _, tc := range tests {
			t.Run(capability+"/"+tc.name, func(t *testing.T) {
				payload := capabilityTypePayload(capability, tc.value)
				_, err := parseACPCapabilitySnapshot(json.RawMessage(payload))
				if err == nil || !containsAll(err.Error(), "sessionCapabilities."+capability, "JSON object") {
					t.Fatalf("parse error=%v, want clear object type error", err)
				}
			})
		}
	}
}

// capabilityTypePayload 构造包含指定能力值的 initialize result。
func capabilityTypePayload(capability string, value string) string {
	return fmt.Sprintf(
		`{"protocolVersion":%d,"agentInfo":{"name":"generic-agent"},"agentCapabilities":{"sessionCapabilities":{"%s":%s}}}`,
		acpProtocolVersion, capability, value,
	)
}

// newCapabilityTestAgent 用测试二进制模拟不同名称的 ACP adapter。
func newCapabilityTestAgent(t *testing.T, name string, payload string) (*ACPAgent, string) {
	t.Helper()
	command := capabilityTestCommand(t, name)
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	agent := NewACPAgent(ACPAgentConfig{
		Command: command,
		Args:    []string{"-test.run=TestHelperACPCapabilities"},
		Cwd:     t.TempDir(),
		Env: map[string]string{
			testACPCapabilitiesEnv: "1",
			testACPPayloadEnv:      payload,
			testACPPIDFileEnv:      pidFile,
		},
	})
	return agent, pidFile
}

// capabilityTestCommand 按需复制测试二进制，避免依赖文件系统 Symlink 能力。
func capabilityTestCommand(t *testing.T, name string) string {
	t.Helper()
	if name == "" {
		return os.Args[0]
	}
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatalf("open test binary: %v", err)
	}
	defer source.Close()
	command := filepath.Join(t.TempDir(), name)
	target, err := os.OpenFile(command, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatalf("create test agent: %v", err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatalf("copy test agent: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close test agent: %v", err)
	}
	return command
}

// assertCapabilityTestProcessExited 从系统进程表确认 helper 已退出，而非只信任 Agent 内部状态。
func assertCapabilityTestProcessExited(t *testing.T, pidFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Fatal("Windows 尚无可靠的 signal 0 进程退出探测，不能跳过该断言")
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read helper pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse helper pid: %v", err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find helper process: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		signalErr := process.Signal(syscall.Signal(0))
		if errors.Is(signalErr, os.ErrProcessDone) || errors.Is(signalErr, syscall.ESRCH) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("helper process pid=%d still exists", pid)
}

// TestHelperACPCapabilities 返回指定的标准 ACP initialize result。
func TestHelperACPCapabilities(t *testing.T) {
	if os.Getenv(testACPCapabilitiesEnv) != "1" {
		return
	}
	pidFile := os.Getenv(testACPPIDFileEnv)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(5)
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	var request rpcRequest
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
		os.Exit(3)
	}
	payload := json.RawMessage(strings.TrimSpace(os.Getenv(testACPPayloadEnv)))
	response := rpcResponse{JSONRPC: "2.0", ID: &request.ID, Result: payload}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(4)
	}
	for scanner.Scan() {
	}
	os.Exit(0)
}
