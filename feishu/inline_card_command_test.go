package feishu

import "testing"

func TestClaudeQuotaIsInlineCardCommand(t *testing.T) {
	if !isInlineCardCommand("/cc quota") {
		t.Fatal("/cc quota should execute directly from an inline help card")
	}
}
