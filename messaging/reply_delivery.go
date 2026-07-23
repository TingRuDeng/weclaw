package messaging

import (
	"context"
	"log"
	"strings"

	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/fastclaw-ai/weclaw/platform"
)

// sendReplyWithMedia sends a text reply and any extracted image URLs.
func (h *Handler) sendReplyWithMedia(ctx context.Context, replyWriter platform.Replier, userID string, agentName string, reply string) {
	h.sendReplyWithMediaAfterStream(ctx, replyWriter, userID, agentName, reply, false)
}

func (h *Handler) sendReplyWithMediaAfterStream(ctx context.Context, replyWriter platform.Replier, userID string, agentName string, reply string, finalInStream bool) {
	h.sendReplyWithMediaAfterStreamCore(ctx, replyWriter, userID, agentName, reply, finalInStream)
}

func (h *Handler) sendReplyWithMediaForRoute(ctx context.Context, replyWriter platform.Replier, userID string, routeUserID string, agentName string, reply string) {
	h.sendReplyWithMediaAfterStreamForRoute(ctx, replyWriter, userID, routeUserID, agentName, reply, false)
}

func (h *Handler) sendReplyWithMediaAfterStreamForRoute(ctx context.Context, replyWriter platform.Replier, userID string, _ string, agentName string, reply string, finalInStream bool) {
	h.sendReplyWithMediaAfterStreamCore(ctx, replyWriter, userID, agentName, reply, finalInStream)
}

type replyDeliveryRequest struct {
	ctx         context.Context
	replyWriter platform.Replier
	userID      string
	agentName   string
	reply       string
	trace       observability.TraceContext
}

type progressReplyDelivery struct {
	delivery replyDeliveryRequest
	failed   bool
	finish   func(string, bool) bool
	progress *progressSession
}

type replyDeliveryProjection struct {
	text      string
	imageURLs []string
}

func (h *Handler) sendReplyWithMediaAfterStreamCore(ctx context.Context, replyWriter platform.Replier, userID string, agentName string, reply string, finalInStream bool) {
	trace, _ := observability.TraceFromContext(ctx)
	req := replyDeliveryRequest{
		ctx: ctx, replyWriter: replyWriter, userID: userID,
		agentName: agentName, reply: reply, trace: traceWithReply(trace, replyWriter),
	}
	projection := h.prepareReplyDelivery(req)
	h.sendReplyProjection(req, projection, finalInStream)
}

func (h *Handler) prepareReplyDelivery(req replyDeliveryRequest) replyDeliveryProjection {
	attachmentPaths := extractLocalAttachmentPaths(req.reply)
	sentPaths, failedPaths := h.sendLocalAttachments(req, attachmentPaths)
	return replyDeliveryProjection{
		text:      rewriteReplyWithAttachmentResults(req.reply, sentPaths, failedPaths),
		imageURLs: ExtractImageURLs(req.reply),
	}
}

func (h *Handler) sendLocalAttachments(req replyDeliveryRequest, paths []string) ([]string, []string) {
	allowedRoots := h.allowedAttachmentRoots(req.agentName)
	var sentPaths []string
	var failedPaths []string
	for _, attachmentPath := range paths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			log.Printf("[handler] rejected attachment outside allowed roots for agent %q: %s", req.agentName, attachmentPath)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		var sendErr error
		if isImageAttachmentPath(attachmentPath) {
			sendErr = req.replyWriter.SendImage(req.ctx, attachmentPath)
		} else {
			sendErr = req.replyWriter.SendFile(req.ctx, attachmentPath)
		}
		if sendErr != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", req.userID, sendErr)
			failedPaths = append(failedPaths, attachmentPath)
			continue
		}
		sentPaths = append(sentPaths, attachmentPath)
	}
	return sentPaths, failedPaths
}

