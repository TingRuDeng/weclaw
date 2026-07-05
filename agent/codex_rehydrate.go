package agent

import "strings"

func (a *ACPAgent) recordConversationExchange(conversationID, userText, assistantText string) {
	if conversationID == "" {
		return
	}

	userText = strings.TrimSpace(userText)
	assistantText = strings.TrimSpace(assistantText)
	if userText == "" || assistantText == "" {
		return
	}

	a.mu.Lock()
	h := append(a.history[conversationID],
		acpHistoryMessage{Role: "user", Text: userText},
		acpHistoryMessage{Role: "assistant", Text: assistantText},
	)
	if len(h) > acpMaxHistoryMessages {
		h = h[len(h)-acpMaxHistoryMessages:]
	}
	a.history[conversationID] = h
	a.mu.Unlock()
	a.persistState()
}

func (a *ACPAgent) buildRehydratePrompt(conversationID, currentMessage string) (string, bool) {
	a.mu.Lock()
	history := append([]acpHistoryMessage(nil), a.history[conversationID]...)
	a.mu.Unlock()

	if len(history) == 0 {
		return "", false
	}

	render := func(from int) string {
		var b strings.Builder
		b.WriteString("Context from the previous conversation (auto-restored after thread/account switch):\n")
		for _, msg := range history[from:] {
			role := "User"
			if msg.Role == "assistant" {
				role = "Assistant"
			}
			b.WriteString(role)
			b.WriteString(": ")
			b.WriteString(msg.Text)
			b.WriteString("\n")
		}
		b.WriteString("\nCurrent user message:\n")
		b.WriteString(currentMessage)
		b.WriteString("\n\nPlease continue the conversation using the restored context.")
		return b.String()
	}

	start := 0
	prompt := render(start)
	for len(prompt) > acpMaxRehydratePromptChars && start < len(history)-1 {
		start++
		prompt = render(start)
	}
	return prompt, true
}

func isMissingThreadError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	hasEntity := strings.Contains(msg, "thread") || strings.Contains(msg, "conversation") || strings.Contains(msg, "session")
	hasMissing := strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no conversation found") ||
		strings.Contains(msg, "unknown thread") ||
		strings.Contains(msg, "unknown session")
	return hasEntity && hasMissing
}
