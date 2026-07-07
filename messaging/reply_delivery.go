package messaging

import (
	"context"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

// sendReplyWithMedia sends a text reply and any extracted image URLs.
func (h *Handler) sendReplyWithMedia(ctx context.Context, replyWriter platform.Replier, userID string, agentName string, reply string) {
	h.sendReplyWithMediaAfterStream(ctx, replyWriter, userID, agentName, reply, false)
}

func (h *Handler) sendReplyWithMediaAfterStream(ctx context.Context, replyWriter platform.Replier, userID string, agentName string, reply string, finalInStream bool) {
	h.sendReplyWithMediaAfterStreamWithMetadata(ctx, replyWriter, userID, agentName, reply, finalInStream, nil)
}

func (h *Handler) sendReplyWithMediaForRoute(ctx context.Context, replyWriter platform.Replier, userID string, routeUserID string, agentName string, reply string) {
	h.sendReplyWithMediaAfterStreamForRoute(ctx, replyWriter, userID, routeUserID, agentName, reply, false)
}

func (h *Handler) sendReplyWithMediaAfterStreamForRoute(ctx context.Context, replyWriter platform.Replier, userID string, routeUserID string, agentName string, reply string, finalInStream bool) {
	metadata := map[string]string{}
	if sessionKey := feishuSessionKeyFromRoute(routeUserID); sessionKey != "" {
		metadata[feishuSessionMetadataKey] = sessionKey
	}
	h.sendReplyWithMediaAfterStreamWithMetadata(ctx, replyWriter, userID, agentName, reply, finalInStream, metadata)
}

func (h *Handler) sendReplyWithMediaAfterStreamWithMetadata(ctx context.Context, replyWriter platform.Replier, userID string, agentName string, reply string, finalInStream bool, choiceMetadata map[string]string) {
	imageURLs := ExtractImageURLs(reply)
	attachmentPaths := extractLocalAttachmentPaths(reply)
	allowedRoots := h.allowedAttachmentRoots(agentName)

	var sentPaths []string
	var failedPaths []string
	for _, attachmentPath := range attachmentPaths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			log.Printf("[handler] rejected attachment outside allowed roots for agent %q: %s", agentName, attachmentPath)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		var sendErr error
		if isImageAttachmentPath(attachmentPath) {
			sendErr = replyWriter.SendImage(ctx, attachmentPath)
		} else {
			sendErr = replyWriter.SendFile(ctx, attachmentPath)
		}
		if sendErr != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", userID, sendErr)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
	}

	reply = rewriteReplyWithAttachmentResults(reply, sentPaths, failedPaths)
	choiceResult, hasChoices := detectChoices(reply)
	if hasChoices {
		reply = choiceResult.CleanText
		choiceResult.Choices = platformChoicesWithMetadata(choiceResult.Choices, choiceMetadata)
	}

	if wxReply, ok := replyWriter.(*wechat.Replier); ok {
		wxReply.ChunkRunes = textReplyChunkLimit(ctx)
	}
	if !finalInStream && strings.TrimSpace(reply) != "" {
		if err := replyWriter.SendText(ctx, reply); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", userID, err)
		}
	}
	if hasChoices {
		if err := replyWriter.AskChoices(ctx, choiceResult.Prompt, choiceResult.Choices); err != nil {
			log.Printf("[handler] failed to send choices to %s: %v", userID, err)
		}
	}

	for _, imgURL := range imageURLs {
		if wxReply, ok := replyWriter.(*wechat.Replier); ok {
			if err := wxReply.SendMediaFromURL(ctx, imgURL); err != nil {
				log.Printf("[handler] failed to send image to %s: %v", userID, err)
			}
			continue
		}
		log.Printf("[handler] skip remote image for %s: platform replier has no URL media sender", userID)
	}
}

func finalCardReplyText(reply string) string {
	choiceResult, hasChoices := detectChoices(reply)
	if hasChoices {
		return choiceResult.CleanText
	}
	return reply
}

func finishProgressWithReply(finish func(string, bool) bool, reply string, failed bool) bool {
	if !canConsumeFinalReplyInStream(reply) {
		_ = finish("", failed)
		return false
	}
	return finish(finalCardReplyText(reply), failed)
}

func finishProgressWithReplyForPlatform(replyWriter platform.Replier, finish func(string, bool) bool, reply string, failed bool) bool {
	if shouldKeepFinalReplyOutsideStream(replyWriter, reply) {
		_ = finish("", failed)
		return false
	}
	return finishProgressWithReply(finish, reply, failed)
}

func shouldKeepFinalReplyOutsideStream(replyWriter platform.Replier, reply string) bool {
	if replyWriter == nil || strings.TrimSpace(reply) == "" {
		return false
	}
	return replyWriter.Capabilities().FinalReplyOutsideStream && canConsumeFinalReplyInStream(reply)
}

func canConsumeFinalReplyInStream(reply string) bool {
	return len(ExtractImageURLs(reply)) == 0 && len(extractLocalAttachmentPaths(reply)) == 0
}

func sendPlatformText(ctx context.Context, reply platform.Replier, userID string, text string) {
	if reply == nil {
		return
	}
	if err := reply.SendText(ctx, text); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", userID, err)
	}
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
