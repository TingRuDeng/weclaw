package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
)

func assertFullTaskProgress(t *testing.T, raw, want string) {
	t.Helper()
	card := decodeCardJSON(t, raw)
	elements := card["body"].(map[string]any)["elements"].([]any)
	if got := taskProgressElementContent(elements, cardMainContentID); got != want {
		t.Fatalf("progress=%q, want full content=%q", got, want)
	}
	for _, raw := range elements {
		element := raw.(map[string]any)
		if id := element["element_id"]; id == cardProgressExpandID || id == cardProgressCollapseID {
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
	assertFullTaskProgress(t, kit.updateCards[0], active)

	content += "\n\n第七条说明"
	active = content + "\n\n" + platform.TaskStreamThinkingIndicator
	if err := s.UpdatePresentation(context.Background(), platform.StreamPresentation{Preview: active, Details: active}); err != nil {
		t.Fatal(err)
	}
	if len(kit.updateCards) != 1 || len(kit.streamTexts) != 1 || kit.streamTexts[0] != active {
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

func TestTaskCardFullProgressRemovesLegacyControls(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		name := "collapsed"
		if expanded {
			name = "expanded"
		}
		t.Run(name, func(t *testing.T) {
			kit := &fakeCardKitClient{cardID: "card-legacy"}
			registry := newTaskCardRegistry()
			reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, registry)
			stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
				Title: "Codex", InitialPresentation: &platform.StreamPresentation{Preview: "第六条说明", Details: "第一条说明\n\n第六条说明"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if expanded {
				registry.setExpandedWithSequence("card-legacy", true)
			}
			s := stream.(*feishuStream)
			s.throttle = 0
			content := "第一条说明\n\n第六条说明\n\n第七条说明"
			if err := s.UpdatePresentation(context.Background(), platform.StreamPresentation{Preview: content, Details: content}); err != nil {
				t.Fatal(err)
			}
			if len(kit.updateCards) != 2 || len(kit.streamTexts) != 0 {
				t.Fatalf("updates=%d streams=%d, want one layout update to remove legacy controls", len(kit.updateCards), len(kit.streamTexts))
			}
			assertFullTaskProgress(t, kit.updateCards[1], content)
			content += "\n\n第八条说明"
			if err := s.UpdatePresentation(context.Background(), platform.StreamPresentation{Preview: content, Details: content}); err != nil {
				t.Fatal(err)
			}
			if len(kit.updateCards) != 2 || len(kit.streamTexts) != 1 || kit.streamTexts[0] != content {
				t.Fatalf("updates=%d streams=%#v, want subsequent updates to stream full content", len(kit.updateCards), kit.streamTexts)
			}
		})
	}
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
	if !ok || opts.Preview != latest || opts.Content != latest {
		t.Fatalf("terminal registry=%#v, want latest complete snapshot", opts)
	}
}

func TestTaskCardFullProgressRetriesFailedLayoutChange(t *testing.T) {
	kit := &fakeCardKitClient{cardID: "card-retry"}
	reply := newReplierWithTaskCards(&fakeMessageSender{}, "ou_user", kit, newTaskCardRegistry())
	stream, err := reply.OpenStream(context.Background(), platform.StreamOptions{
		Title: "Codex", InitialPresentation: &platform.StreamPresentation{Preview: "预览", Details: "完整说明"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := stream.(*feishuStream)
	s.throttle = 0
	p := platform.StreamPresentation{Preview: "完整说明", Details: "完整说明"}
	kit.updateErrors = []error{errors.New("update failed")}
	if err := s.UpdatePresentation(context.Background(), p); err == nil {
		t.Fatal("expected layout update failure")
	}
	if err := s.UpdatePresentation(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(kit.updateCards) != 3 || len(kit.streamTexts) != 0 {
		t.Fatalf("updates=%d streams=%d, want layout retry before component updates", len(kit.updateCards), len(kit.streamTexts))
	}
	assertFullTaskProgress(t, kit.updateCards[2], p.Details)
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
