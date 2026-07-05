package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// chatCodex handles codex CLI invocation using "codex exec".
func (a *CLIAgent) chatCodex(ctx context.Context, conversationID string, message string) (string, error) {
	args := []string{"exec", message}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	// Append extra args from config (e.g. --skip-git-repo-check)
	args = append(args, a.args...)

	log.Printf("[cli] running codex exec (command=%s)", a.command)
	command, cmdArgs := a.runAs.wrapCommand(a.command, args)
	cmd := exec.CommandContext(ctx, command, cmdArgs...)
	configureTurnProcess(cmd)
	defer sweepProcessGroup(cmd)
	if cwd := a.cwdForConversation(conversationID); cwd != "" {
		cmd.Dir = cwd
	}
	if len(a.env) > 0 {
		cmdEnv, err := mergeEnv(os.Environ(), a.env)
		if err != nil {
			return "", fmt.Errorf("build %s env: %w", a.name, err)
		}
		cmd.Env = cmdEnv
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("codex error: %w, stderr: %s", err, errMsg)
		}
		return "", fmt.Errorf("codex error: %w", err)
	}

	result := strings.TrimSpace(string(out))
	if result == "" {
		return "", fmt.Errorf("codex returned empty response")
	}
	return result, nil
}
