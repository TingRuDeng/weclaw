package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
)

// streamEvent represents a single event from claude's stream-json output.
type streamEvent struct {
	Type      string         `json:"type"`
	SessionID string         `json:"session_id"`
	Result    string         `json:"result"`
	IsError   bool           `json:"is_error"`
	Message   *streamMessage `json:"message,omitempty"`
}

// streamMessage represents the message field in an assistant event.
type streamMessage struct {
	Content []streamContent `json:"content"`
}

// streamContent represents a content block in an assistant message.
type streamContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeInvocationConfig struct {
	SessionID  string
	HasSession bool
	Model      string
	Effort     string
}

type claudeCommandRun struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *strings.Builder
}

type claudeCommandOutput struct {
	result        string
	sessionID     string
	assistantText []string
}

// claudeInvocationState 原子读取 session，并为新会话捕获当前 Agent 级模型配置。
func (a *CLIAgent) claudeInvocationState(conversationID string) claudeInvocationConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	sessionID, hasSession := a.sessions[conversationID]
	if !hasSession {
		a.conversationModels[conversationID] = a.model
		a.conversationEfforts[conversationID] = a.effort
	}
	return claudeInvocationConfig{
		SessionID: sessionID, HasSession: hasSession,
		Model: a.conversationModels[conversationID], Effort: a.conversationEfforts[conversationID],
	}
}

// chatClaude uses claude -p with stream-json to get structured output and session persistence.
func (a *CLIAgent) chatClaude(ctx context.Context, conversationID string, message string) (string, error) {
	args := a.claudeCommandArgs(conversationID, message)
	run, err := a.startClaudeCommand(ctx, conversationID, args)
	if err != nil {
		return "", err
	}
	defer sweepProcessGroup(run.cmd)
	output, err := readClaudeCommandOutput(run.stdout, a.name)
	if err != nil {
		return "", err
	}
	if output.result == "" && len(output.assistantText) > 0 {
		output.result = strings.Join(output.assistantText, "")
	}
	if err := waitClaudeCommand(run, output.result, a.name); err != nil {
		return "", err
	}
	log.Printf("[cli] process exited (command=%s, pid=%d)", a.command, run.cmd.Process.Pid)
	a.saveClaudeSession(conversationID, output.sessionID)
	result := strings.TrimSpace(output.result)
	if result == "" {
		return "", fmt.Errorf("%s returned empty response", a.name)
	}
	return result, nil
}

// claudeCommandArgs 使用 conversation 捕获的配置构造一次 Claude CLI 调用参数。
func (a *CLIAgent) claudeCommandArgs(conversationID string, message string) []string {
	args := []string{"-p", message, "--output-format", "stream-json", "--verbose"}
	invocation := a.claudeInvocationState(conversationID)
	if invocation.Model != "" {
		args = append(args, "--model", invocation.Model)
	}
	if invocation.Effort != "" {
		args = append(args, "--effort", invocation.Effort)
	}
	if a.systemPrompt != "" {
		args = append(args, "--append-system-prompt", a.systemPrompt)
	}
	// Append extra args from config (e.g. --dangerously-skip-permissions)
	args = append(args, a.args...)
	if invocation.HasSession {
		args = append(args, "--resume", invocation.SessionID)
		log.Printf("[cli] resuming session (command=%s, session=%s, conversation=%s)", a.command, invocation.SessionID, conversationID)
	} else {
		log.Printf("[cli] starting new conversation (command=%s, conversation=%s)", a.command, conversationID)
	}
	return args
}

// startClaudeCommand 启动独立进程组并返回流式输出句柄。
func (a *CLIAgent) startClaudeCommand(ctx context.Context, conversationID string, args []string) (claudeCommandRun, error) {
	command, cmdArgs := a.runAs.wrapCommand(a.command, args)
	cmd := exec.CommandContext(ctx, command, cmdArgs...)
	configureTurnProcess(cmd)
	if cwd := a.cwdForConversation(conversationID); cwd != "" {
		cmd.Dir = cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return claudeCommandRun{}, fmt.Errorf("build %s env: %w", a.name, err)
		}
		cmd.Env = cmdEnv
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return claudeCommandRun{}, fmt.Errorf("create stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return claudeCommandRun{}, fmt.Errorf("start %s: %w", a.name, err)
	}
	log.Printf("[cli] spawned process (command=%s, pid=%d, conversation=%s)", a.command, cmd.Process.Pid, conversationID)
	return claudeCommandRun{cmd: cmd, stdout: stdout, stderr: stderr}, nil
}

// readClaudeCommandOutput 解析 Claude stream-json 并汇总最终回复和 session ID。
func readClaudeCommandOutput(stdout io.Reader, agentName string) (claudeCommandOutput, error) {
	var output claudeCommandOutput
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for large responses
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if event.SessionID != "" {
			output.sessionID = event.SessionID
		}
		switch event.Type {
		case "result":
			if event.IsError {
				return claudeCommandOutput{}, fmt.Errorf("%s returned error: %s", agentName, event.Result)
			}
			output.result = event.Result
		case "assistant":
			if event.Message != nil {
				for _, c := range event.Message.Content {
					if c.Type == "text" && c.Text != "" {
						output.assistantText = append(output.assistantText, c.Text)
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return claudeCommandOutput{}, fmt.Errorf("read Claude output: %w", err)
	}
	return output, nil
}

// waitClaudeCommand 保留“已有结果时容忍 hook 非零退出”的原有语义。
func waitClaudeCommand(run claudeCommandRun, result string, agentName string) error {
	if err := run.cmd.Wait(); err != nil {
		if result == "" {
			errMsg := strings.TrimSpace(run.stderr.String())
			if errMsg != "" {
				return fmt.Errorf("%s exited with error: %w, stderr: %s", agentName, err, errMsg)
			}
			return fmt.Errorf("%s exited with error: %w", agentName, err)
		}
	}
	return nil
}

// saveClaudeSession 保存 Claude CLI 返回的 session ID。
func (a *CLIAgent) saveClaudeSession(conversationID string, sessionID string) {
	if sessionID == "" {
		return
	}
	a.mu.Lock()
	a.sessions[conversationID] = sessionID
	a.mu.Unlock()
	log.Printf("[cli] saved session (session=%s, conversation=%s)", sessionID, conversationID)
}
