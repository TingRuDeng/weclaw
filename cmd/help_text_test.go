package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootHelpUsesChineseProductDescription(t *testing.T) {
	output := commandHelpText(t, rootCmd)
	for _, want := range []string{
		"WeClaw 连接微信、飞书和 AI Agent",
		"启动消息服务",
		"管理飞书机器人",
		"管理本机配置",
		"检查运行状态",
		"更新 WeClaw",
		"查看版本",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("root help=%q, want %q", output, want)
		}
	}
	for _, hidden := range []string{"WeChat AI agent bridge", "Start the message bridge", "Manage Feishu platform credentials", "Help about any command", "help for weclaw", "version for weclaw", "CLI Companion"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("root help=%q, should not contain old English description %q", output, hidden)
		}
	}
}

func TestFeishuHelpHighlightsFriendlyCommands(t *testing.T) {
	output := commandHelpText(t, feishuCmd)
	for _, want := range []string{
		"管理飞书机器人",
		"add",
		"status",
		"users",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("feishu help=%q, want %q", output, want)
		}
	}
	for _, hidden := range []string{"login", "bootstrap", "Manage Feishu platform credentials", "help for feishu"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("feishu help=%q, should hide advanced command or old English text %q", output, hidden)
		}
	}
}

func commandHelpText(t *testing.T, command *cobra.Command) string {
	t.Helper()
	prepareRootCommandHelp()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	if err := command.Help(); err != nil {
		t.Fatalf("Help error: %v", err)
	}
	return out.String()
}
