package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
)

func TestRunFeishuAddPromptsAndBootstrapsBot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restoreFeishuCredentialHooks(t)

	var savedName string
	var saved feishu.Credentials
	validateFeishuCreds = func(ctx context.Context, creds feishu.Credentials) error {
		if creds.AppID != "cli_a" || creds.AppSecret != "secret-a" {
			t.Fatalf("creds=%#v，期望使用交互输入的凭证", creds)
		}
		return nil
	}
	saveFeishuCreds = func(name string, creds feishu.Credentials) error {
		savedName = name
		saved = creds
		return nil
	}

	prompter := &fakeFeishuAddPrompter{
		prompts: []string{
			"project-a",
			"卡片管家",
			"信用卡管理, 安卓卡管",
			"cli_a",
			"ou_1, union_2",
			"codex",
			"stream",
		},
		secret: "secret-a",
		bools:  []bool{false},
	}
	output := captureStdout(t, func() {
		if err := runFeishuAdd(context.Background(), feishuAddOptions{}, prompter); err != nil {
			t.Fatalf("runFeishuAdd 返回错误：%v", err)
		}
	})

	if savedName != "project-a" || saved.AppID != "cli_a" || saved.AppSecret != "secret-a" {
		t.Fatalf("保存结果 name=%q creds=%#v，期望 project-a/cli_a/secret-a", savedName, saved)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load 返回错误：%v", err)
	}
	bots := cfg.Platforms["feishu"].Bots
	if len(bots) != 1 {
		t.Fatalf("bots=%#v，期望只有一个 bot", bots)
	}
	bot := bots[0]
	if bot.Name != "project-a" || bot.AppID != "cli_a" {
		t.Fatalf("bot=%#v，期望 project-a/cli_a", bot)
	}
	if bot.DisplayName != "卡片管家" {
		t.Fatalf("display name=%q，期望中文展示名", bot.DisplayName)
	}
	if strings.Join(bot.Aliases, ",") != "信用卡管理,安卓卡管" {
		t.Fatalf("aliases=%#v，期望中文别名", bot.Aliases)
	}
	if strings.Join(bot.AllowedUsers, ",") != "ou_1,union_2" {
		t.Fatalf("allowed users=%#v，期望 ou_1,union_2", bot.AllowedUsers)
	}
	if bot.DefaultAgent != "codex" {
		t.Fatalf("default agent=%q，期望 codex", bot.DefaultAgent)
	}
	if bot.Progress == nil || bot.Progress.Mode != "stream" {
		t.Fatalf("progress=%#v，期望 stream", bot.Progress)
	}
	if bot.RequireMentionInGroup == nil || *bot.RequireMentionInGroup {
		t.Fatalf("require mention=%#v，期望 false", bot.RequireMentionInGroup)
	}
	if strings.Contains(output, "secret-a") {
		t.Fatalf("add 输出泄露 secret：%s", output)
	}
}

type fakeFeishuAddPrompter struct {
	prompts []string
	secret  string
	bools   []bool
}

func (p *fakeFeishuAddPrompter) Prompt(label string, defaultValue string) (string, error) {
	if len(p.prompts) == 0 {
		return "", errors.New("出现未预期的普通输入提示：" + label)
	}
	value := p.prompts[0]
	p.prompts = p.prompts[1:]
	return value, nil
}

func (p *fakeFeishuAddPrompter) PromptSecret(label string) (string, error) {
	if p.secret == "" {
		return "", errors.New("出现未预期的 secret 输入提示：" + label)
	}
	value := p.secret
	p.secret = ""
	return value, nil
}

func (p *fakeFeishuAddPrompter) PromptBool(label string, defaultValue bool) (bool, error) {
	if len(p.bools) == 0 {
		return false, errors.New("出现未预期的布尔输入提示：" + label)
	}
	value := p.bools[0]
	p.bools = p.bools[1:]
	return value, nil
}
