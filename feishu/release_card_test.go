package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestPrepareDetachFromReferenceUsesSynchronizationLanguage(t *testing.T) {
	payload, err := json.Marshal(feishuStreamReferencePayload{
		CardID: "card-release", Title: "Codex · project", Sequence: 4,
		Summary: "已完成代码检查", Details: "1. 已读取实现\n2. 已补充测试",
		Collapsible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := prepareFeishuDetachFromReference(platform.DurableStreamReference{
		Kind: feishuStreamReferenceKind, Payload: payload,
	}, "已解除当前窗口的会话绑定；本地 Codex 任务继续运行。", "release-1")
	if err != nil {
		t.Fatal(err)
	}
	var op feishuStreamTerminalOp
	if err := json.Unmarshal(checkpoint.Payload, &op); err != nil {
		t.Fatal(err)
	}
	card := decodeCardJSON(t, op.CardJSON)
	elements := card["body"].(map[string]any)["elements"].([]any)
	var status, details string
	for _, raw := range elements {
		element := raw.(map[string]any)
		switch element["element_id"] {
		case "status":
			status, _ = element["content"].(string)
		case cardProgressPanelID:
			panelElements := element["elements"].([]any)
			details, _ = panelElements[0].(map[string]any)["content"].(string)
		}
	}
	if status != "**已停止同步**" {
		t.Fatalf("detach status=%q", status)
	}
	if !strings.Contains(details, "1. 已读取实现") || !strings.Contains(details, "本地 Codex 任务继续运行") {
		t.Fatalf("detach details=%q, want preserved progress and release notice", details)
	}
}
