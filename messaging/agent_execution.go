package messaging

import (
	"context"
	"fmt"
	"log"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

// sendToDefaultAgent sends the message to the default agent and replies.
func (h *Handler) sendToDefaultAgent(ctx context.Context, platformName platform.PlatformName, userID string, routeUserID string, replyWriter platform.Replier, text string, clientID string) {
	h.sendToDefaultAgentForAccount(ctx, platformName, "", userID, routeUserID, replyWriter, text, clientID)
}

func (h *Handler) sendToDefaultAgentForAccount(ctx context.Context, platformName platform.PlatformName, accountID string, userID string, routeUserID string, replyWriter platform.Replier, text string, clientID string) {
	defaultName := h.defaultAgentNameForAccount(platformName, accountID)

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

		onProgress, finishProgress := h.startProgressSessionWithFinal(agentCtx, replyWriter, "", text, progressCfg)

		var err error
		conversationID, resolveErr := h.resolveAgentConversationIDForRoute(agentCtx, userID, routeUserID, defaultName, ag)
		if resolveErr != nil {
			reply = renderFinalFailure("", resolveErr)
			consumed := finishProgressWithReply(finishProgress, reply, true)
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
		consumed := finishProgressWithReply(finishProgress, reply, err != nil)
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

	onProgress, finishProgress := h.startProgressSessionWithFinal(agentCtx, replyWriter, "", message, progressCfg)

	conversationID, resolveErr := h.resolveAgentConversationIDForRoute(agentCtx, userID, routeUserID, name, ag)
	if resolveErr != nil {
		reply := renderFinalFailure("["+name+"] ", resolveErr)
		consumed := finishProgressWithReply(finishProgress, reply, true)
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
	consumed := finishProgressWithReply(finishProgress, reply, err != nil)
	h.sendReplyWithMediaAfterStreamForRoute(replyCtx, replyWriter, userID, routeUserID, name, reply, consumed)
}

// broadcastToAgents sends the message to multiple agents in parallel.
// Each reply is sent as a separate message with the agent name prefix.
func (h *Handler) broadcastToAgents(ctx context.Context, platformName platform.PlatformName, userID string, routeUserID string, replyWriter platform.Replier, names []string, message string) {
	type result struct {
		name          string
		reply         string
		skip          bool
		finalInStream bool
	}

	ch := make(chan result, len(names))

	for _, name := range names {
		go func(n string) {
			ag, err := h.getAgent(ctx, n)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err)}
				return
			}
			progressCfg := h.resolveProgressConfigForPlatform(platformName, n)
			agentCtx, cancelTaskTimeout := contextWithTaskTimeout(ctx, progressCfg)
			defer cancelTaskTimeout()
			agentCtx = agent.ContextWithApprovalHandler(agentCtx, h.approvalHandlerForUser(userID, routeUserID, replyWriter))

			var codexRoute codexConversationRoute
			var executionKey string
			var activeTask *activeAgentTask
			if isCodexAgent(n, ag.Info()) {
				codexRoute = h.codexConversationRouteForSession(userID, routeUserID, n, ag)
				executionKey = codexRoute.conversationID
				task, taskCtx, started := h.beginActiveTask(agentCtx, executionKey, activeTaskMeta{
					owner:     userID,
					agentName: n,
					message:   message,
				})
				if !started {
					h.storePendingGuide(executionKey, message)
					ch <- result{name: n, reply: runningCodexGuidePromptForTask(task)}
					return
				}
				activeTask = task
				agentCtx = taskCtx
				defer func() {
					pendingMessage, ok := h.promotePendingGuideToRun(executionKey, task)
					h.finishActiveTask(executionKey, task)
					if ok {
						sendPlatformText(ctx, replyWriter, userID, runnablePendingCodexPrompt(pendingMessage))
					}
				}()
			} else {
				executionKey = h.agentExecutionKeyForRoute(userID, routeUserID, n, ag)
			}
			unlock := h.lockAgentExecution(executionKey)
			defer unlock()

			onProgress, finishProgress := h.startProgressSessionWithFinal(agentCtx, replyWriter, "["+n+"] ", message, progressCfg)
			sendResult := func(reply string, failed bool) {
				consumed := finishProgressWithReply(finishProgress, reply, failed)
				ch <- result{name: n, reply: reply, finalInStream: consumed}
			}

			var conversationID string
			if isCodexAgent(n, ag.Info()) {
				if err := h.prepareCodexConversation(agentCtx, codexRoute, ag); err != nil {
					sendResult(renderFinalFailure("["+n+"] ", err), true)
					return
				}
				conversationID = codexRoute.conversationID
			} else {
				resolvedID, resolveErr := h.resolveAgentConversationIDForRoute(agentCtx, userID, routeUserID, n, ag)
				if resolveErr != nil {
					sendResult(renderFinalFailure("["+n+"] ", resolveErr), true)
					return
				}
				conversationID = resolvedID
			}
			reply, err := h.chatWithAgentWithProgress(agentCtx, ag, conversationID, message, onProgress)
			if err != nil {
				sendResult(renderFinalFailure("["+n+"] ", err), true)
				return
			}
			if isCodexAgent(n, ag.Info()) {
				h.recordCodexThreadForWorkspace(routeUserID, n, ag, conversationID, codexRoute.workspaceRoot)
			} else {
				h.recordCodexThread(routeUserID, n, ag, conversationID)
				h.recordClaudeSessionForRoute(userID, routeUserID, n, ag, conversationID)
			}
			if activeTask != nil && !activeTask.shouldSendFinal() {
				_ = finishProgress("", false)
				ch <- result{name: n, skip: true}
				return
			}
			sendResult(renderFinalSuccess("["+n+"] ", reply), false)
		}(name)
	}

	// Send replies as they arrive
	for range names {
		r := <-ch
		if r.skip {
			continue
		}
		if wxReply, ok := replyWriter.(*wechat.Replier); ok {
			wxReply.ClientID = NewClientID()
		}
		h.sendReplyWithMediaAfterStreamForRoute(ctx, replyWriter, userID, routeUserID, r.name, r.reply, r.finalInStream)
	}
}
