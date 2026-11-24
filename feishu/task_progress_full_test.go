package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func taskProgressElementContent(elements []any, elementID string) string {
	for _, raw := range elements {
		element := raw.(map[string]any)
		if element["element_id"] == elementID {
			if content, ok := element["content"].(string); ok {
				return content
			}
		}
	}
	return ""
}

func assertFullTaskProgress(t *testing.T, raw, want string) {
	t.Helper()
	card := decodeCardJSON(t, raw)
	elements := card["body"].(map[string]any)["elements"].([]any)
	if got := taskProgressElementContent(elements, cardMainContentID); got != want {
		t.Fatalf("progress=%q, want full content=%q", got, want)
	}
	for _, raw := range elements {
		element := raw.(map[string]any)
		if element["tag"] == "button" {
			t.Fatalf("unexpected progress control: %#v", element)
		}
	}
}

func TestTaskCardFullProgressSurvivesUpdatesAndTerminalRecovery(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-full"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	content := strings.Join([]string{
		"> 🧩 **步骤**：读取代码。  \n> ✅ **结果**：已经找到入口。",
		"第二条说明", "第三条说明", "第四条说明", "第五条说明", "第六条说明",
	}, "\n\n")
	active := content + "\n\n" + platform.TaskStreamThinkingIndicator
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialPresentation: &platform.StreamPresentation{Preview: active, Details: active},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := stream.(*feishuStream)
	s.throttle = 0
	assertFullTaskProgress(t, kit.createdCards[0], active)
	if len(kit.updateCards) != 0 {
		t.Fatalf("initial updateCards=%d, want 0 without collapse binding update", len(kit.updateCards))
	}

	content += "\n\n第七条说明"
	active = content + "\n\n" + platform.TaskStreamThinkingIndicator
	if err := s.UpdatePresentation(context.Background(), platform.StreamPresentation{Preview: active, Details: active}); err != nil {
		t.Fatal(err)
	}
	if len(kit.updateCards) != 0 || len(kit.streamTexts) != 1 || kit.streamTexts[0] != active {
		t.Fatalf("updates=%d streams=%#v, want all messages in one component update", len(kit.updateCards), kit.streamTexts)
	}
	reference, err := s.DurableReference()
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []platform.StreamTerminalState{
		platform.StreamTerminalCompleted, platform.StreamTerminalFailed, platform.StreamTerminalStopped,
	} {
		t.Run(string(state), func(t *testing.T) {
			afterRestart := newReplierWithTaskCards(nil, "ou_user", kit, newTaskCardRegistry())
			checkpoint, err := afterRestart.PrepareTerminalFromReferenceWithState(reference, "最终结果", state)
			if err != nil {
				t.Fatal(err)
			}
			if err := afterRestart.DeliverTerminal(context.Background(), checkpoint); err != nil {
				t.Fatal(err)
			}
			assertFullTaskProgress(t, kit.updateCards[len(kit.updateCards)-1], content)
		})
	}
}

