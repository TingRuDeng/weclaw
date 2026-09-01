package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
)

const codexInputDeliveryConfirmTimeout = 5 * time.Second

type codexInputDeliveryKind uint8

const (
	codexInputDeliveryStart codexInputDeliveryKind = iota + 1
	codexInputDeliverySteer
)

type codexInputDeliveryBaseline struct {
	kind              codexInputDeliveryKind
	threadID          string
	lastTurnID        string
	expectedTurnID    string
	messageDigest     string
	matchingUserItems int
	previewDigest     string
}

func newCodexStartDeliveryBaseline(req CodexTurnRequest, state CodexThreadState) codexInputDeliveryBaseline {
	return codexInputDeliveryBaseline{
		kind: codexInputDeliveryStart, threadID: strings.TrimSpace(req.Runtime.Ref.ThreadID),
		lastTurnID: strings.TrimSpace(state.LastTurnID), messageDigest: codexInputMessageDigest(req.Message),
		previewDigest: codexInputMessageDigest(state.Preview),
	}
}

func (a *ACPAgent) captureCodexSteerDeliveryBaseline(
	ctx context.Context,
	req CodexTurnRequest,
	state CodexThreadState,
) (codexInputDeliveryBaseline, error) {
	baseline := codexInputDeliveryBaseline{
		kind: codexInputDeliverySteer, threadID: strings.TrimSpace(req.Runtime.Ref.ThreadID),
		lastTurnID: strings.TrimSpace(state.LastTurnID), expectedTurnID: strings.TrimSpace(state.ActiveTurnID),
		messageDigest: codexInputMessageDigest(req.Message), previewDigest: codexInputMessageDigest(state.Preview),
	}
	if binding, ok := a.runtimeBindingForThread(req.Runtime.Ref.ConversationID, req.Runtime.Ref.ThreadID); ok &&
		binding.Runtime == CodexRuntimeDesktop {
		return baseline, nil
	}
	_, snapshot, _, _, err := a.readCodexAppServerThreadSnapshotResult(
		ctx, req.Runtime.Ref.ThreadID, baseline.expectedTurnID,
	)
	if err != nil {
		return codexInputDeliveryBaseline{}, err
	}
	baseline.matchingUserItems = countCodexInputDigest(snapshot, baseline.expectedTurnID, baseline.messageDigest)
	return baseline, nil
}

func (a *ACPAgent) confirmCodexInputDelivery(
	ctx context.Context,
	req CodexTurnRequest,
	baseline codexInputDeliveryBaseline,
) (string, error) {
	confirmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexInputDeliveryConfirmTimeout)
	defer cancel()
	if binding, ok := a.runtimeBindingForThread(req.Runtime.Ref.ConversationID, req.Runtime.Ref.ThreadID); ok &&
		binding.Runtime == CodexRuntimeDesktop {
		state, err := a.ReadCodexThreadState(confirmCtx, req.Runtime.Ref.ConversationID, req.Runtime.Ref.ThreadID)
		if err != nil {
			return "", unconfirmedCodexInputDelivery(err)
		}
		turnID := firstNonEmpty(strings.TrimSpace(state.ActiveTurnID), strings.TrimSpace(state.LastTurnID))
		previewDigest := codexInputMessageDigest(state.Preview)
		accepted := previewDigest != "" && previewDigest == baseline.messageDigest && previewDigest != baseline.previewDigest
		if baseline.kind == codexInputDeliveryStart {
			accepted = accepted && turnID != "" && turnID != baseline.lastTurnID
		} else {
			accepted = accepted && turnID == baseline.expectedTurnID
		}
		if accepted {
			return turnID, nil
		}
		return "", unconfirmedCodexInputDelivery(nil)
	}
	targetTurnID := ""
	if baseline.kind == codexInputDeliverySteer {
		targetTurnID = baseline.expectedTurnID
	}
	state, snapshot, _, _, err := a.readCodexAppServerThreadSnapshotResult(
		confirmCtx, req.Runtime.Ref.ThreadID, targetTurnID,
	)
	if err != nil {
		return "", unconfirmedCodexInputDelivery(err)
	}
	turnID := firstNonEmpty(strings.TrimSpace(state.ActiveTurnID), strings.TrimSpace(state.LastTurnID))
	matching := countCodexInputDigest(snapshot, turnID, baseline.messageDigest)
	accepted := false
	if baseline.kind == codexInputDeliveryStart {
		accepted = turnID != "" && turnID != baseline.lastTurnID && matching > 0
	} else {
		accepted = turnID == baseline.expectedTurnID && matching > baseline.matchingUserItems
	}
	if !accepted {
		return "", unconfirmedCodexInputDelivery(nil)
	}
	return turnID, nil
}

func countCodexInputDigest(snapshot codexThreadSnapshot, turnID string, digest string) int {
	turnID = strings.TrimSpace(turnID)
	count := 0
	for _, turn := range snapshot.Turns {
		if turnID != "" && strings.TrimSpace(turn.ID) != turnID {
			continue
		}
		for _, item := range turn.Items {
			if strings.EqualFold(strings.TrimSpace(item.Type), "userMessage") &&
				codexInputMessageDigest(codexItemText(item)) == digest {
				count++
			}
		}
	}
	return count
}

func codexInputMessageDigest(message string) string {
	message = strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n"))
	if message == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(message))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func unconfirmedCodexInputDelivery(cause error) error {
	message := "请先在 Codex App 核对该会话；若未出现这条输入，再手工重发"
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrCodexInputDeliveryUnconfirmed, message)
	}
	return fmt.Errorf("%w: %s: %v", ErrCodexInputDeliveryUnconfirmed, message, cause)
}

func isCodexInputDeliveryUnknown(err error) bool {
	return errors.Is(err, ErrCodexInputDeliveryUnknown) || errors.Is(err, ErrCodexDesktopDeliveryUnknown)
}

func codexInputAttemptFromBaseline(
	req CodexTurnRequest,
	baseline codexInputDeliveryBaseline,
	status CodexInputAttemptStatus,
) CodexInputAttempt {
	return CodexInputAttempt{
		AttemptID: strings.TrimSpace(req.AttemptID), MessageKey: strings.TrimSpace(req.MessageKey),
		ThreadID: baseline.threadID, BaselineTurnID: baseline.lastTurnID,
		ExpectedTurnID: baseline.expectedTurnID, MessageDigest: baseline.messageDigest, Status: status,
	}
}

func notifyCodexInputAttempt(
	req CodexTurnRequest,
	baseline codexInputDeliveryBaseline,
	status CodexInputAttemptStatus,
) error {
	if req.OnInputAttempt == nil {
		return nil
	}
	return req.OnInputAttempt(codexInputAttemptFromBaseline(req, baseline, status))
}