func (h *Handler) sendReplyProjection(req replyDeliveryRequest, projection replyDeliveryProjection, finalInStream bool) {
	if setter, ok := optionalTextChunkLimitSetter(req.replyWriter); ok {
		setter.SetTextChunkLimit(textReplyChunkLimit(req.ctx))
	}
	attempted := (!finalInStream && strings.TrimSpace(projection.text) != "") || len(projection.imageURLs) > 0
	if attempted {
		h.recordTraceStage(req.trace, "reply.delivery.attempt", "running", "sending reply projection")
	}
	deliveryFailure := ""
	if !finalInStream && strings.TrimSpace(projection.text) != "" {
		if err := req.replyWriter.SendText(req.ctx, projection.text); err != nil {
			log.Printf("[handler] failed to send reply to %s: %v", req.userID, err)
			deliveryFailure = err.Error()
		}
	}

	for _, imgURL := range projection.imageURLs {
		if sender, ok := optionalRemoteMediaSender(req.replyWriter); ok {
			if err := sender.SendMediaFromURL(req.ctx, imgURL); err != nil {
				log.Printf("[handler] failed to send image to %s: %v", req.userID, err)
				deliveryFailure = err.Error()
			}
			continue
		}
		log.Printf("[handler] skip remote image for %s: platform replier has no URL media sender", req.userID)
		if deliveryFailure == "" {
			deliveryFailure = "platform replier has no URL media sender"
		}
	}
	if !attempted {
		return
	}
	if deliveryFailure != "" {
		h.recordTraceStage(req.trace, "reply.delivery.failed", "failed", deliveryFailure)
		return
	}
	h.recordTraceStage(req.trace, "reply.delivery.completed", "completed", "reply projection sent")
}

func optionalTextChunkLimitSetter(reply platform.Replier) (platform.TextChunkLimitSetter, bool) {
	if serialized, ok := reply.(*serializedReplier); ok {
		setter, supported := optionalTextChunkLimitSetter(serialized.inner)
		if !supported {
			return nil, false
		}
		return serializedTextChunkLimitSetter{reply: serialized, setter: setter}, true
	}
	setter, ok := reply.(platform.TextChunkLimitSetter)
	return setter, ok
}

func optionalRemoteMediaSender(reply platform.Replier) (platform.RemoteMediaSender, bool) {
	if serialized, ok := reply.(*serializedReplier); ok {
		sender, supported := optionalRemoteMediaSender(serialized.inner)
		if !supported {
			return nil, false
		}
		return serializedRemoteMediaSender{reply: serialized, sender: sender}, true
	}
	sender, ok := reply.(platform.RemoteMediaSender)
	return sender, ok
}

type serializedTextChunkLimitSetter struct {
	reply  *serializedReplier
	setter platform.TextChunkLimitSetter
}

func (s serializedTextChunkLimitSetter) SetTextChunkLimit(maxRunes int) {
	s.reply.mu.Lock()
	defer s.reply.mu.Unlock()
	s.setter.SetTextChunkLimit(maxRunes)
}

type serializedRemoteMediaSender struct {
	reply  *serializedReplier
	sender platform.RemoteMediaSender
}

func (s serializedRemoteMediaSender) SendMediaFromURL(ctx context.Context, mediaURL string) error {
	s.reply.mu.Lock()
	defer s.reply.mu.Unlock()
	return s.sender.SendMediaFromURL(ctx, mediaURL)
}

func (h *Handler) finishAndSendProgressReply(req progressReplyDelivery) bool {
	if req.progress != nil {
		if current := req.progress.currentTerminalReply(); current != nil {
			req.delivery.replyWriter = current
			req.delivery.trace = traceWithReply(req.delivery.trace, current)
		}
	}
	projection := h.prepareReplyDelivery(req.delivery)
	if consumed, handled := h.finishProgressReplyWithOutbox(req, projection); handled {
		h.sendReplyProjection(req.delivery, replyDeliveryProjection{imageURLs: projection.imageURLs}, true)
		return consumed
	}
	consumed := finishProgressWithReplyForPlatform(
		req.delivery.replyWriter, req.finish, projection.text, req.failed,
	)
	h.sendReplyProjection(req.delivery, projection, consumed)
	return consumed
}

