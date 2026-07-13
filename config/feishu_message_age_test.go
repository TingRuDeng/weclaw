package config

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestValidateRejectsNegativeFeishuMessageAge 验证负数时效窗口不能进入运行时。
func TestValidateRejectsNegativeFeishuMessageAge(t *testing.T) {
	cfg := decodeFeishuMessageAgeConfig(t, -1)

	err := cfg.Validate()

	if err == nil || !strings.Contains(err.Error(), "max_message_age_seconds") {
		t.Fatalf("Validate error=%v，期望拒绝负数 max_message_age_seconds", err)
	}
}

// TestValidateRejectsOverflowingFeishuMessageAge 验证秒数不能在转为 time.Duration 时溢出。
func TestValidateRejectsOverflowingFeishuMessageAge(t *testing.T) {
	overflowing := int64(time.Duration(1<<63-1)/time.Second) + 1
	cfg := decodeFeishuMessageAgeConfig(t, overflowing)

	err := cfg.Validate()

	if err == nil || !strings.Contains(err.Error(), "max_message_age_seconds") {
		t.Fatalf("Validate error=%v，期望拒绝无法转换为 time.Duration 的秒数", err)
	}
}

// TestFeishuMessageAgeSurvivesJSONRoundTrip 验证显式关闭值不会被 omitempty 丢失。
func TestFeishuMessageAgeSurvivesJSONRoundTrip(t *testing.T) {
	cfg := decodeFeishuMessageAgeConfig(t, 0)

	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"max_message_age_seconds":0`)) {
		t.Fatalf("config=%s，期望保留显式关闭值", encoded)
	}
}

// decodeFeishuMessageAgeConfig 通过真实 JSON 边界构造飞书机器人配置。
func decodeFeishuMessageAgeConfig(t *testing.T, seconds int64) *Config {
	t.Helper()
	raw := []byte(`{"platforms":{"feishu":{"enabled":true,"bots":[{"name":"main","app_id":"cli_a","max_message_age_seconds":` + strconv.FormatInt(seconds, 10) + `}]}}}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	return &cfg
}
