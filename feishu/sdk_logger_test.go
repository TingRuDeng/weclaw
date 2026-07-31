package feishu

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

func TestFeishuSDKLoggerDoesNotForwardUntrustedDetails(t *testing.T) {
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)
	logger := silentFeishuSDKLogger{}
	secret := "wss://example.invalid/connect?access_key=secret&ticket=secret"

	logger.Debug(context.Background(), "payload", secret)
	logger.Info(context.Background(), "connected", secret)
	logger.Warn(context.Background(), "warning", secret)
	logger.Error(context.Background(), "error", secret)

	if strings.Contains(logs.String(), "secret") || logs.Len() != 0 {
		t.Fatalf("SDK logger forwarded untrusted details: %q", logs.String())
	}
}
