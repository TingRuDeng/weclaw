package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/feishu"
)

// TestResolveFeishuMaxMessageAge 验证启动时正确解析默认、自定义和关闭值。
func TestResolveFeishuMaxMessageAge(t *testing.T) {
	custom := int64(30)
	disabled := int64(0)
	tests := []struct {
		name string
		bot  config.FeishuBotConfig
		want time.Duration
	}{
		{name: "默认", bot: config.FeishuBotConfig{}, want: feishu.DefaultMessageMaxAge},
		{name: "自定义", bot: config.FeishuBotConfig{MaxMessageAgeSeconds: &custom}, want: 30 * time.Second},
		{name: "关闭", bot: config.FeishuBotConfig{MaxMessageAgeSeconds: &disabled}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveFeishuMaxMessageAge(tt.bot); got != tt.want {
				t.Fatalf("resolveFeishuMaxMessageAge=%s，期望 %s", got, tt.want)
			}
		})
	}
}

// TestUpsertFeishuBotPreservesMessageAge 验证重新配置机器人不会清空已有时效策略。
func TestUpsertFeishuBotPreservesMessageAge(t *testing.T) {
	var cfg config.Config
	raw := []byte(`{"platforms":{"feishu":{"enabled":true,"bots":[{"name":"main","app_id":"cli_old","max_message_age_seconds":30}]}}}`)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	upsertFeishuBotConfig(&cfg, feishuBootstrapOptions{Name: "main", AppID: "cli_new"})

	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"max_message_age_seconds":30`)) {
		t.Fatalf("config=%s，期望保留已有时效策略", encoded)
	}
}