func (h *Handler) finishProgressReplyWithOutbox(req progressReplyDelivery, projection replyDeliveryProjection) (bool, bool) {
	outbox := h.currentTerminalOutbox()
	reporter, routeOK := req.delivery.replyWriter.(platform.DeliveryRouteReporter)
	if outbox == nil || !routeOK || !reporter.DeliveryRoute().Valid() {
		return false, false
	}
	if req.progress != nil && !req.progress.canPrepareDurableTerminal() {
		return false, false
	}
	if strings.TrimSpace(projection.text) == "" && !req.progress.hasDurableTerminalStream() {
		return false, false
	}
	recoveryDraft := terminalOutboxDraft{
		Route: reporter.DeliveryRoute(), AgentName: req.delivery.agentName, Failed: req.failed,
		Text: terminalRecoveryText(req.progress, projection.text, req.failed), Trace: req.delivery.trace,
	}
	reservation, err := outbox.reserve(recoveryDraft)
	if err != nil {
		h.recordTraceStage(req.delivery.trace, "terminal.outbox", "failed", err.Error())
		log.Printf("[terminal-outbox] failed to persist terminal recovery draft; using legacy terminal path: %v", err)
		return false, false
	}
	prepared, err := req.progress.prepareDurableTerminal(req.delivery.replyWriter, projection.text, req.failed)
	if err != nil {
		log.Printf("[terminal-outbox] failed to prepare stream checkpoint; falling back to durable text: %v", err)
		prepared = preparedProgressTerminal{}
	}
	if prepared.reply != nil {
		req.delivery.replyWriter = prepared.reply
		req.delivery.trace = traceWithReply(req.delivery.trace, prepared.reply)
		if latestReporter, ok := prepared.reply.(platform.DeliveryRouteReporter); ok && latestReporter.DeliveryRoute().Valid() {
			reporter = latestReporter
		}
	}
	if !req.delivery.replyWriter.Capabilities().StreamCompletionNotification {
		prepared.notification = ""
	}
	checkpoint := prepared.checkpoint
	if checkpoint != nil && checkpoint.Kind == "" {
		checkpoint = nil
	}
	text := ""
	if projection.text != progressStatusOnlyComplete &&
		(!prepared.consumed || checkpoint == nil && strings.TrimSpace(projection.text) != "") {
		text = projection.text
	}
	draft := terminalOutboxDraft{
		Route: reporter.DeliveryRoute(), AgentName: req.delivery.agentName, Failed: req.failed,
		Checkpoint: checkpoint, Text: text, Notification: prepared.notification, Trace: req.delivery.trace,
	}
	if checkpoint == nil && strings.TrimSpace(text) == "" && strings.TrimSpace(draft.Notification) == "" {
		outbox.releaseReservation(reservation.ID)
		return false, true
	}
	ctx := context.WithoutCancel(req.delivery.ctx)
	if err := outbox.commitReservation(reservation.ID, draft); err != nil {
		h.recordTraceStage(req.delivery.trace, "terminal.outbox", "failed", err.Error())
		log.Printf("[terminal-outbox] failed to persist prepared terminal delivery; recovery text remains queued: %v", err)
		if attemptErr := outbox.attempt(ctx, reservation.ID, req.delivery.replyWriter); attemptErr != nil {
			log.Printf("[terminal-outbox] recovery text remains pending id=%s: %v", reservation.ID, attemptErr)
		}
		outbox.signal()
		return false, true
	}
	h.recordTraceStage(req.delivery.trace, "terminal.outbox", "persisted", "terminal delivery persisted")
	if err := outbox.attempt(ctx, reservation.ID, req.delivery.replyWriter); err != nil {
		log.Printf("[terminal-outbox] delivery pending id=%s platform=%s account=%s: %v",
			reservation.ID, draft.Route.Platform, draft.Route.AccountID, err)
	}
	outbox.signal()
	return prepared.consumed && checkpoint != nil, true
}

func terminalRecoveryText(progress *progressSession, finalText string, failed bool) string {
	if finalText != progressStatusOnlyComplete && strings.TrimSpace(finalText) != "" {
		return finalText
	}
	if progress != nil && progress.ctx != nil && progress.ctx.Err() != nil {
		return "任务已停止。"
	}
	if failed {
		return "任务执行失败。"
	}
	return progressDefaultCompletion
}

func finishProgressWithReply(finish func(string, bool) bool, reply string, failed bool) bool {
	if !canConsumeFinalReplyInStream(reply) {
		_ = finish("", failed)
		return false
	}
	return finish(reply, failed)
}

func finishProgressWithReplyForPlatform(replyWriter platform.Replier, finish func(string, bool) bool, reply string, failed bool) bool {
	if shouldKeepFinalReplyOutsideStream(replyWriter, reply) {
		if failed {
			_ = finish(reply, true)
			return false
		}
		_ = finish(progressStatusOnlyComplete, failed)
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
