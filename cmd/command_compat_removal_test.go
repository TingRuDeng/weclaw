package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestUpgradeCommandIsNotRegistered(t *testing.T) {
	if rootCommandHasDirectChild("upgrade") {
		t.Fatal("不应注册 weclaw upgrade；请使用 weclaw update")
	}
	if !rootCommandHasDirectChild("update") {
		t.Fatal("weclaw update 应继续注册")
	}
}

func TestWechatCommandsOnlyUnderWechatNamespace(t *testing.T) {
	for _, name := range []string{"login", "send", "users"} {
		if rootCommandHasDirectChild(name) {
			t.Fatalf("不应注册 weclaw %s；请使用 weclaw wechat %s", name, name)
		}
		if !commandHasDirectChild(wechatCmd, name) {
			t.Fatalf("weclaw wechat %s 应继续注册", name)
		}
	}
}

func rootCommandHasDirectChild(name string) bool {
	return commandHasDirectChild(rootCmd, name)
}

func commandHasDirectChild(cmd commandWithChildren, name string) bool {
	for _, command := range cmd.Commands() {
		if command.Name() == name {
			return true
		}
	}
	return false
}

type commandWithChildren interface {
	Commands() []*cobra.Command
}
