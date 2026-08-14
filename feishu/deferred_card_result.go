package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const feishuCommandResultReferenceKind = "feishu_command_result_v1"

// deferredCardResultReplier 把超过飞书回调预算的最终文本回写到原卡。
// 若原卡不可更新，显式降级为正常消息，避免吞掉业务结果。
type deferredCardResultReplier struct {
	platform.Replier
	sender    messageSender
	messageID string
	title     string
	command   string
}

func newDeferredCardResultReplier(reply platform.Replier, sender messageSender, messageID string) platform.Replier {
	return newDeferredCardResultReplierWithTitle(reply, sender, messageID, "会话切换结果")
}

func newDeferredCardResultReplierWithTitle(reply platform.Replier, sender messageSender, messageID string, title string, command ...string) platform.Replier {
	if reply == nil || sender == nil || strings.TrimSpace(messageID) == "" {
		return reply
	}
	if title = strings.TrimSpace(title); title == "" {
		title = "操作结果"
	}
	return &deferredCardResultReplier{
		Replier: reply, sender: sender, messageID: strings.TrimSpace(messageID), title: title,
		command: strings.TrimSpace(firstString(command)),
	}
}

func (r *deferredCardResultReplier) SendText(ctx context.Context, content string) error {
	card := buildChoiceHandledStatusCardWithTitle(
		choiceCommandResultTemplate(r.command, content), r.title, strings.TrimSpace(content),
	)
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

// DurableCommandResultReference 导出原卡消息定位，供运行通道稍后恢复时原地收敛结果。
func (r *deferredCardResultReplier) DurableCommandResultReference() (platform.DurableCommandResultReference, error) {
	reference := platform.DurableCommandResultReference{
		Kind: feishuCommandResultReferenceKind, TargetID: strings.TrimSpace(r.messageID),
		Title: strings.TrimSpace(r.title), Command: strings.TrimSpace(r.command),
		ReadyAfter: time.Now().Add(feishuInlineCardActionTimeout).UTC().Format(time.RFC3339Nano),
	}
	if !reference.Valid() {
		return platform.DurableCommandResultReference{}, fmt.Errorf("飞书命令结果卡引用不完整")
	}
	return reference, nil
}

// DeliverCommandResult 根据持久化引用更新原命令结果卡，不创建额外消息。
func (r *Replier) DeliverCommandResult(ctx context.Context, reference platform.DurableCommandResultReference, content string) error {
	if strings.TrimSpace(reference.Kind) != feishuCommandResultReferenceKind {
		return platform.ErrUnsupported
	}
	messageID := strings.TrimSpace(reference.TargetID)
	if r == nil || r.sender == nil || messageID == "" {
		return fmt.Errorf("飞书命令结果卡引用不完整")
	}
	card := buildChoiceHandledStatusCardWithTitle(
		choiceCommandResultTemplate(reference.Command, content), reference.Title, strings.TrimSpace(content),
	)
	cardJSON, err := json.Marshal(card.Data)
	if err != nil {
		return err
	}
	return r.sender.PatchCard(ctx, messageID, string(cardJSON))
}

func (r *deferredCardResultReplier) patchChoiceCard(ctx context.Context, prompt string, choices []platform.Choice, conversationKey string) error {
	cardJSON, err := buildChoiceCard(prompt, choices, conversationKey)
	if err == nil {
		err = r.sender.PatchCard(ctx, r.messageID, cardJSON)
	}
	if err == nil {
		return nil
	}
	log.Printf("[feishu] failed to update deferred choice card, falling back to message: message=%s err=%v", r.messageID, err)
	return r.Replier.AskChoices(ctx, prompt, choices)
}

// ProgressReplier 将任务卡和终态 outbox 绑定到真实会话回复器，原卡 patch 只处理命令结果。
func (r *deferredCardResultReplier) ProgressReplier() platform.Replier {
	return r.Replier
}

func (r *deferredCardResultReplier) BindTaskCard(cardID string) {
	if binder, ok := r.Replier.(platform.TaskCardBinder); ok {
		binder.BindTaskCard(cardID)
	}
}
