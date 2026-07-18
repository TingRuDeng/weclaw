package feishu

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

// deferredCardResultReplier 把超过飞书回调预算的最终文本回写到原卡。
// 若原卡不可更新，显式降级为正常消息，避免吞掉业务结果。
type deferredCardResultReplier struct {
	platform.Replier
	sender    messageSender
	messageID string
}

func newDeferredCardResultReplier(reply platform.Replier, sender messageSender, messageID string) platform.Replier {
	if reply == nil || sender == nil || strings.TrimSpace(messageID) == "" {
		return reply
	}
	return &deferredCardResultReplier{
		Replier: reply, sender: sender, messageID: strings.TrimSpace(messageID),
	}
}

func (r *deferredCardResultReplier) SendText(ctx context.Context, content string) error {
	card := buildChoiceHandledStatusCard("blue", "**会话切换结果**\n\n"+strings.TrimSpace(content))
	cardJSON, err := json.Marshal(card.Data)
	if err == nil {
		err = r.sender.PatchCard(ctx, r.messageID, string(cardJSON))
	}
	if err == nil {
		return nil
	}
	log.Printf("[feishu] failed to update deferred card result, falling back to message: message=%s err=%v", r.messageID, err)
	return r.Replier.SendText(ctx, content)
}
