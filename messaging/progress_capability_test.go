package messaging

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

func TestNormalizeProgressConfigForReplyDowngradesStream(t *testing.T) {
	cfg := normalizeProgressConfigForCapabilities(config.ProgressConfig{Mode: progressModeStream}, platform.Capabilities{Text: true})
	if cfg.Mode != progressModeSummary {
		t.Fatalf("mode=%q, want summary for non-streaming reply", cfg.Mode)
	}
}
