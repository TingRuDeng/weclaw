package messaging

import (
	"context"
	"fmt"
	"log"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// sendToDefaultAgent sends the message to the default agent and replies.
func (h *Handler) sendToDefaultAgent(ctx context.Context, platformName platform.PlatformName, userID string, routeUserID string, replyWriter platform.Replier, text string, clientID string) {
	h.sendToDefaultAgentForAccount(ctx, platformName, "", userID, routeUserID, replyWriter, text, clientID)
}

func (h *Handler) sendToDefaultAgentForAccount(ctx context.Context, platformName platform.PlatformName, accountID string, userID string, routeUserID string, replyWriter platform.Replier, text string, clientID string) {
	defaultName := h.defaultAgentNameForRoute(routeUserID, platformName, accountID)

	var ag agent.Agent
	var agErr error
	if defaultName != "" {
		ag, agErr = h.getAgent(ctx, defaultName)
	}
	var reply string
	if defaultName != "" && agErr == nil {
		progressCfg := h.resolveProgressConfigForAccount(platformName, accountID, defaultName)
		if isCodexAgent(defaultName, ag.Info()) {
			h.startCodexAgentTask(codexAgentTaskOptions{
				ctx:         ctx,
				userID:      userID,
				routeUserID: routeUserID,
				reply:       replyWriter,
				agentName:   defaultName,
				message:     text,
				clientID:    clientID,
				replyPrefix: "",
				agent:       ag,
				progressCfg: progressCfg,
			})
			return
		}

		replyCtx := ctx
		agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
		defer cancelTaskTimeout()
		agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(userID, routeUserID, replyWriter))

		executionKey := h.agentExecutionKeyForRoute(userID, routeUserID, defaultName, ag)
		unlock := h.lockAgentExecution(executionKey)
		defer unlock()
		task, trackedCtx, trackErr := h.beginSynchronousActiveTask(agentCtx, executionKey, activeTaskMeta{
			owner: userID, agentName: defaultName, message: text,
		})
		if trackErr != nil {
			reply = renderFinalFailure("", trackErr)
			h.sendReplyWithMediaForRoute(replyCtx, replyWriter, userID, routeUserID, defaultName, reply)
			return
		}
		agentCtx = trackedCtx
		defer h.finishActiveTask(executionKey, task)

		onProgress, finishProgress := h.startProgressSessionWithFinal(agentCtx, replyWriter, "", text, progressCfg)

		var err error
		conversationID, resolveErr := h.resolveAgentConversationIDForRoute(agentCtx, userID, routeUserID, defaultName, ag)
		if resolveErr != nil {
			reply = renderFinalFailure("", resolveErr)
			consumed := finishProgressWithReplyForPlatform(replyWriter, finishProgress, reply, true)
			h.sendReplyWithMediaAfterStreamForRoute(replyCtx, replyWriter, userID, routeUserID, defaultName, reply, consumed)
			return
		}
		reply, err = h.chatWithAgentWithProgress(agentCtx, ag, conversationID, text, onProgress)
		if err != nil {
			reply = renderFinalFailure("", err)
		} else {
			h.recordCodexThread(routeUserID, defaultName, ag, conversationID)
			h.recordClaudeSessionForRoute(userID, routeUserID, defaultName, ag, conversationID)
			reply = renderFinalSuccess("", reply)
		}
		consumed := finishProgressWithReplyForPlatform(replyWriter, finishProgress, reply, err != nil)
		h.sendReplyWithMediaAfterStreamForRoute(replyCtx, replyWriter, userID, routeUserID, defaultName, reply, consumed)
		return
	} else {
		if agErr != nil && defaultName != "" {
			log.Printf("[handler] default agent %q not available, using echo mode for %s: %v", defaultName, userID, agErr)
		}
		log.Printf("[handler] agent not ready, using echo mode for %s", userID)
		reply = "[echo] " + text
	}

	h.sendReplyWithMediaForRoute(ctx, replyWriter, userID, routeUserID, defaultName, reply)
}

// sendToNamedAgent sends the message to a specific agent and replies.
func (h *Handler) sendToNamedAgent(ctx context.Context, platformName platform.PlatformName, userID string, routeUserID string, replyWriter platform.Replier, name string, message string, clientID string) {
	h.sendToNamedAgentForAccount(ctx, platformName, "", userID, routeUserID, replyWriter, name, message, clientID)
}

func (h *Handler) sendToNamedAgentForAccount(ctx context.Context, platformName platform.PlatformName, accountID string, userID string, routeUserID string, replyWriter platform.Replier, name string, message string, clientID string) {
	ag, agErr := h.getAgent(ctx, name)
	if agErr != nil {
		log.Printf("[handler] agent %q not available: %v", name, agErr)
		reply := fmt.Sprintf("Agent %q is not available: %v", name, agErr)
		sendPlatformText(ctx, replyWriter, userID, reply)
		return
	}

	replyCtx := ctx
	progressCfg := h.resolveProgressConfigForAccount(platformName, accountID, name)
	if isCodexAgent(name, ag.Info()) {
		h.startCodexAgentTask(codexAgentTaskOptions{
			ctx:         ctx,
			userID:      userID,
			routeUserID: routeUserID,
			reply:       replyWriter,
			agentName:   name,
			message:     message,
			clientID:    clientID,
			replyPrefix: "[" + name + "] ",
			agent:       ag,
			progressCfg: progressCfg,
		})
		return
	}

	agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
	defer cancelTaskTimeout()
	agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(userID, routeUserID, replyWriter))

	executionKey := h.agentExecutionKeyForRoute(userID, routeUserID, name, ag)
	unlock := h.lockAgentExecution(executionKey)
	defer unlock()
	task, trackedCtx, trackErr := h.beginSynchronousActiveTask(agentCtx, executionKey, activeTaskMeta{
		owner: userID, agentName: name, message: message,
	})
	if trackErr != nil {
		reply := renderFinalFailure("["+name+"] ", trackErr)
		h.sendReplyWithMediaForRoute(replyCtx, replyWriter, userID, routeUserID, name, reply)
		return
	}
	agentCtx = trackedCtx
	defer h.finishActiveTask(executionKey, task)

	onProgress, finishProgress := h.startProgressSessionWithFinal(agentCtx, replyWriter, "", message, progressCfg)

	conversationID, resolveErr := h.resolveAgentConversationIDForRoute(agentCtx, userID, routeUserID, name, ag)
	if resolveErr != nil {
		reply := renderFinalFailure("["+name+"] ", resolveErr)
		consumed := finishProgressWithReplyForPlatform(replyWriter, finishProgress, reply, true)
		h.sendReplyWithMediaAfterStreamForRoute(replyCtx, replyWriter, userID, routeUserID, name, reply, consumed)
		return
	}
	reply, err := h.chatWithAgentWithProgress(agentCtx, ag, conversationID, message, onProgress)
	if err != nil {
		reply = renderFinalFailure("["+name+"] ", err)
	} else {
		h.recordCodexThread(routeUserID, name, ag, conversationID)
		h.recordClaudeSessionForRoute(userID, routeUserID, name, ag, conversationID)
		reply = renderFinalSuccess("["+name+"] ", reply)
	}
	consumed := finishProgressWithReplyForPlatform(replyWriter, finishProgress, reply, err != nil)
	h.sendReplyWithMediaAfterStreamForRoute(replyCtx, replyWriter, userID, routeUserID, name, reply, consumed)
}
