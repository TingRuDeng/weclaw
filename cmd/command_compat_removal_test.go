package cmd

import "testing"

func TestUpgradeCommandIsNotRegistered(t *testing.T) {
	if rootCommandHasDirectChild("upgrade") {
		t.Fatal("不应注册 weclaw upgrade；请使用 weclaw update")
	}
	if !rootCommandHasDirectChild("update") {
		t.Fatal("weclaw update 应继续注册")
	}
}

func rootCommandHasDirectChild(name string) bool {
	for _, command := range rootCmd.Commands() {
		if command.Name() == name {
			return true
		}
	}
	return false
}
