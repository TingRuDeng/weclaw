package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBuildResultCardsPreservesMarkdownAndRewritesLocalLinks(t *testing.T) {
	content := "### 次级问题 #11\n\n请检查 [login.html](/home/debian/workspace/jumpserver/login.html:233)、[登录模板](</home/debian/workspace/My Project/login.html:3>)，并参考 [上游文档](https://example.com/docs)。"

	cards, err := buildResultCards(resultCardOptions{
		Title: "Codex · jumpserver", Status: cardStatusDone, Content: content,
	})
	if err != nil {
		t.Fatalf("buildResultCards: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards=%d, want 1", len(cards))
	}
	card := parseResultCard(t, cards[0])
	if got := resultCardHeaderTitle(t, card); got != "Codex · jumpserver · 最终结果" {
		t.Fatalf("title=%q", got)
	}
	if got := card["header"].(map[string]any)["template"]; got != "green" {
		t.Fatalf("template=%v, want green", got)
	}
	config := card["config"].(map[string]any)
	if streaming, _ := config["streaming_mode"].(bool); streaming {
		t.Fatal("result card must not enable streaming mode")
	}
	markdown := resultCardMainMarkdown(t, card)
	for _, want := range []string{
		"### 次级问题 #11",
		"`/home/debian/workspace/jumpserver/login.html:233`",
		"`/home/debian/workspace/My Project/login.html:3`",
		"[上游文档](https://example.com/docs)",
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("markdown=%q, missing %q", markdown, want)
		}
	}
	if strings.Contains(markdown, "](/home/debian/") {
		t.Fatalf("local path must not remain a non-portable link: %q", markdown)
	}
}

func TestBuildResultCardsUsesTerminalStatusStyle(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantLabel  string
		wantColour string
	}{
		{name: "failed", status: cardStatusError, wantLabel: "**执行失败**", wantColour: "red"},
		{name: "stopped", status: cardStatusStopped, wantLabel: "**已停止**", wantColour: "grey"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cards, err := buildResultCards(resultCardOptions{Title: "Claude", Status: test.status, Content: "结果正文"})
			if err != nil || len(cards) != 1 {
				t.Fatalf("cards=%d err=%v", len(cards), err)
			}
			card := parseResultCard(t, cards[0])
			if got := card["header"].(map[string]any)["template"]; got != test.wantColour {
				t.Fatalf("template=%v, want %s", got, test.wantColour)
			}
			elements := card["body"].(map[string]any)["elements"].([]any)
			if got := elements[0].(map[string]any)["content"]; got != test.wantLabel {
				t.Fatalf("status=%v, want %s", got, test.wantLabel)
			}
		})
	}
}

func TestBuildResultCardsSplitsBeforePayloadLimitWithoutDroppingLines(t *testing.T) {
	lines := make([]string, 0, 2400)
	for index := 1; index <= 2400; index++ {
		lines = append(lines, fmt.Sprintf("- 唯一条目-%04d：验证长结果完整投递", index))
	}

	cards, err := buildResultCards(resultCardOptions{
		Title: "Codex · jumpserver", Status: cardStatusDone, Content: strings.Join(lines, "\n"),
	})
	if err != nil {
		t.Fatalf("buildResultCards: %v", err)
	}
	if len(cards) < 2 {
		t.Fatalf("cards=%d, want automatic continuation", len(cards))
	}
	var rendered strings.Builder
	for index, raw := range cards {
		if size := resultCardMessageEnvelopeSize(t, raw); size > feishuResultMessageJSONSoftLimitBytes {
			t.Fatalf("card %d message size=%d exceeds %d", index+1, size, feishuResultMessageJSONSoftLimitBytes)
		}
		card := parseResultCard(t, raw)
		wantSuffix := fmt.Sprintf(" · %d/%d", index+1, len(cards))
		if title := resultCardHeaderTitle(t, card); !strings.HasSuffix(title, wantSuffix) {
			t.Fatalf("card %d title=%q, want suffix %q", index+1, title, wantSuffix)
		}
		rendered.WriteString(resultCardMainMarkdown(t, card))
		rendered.WriteByte('\n')
	}
	combined := rendered.String()
	for _, line := range lines {
		if count := strings.Count(combined, line); count != 1 {
			t.Fatalf("line %q count=%d, want exactly once", line, count)
		}
	}
}

func TestBuildResultCardsPreflightsEscapedMessageEnvelope(t *testing.T) {
	content := strings.Repeat(`"\\`, 12000)
	cards, err := buildResultCards(resultCardOptions{
		Title: "Codex · jumpserver", Status: cardStatusDone, Content: content,
	})
	if err != nil {
		t.Fatalf("buildResultCards: %v", err)
	}
	if len(cards) < 2 {
		t.Fatalf("cards=%d, want escaped content to continue", len(cards))
	}
	for index, raw := range cards {
		if size := resultCardMessageEnvelopeSize(t, raw); size > feishuResultMessageJSONSoftLimitBytes {
			t.Fatalf("card %d message size=%d exceeds %d", index+1, size, feishuResultMessageJSONSoftLimitBytes)
		}
	}
}

func resultCardMessageEnvelopeSize(t *testing.T, cardJSON string) int {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"receive_id": strings.Repeat("x", feishuResultMessageReceiveIDReserveBytes),
		"msg_type":   "interactive",
		"content":    cardJSON,
		"uuid":       strings.Repeat("x", 36),
	})
	if err != nil {
		t.Fatalf("marshal result message envelope: %v", err)
	}
	return len(payload)
}

func parseResultCard(t *testing.T, raw string) map[string]any {
	t.Helper()
	var card map[string]any
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("parse card: %v", err)
	}
	return card
}

func resultCardHeaderTitle(t *testing.T, card map[string]any) string {
	t.Helper()
	header := card["header"].(map[string]any)
	title := header["title"].(map[string]any)
	return title["content"].(string)
}

func resultCardMainMarkdown(t *testing.T, card map[string]any) string {
	t.Helper()
	elements := card["body"].(map[string]any)["elements"].([]any)
	for _, raw := range elements {
		element := raw.(map[string]any)
		if element["element_id"] == cardMainContentID {
			return element["content"].(string)
		}
	}
	t.Fatal("main markdown element missing")
	return ""
}