func TestLegacyOutboxReferenceWithPreviewAndExpandedRecoversFullProgress(t *testing.T) {
	t.Run("prefers_details_and_ignores_legacy_collapse_fields", func(t *testing.T) {
		kit := &fakeIdempotentCardKitClient{}
		afterRestart := newReplierWithTaskCards(nil, "ou_user", kit, newTaskCardRegistry())
		legacyRef := platform.DurableStreamReference{
			Kind: feishuStreamReferenceKind,
			Payload: json.RawMessage(`{
				"card_id":"card-legacy-1",
				"title":"Codex",
				"sequence":5,
				"summary":"第六步",
				"preview":"第五步\n\n第六步",
				"details":"第一步\n\n第五步\n\n第六步\n\n思考中.....",
				"collapsible":true,
				"expanded":true,
				"approvals":["允许本次：读取文件"]
			}`),
		}
		checkpoint, err := afterRestart.PrepareTerminalFromReferenceWithState(legacyRef, "最终结果", platform.StreamTerminalCompleted)
		if err != nil {
			t.Fatalf("PrepareTerminalFromReferenceWithState: %v", err)
		}
		if err := afterRestart.DeliverTerminal(context.Background(), checkpoint); err != nil {
			t.Fatalf("DeliverTerminal: %v", err)
		}
		assertFullTaskProgress(t, kit.updateCards[len(kit.updateCards)-1], "第一步\n\n第五步\n\n第六步")

		supersede, err := afterRestart.PrepareSupersedeFromReference(legacyRef, "已在下方新卡继续展示。", "reanchor-legacy-preview")
		if err != nil {
			t.Fatalf("PrepareSupersedeFromReference: %v", err)
		}
		var op feishuStreamTerminalOp
		if err := json.Unmarshal(supersede.Payload, &op); err != nil {
			t.Fatalf("decode supersede: %v", err)
		}
		assertFullTaskProgress(t, op.CardJSON, "第一步\n\n第五步\n\n第六步\n\n---\n\n已在下方新卡继续展示。")
	})

	t.Run("falls_back_to_legacy_preview_when_details_and_content_empty", func(t *testing.T) {
		kit := &fakeIdempotentCardKitClient{}
		afterRestart := newReplierWithTaskCards(nil, "ou_user", kit, newTaskCardRegistry())
		legacyRef := platform.DurableStreamReference{
			Kind: feishuStreamReferenceKind,
			Payload: json.RawMessage(`{
				"card_id":"card-legacy-2",
				"title":"Codex",
				"sequence":3,
				"preview":"旧版仅预览进度",
				"collapsible":true,
				"expanded":false
			}`),
		}
		checkpoint, err := afterRestart.PrepareTerminalFromReferenceWithState(legacyRef, "最终结果", platform.StreamTerminalStopped)
		if err != nil {
			t.Fatalf("PrepareTerminalFromReferenceWithState: %v", err)
		}
		if err := afterRestart.DeliverTerminal(context.Background(), checkpoint); err != nil {
			t.Fatalf("DeliverTerminal: %v", err)
		}
		assertFullTaskProgress(t, kit.updateCards[len(kit.updateCards)-1], "旧版仅预览进度")
	})
}

func TestTaskCardFullProgressTerminalIncludesUnstreamedSnapshot(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-terminal-snapshot"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	active := "第一条说明\n\n" + platform.TaskStreamThinkingIndicator
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialPresentation: &platform.StreamPresentation{Preview: active, Details: active},
	})
	if err != nil {
		t.Fatal(err)
	}
	latest := "第一条说明\n\n任务结束前的最后一条说明"
	checkpoint, err := stream.(platform.StatefulDurableTerminalStream).PrepareTerminalWithState(latest, platform.StreamTerminalCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := reply.DeliverTerminal(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	assertFullTaskProgress(t, kit.updateCards[len(kit.updateCards)-1], latest)
	opts, ok := reply.taskCards.snapshot("card-terminal-snapshot")
	if !ok || opts.Content != latest {
		t.Fatalf("terminal registry=%#v, want latest complete snapshot", opts)
	}
}

func TestTaskCardFullProgressFreezeDoesNotRestoreThinkingIndicator(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-frozen"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	active := "完整进度\n\n" + platform.TaskStreamThinkingIndicator
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialPresentation: &platform.StreamPresentation{Preview: active, Details: active},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.(platform.SupersedableStream).Supersede(context.Background(), "后续进度见下一张卡片"); err != nil {
		t.Fatal(err)
	}
	assertFullTaskProgress(t, kit.updateCards[len(kit.updateCards)-1], "完整进度\n\n---\n\n后续进度见下一张卡片")
	opts, ok := reply.taskCards.snapshot("card-frozen")
	if !ok {
		t.Fatal("frozen card snapshot missing")
	}
	raw, err := buildCardV2(opts)
	if err != nil {
		t.Fatal(err)
	}
	assertFullTaskProgress(t, raw, "完整进度")
}
