package feishu

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestTaskControlChoiceCarriesTokenThroughCardAction(t *testing.T) {
	choice := platform.Choice{
		ID: "/cancel", Label: "撤回暂存消息",
		Metadata: map[string]string{
			platform.ChoiceMetadataInteractionKind:  platform.ChoiceInteractionTaskControl,
			platform.ChoiceMetadataAgentName:        "Codex",
			platform.ChoiceMetadataTaskControlToken: "@task_token",
		},
	}
	buttons := buildChoiceButtons([]platform.Choice{choice}, choiceOptions("请选择", []platform.Choice{choice}, "conv"))
	if len(buttons) != 1 {
		t.Fatalf("buttons=%#v", buttons)
	}
	value, ok := buttons[0]["value"].(map[string]string)
	if !ok || value[platform.ChoiceMetadataTaskControlToken] != "@task_token" {
		t.Fatalf("value=%#v, want task control token", buttons[0]["value"])
	}
	if value[platform.ChoiceMetadataAgentName] != "Codex" {
		t.Fatalf("value=%#v, want task control agent name", value)
	}
	for _, command := range []string{"/guide", "/cancel", "/stop"} {
		if !isInlineCardCommand(command) {
			t.Fatalf("%s should update the original task-control card", command)
		}
	}
	actionValue := regularCardActionValue(parsedCardAction{TaskControlToken: "@task_token", AgentName: "Codex"})
	if actionValue[platform.ChoiceMetadataTaskControlToken] != "@task_token" {
		t.Fatalf("action value=%#v, want forwarded task control token", actionValue)
	}
}
