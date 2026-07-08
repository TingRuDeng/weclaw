package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestRunConfigPermissionSetsLevelAndClearsOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{
		Type:             "acp",
		Command:          "codex",
		PermissionLevel:  levelDefault,
		ApprovalPolicy:   "untrusted",
		ApprovalReviewer: "auto_review",
		SandboxMode:      "read-only",
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}

	prompter := &fakeConfigPermissionPrompter{}
	output := captureStdout(t, func() {
		err := runConfigPermission(configPermissionOptions{
			Agent:    "codex",
			Level:    levelAutoReview,
			LevelSet: true,
		}, prompter)
		if err != nil {
			t.Fatalf("runConfigPermission error: %v", err)
		}
	})

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	agentCfg := loaded.Agents["codex"]
	if agentCfg.PermissionLevel != levelAutoReview {
		t.Fatalf("permission_level=%q，期望 %q", agentCfg.PermissionLevel, levelAutoReview)
	}
	if agentCfg.ApprovalPolicy != "" || agentCfg.ApprovalReviewer != "" || agentCfg.SandboxMode != "" {
		t.Fatalf("高级覆盖未清空：%#v", agentCfg)
	}
	for _, want := range []string{"已更新 codex 权限档位：auto_review", "workspace-write + on-request + auto_review reviewer", "已写入：", "请重启"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output=%q，期望包含 %q", output, want)
		}
	}
	if strings.Contains(output, "secret") {
		t.Fatalf("输出不应包含 secret：%s", output)
	}
}

func TestRunConfigPermissionRejectsInvalidLevel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex"}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}

	err := runConfigPermission(configPermissionOptions{
		Agent:    "codex",
		Level:    "auto",
		LevelSet: true,
	}, &fakeConfigPermissionPrompter{})
	if err == nil || !strings.Contains(err.Error(), "无效权限档位") {
		t.Fatalf("error=%v，期望非法权限档位错误", err)
	}
	loaded, loadErr := config.Load()
	if loadErr != nil {
		t.Fatalf("config.Load error: %v", loadErr)
	}
	if loaded.Agents["codex"].PermissionLevel != "" {
		t.Fatalf("非法输入不应写入 permission_level：%#v", loaded.Agents["codex"])
	}
}

func TestRunConfigPermissionRejectsMissingAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(config.DefaultConfig()); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}

	err := runConfigPermission(configPermissionOptions{
		Agent:    "codex",
		Level:    levelDefault,
		LevelSet: true,
	}, &fakeConfigPermissionPrompter{})
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("error=%v，期望 Agent 不存在错误", err)
	}
	loaded, loadErr := config.Load()
	if loadErr != nil {
		t.Fatalf("config.Load error: %v", loadErr)
	}
	if _, ok := loaded.Agents["codex"]; ok {
		t.Fatal("缺失 Agent 时不应自动创建半成品配置")
	}
}

func TestRunConfigPermissionPromptsAgentAndLevel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents["codex"] = config.AgentConfig{Type: "acp", Command: "codex"}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save error: %v", err)
	}

	prompter := &fakeConfigPermissionPrompter{prompts: []string{"codex", levelFullAccess}}
	if err := runConfigPermission(configPermissionOptions{}, prompter); err != nil {
		t.Fatalf("runConfigPermission error: %v", err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error: %v", err)
	}
	if loaded.Agents["codex"].PermissionLevel != levelFullAccess {
		t.Fatalf("permission_level=%q，期望 full_access", loaded.Agents["codex"].PermissionLevel)
	}
	if len(prompter.prompts) != 0 {
		t.Fatalf("仍有未消费输入：%#v", prompter.prompts)
	}
}

type fakeConfigPermissionPrompter struct {
	prompts []string
}

func (p *fakeConfigPermissionPrompter) Prompt(label string, defaultValue string) (string, error) {
	if len(p.prompts) == 0 {
		return "", errors.New("出现未预期的普通输入提示：" + label)
	}
	value := p.prompts[0]
	p.prompts = p.prompts[1:]
	return value, nil
}
