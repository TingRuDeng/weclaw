package agent

import (
	"encoding/json"
	"log"
)

func (a *ACPAgent) handleSessionUpdate(params json.RawMessage) {
	a.handleSessionUpdateAt(params, a.wireSequence.Add(1))
}

func (a *ACPAgent) handleSessionUpdateAt(params json.RawMessage, sequence uint64) {
	var p sessionUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		log.Printf("[acp] failed to parse session/update: %v", err)
		return
	}
	if p.Update.SessionUpdate == "config_option_update" && a.isClaudeACP() {
		if err := a.cacheClaudeSessionConfigAt(p.SessionID, p.Update.ConfigOptions, sequence); err != nil {
			log.Printf("[acp] ignored invalid config_option_update (session=%s): %v", p.SessionID, err)
		}
	}

	// Only log non-streaming events (skip chunks to reduce noise)
	switch p.Update.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		// skip — too noisy
	default:
		log.Printf("[acp] session/update (session=%s, type=%s)", p.SessionID, p.Update.SessionUpdate)
	}

	a.notifyMu.Lock()
	ch, ok := a.notifyCh[p.SessionID]
	a.notifyMu.Unlock()

	if ok {
		select {
		case ch <- &p.Update:
		default:
			log.Printf("[acp] notification channel full, dropping update (session=%s)", p.SessionID)
		}
	}
}
