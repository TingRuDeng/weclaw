package web

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

// TestFeishuMessageAgeChangeRequiresRestart 验证运行时参数变化不会被误报为热加载完成。
func TestFeishuMessageAgeChangeRequiresRestart(t *testing.T) {
	current := decodeWebFeishuMessageAgeConfig(t, 120)
	next := decodeWebFeishuMessageAgeConfig(t, 30)

	if !restartRequiredConfigChanged(current, next) {
		t.Fatal("修改 max_message_age_seconds 后必须提示重启")
	}
}

// decodeWebFeishuMessageAgeConfig 通过面板使用的配置结构构造测试输入。
func decodeWebFeishuMessageAgeConfig(t *testing.T, seconds int) *config.Config {
	t.Helper()
	raw := []byte(`{"platforms":{"feishu":{"enabled":true,"bots":[{"name":"main","app_id":"cli_a","max_message_age_seconds":` + strconv.Itoa(seconds) + `}]}}}`)
	var cfg config.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	return &cfg
}
