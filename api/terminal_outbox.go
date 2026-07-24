package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/weclaw/messaging"
)

const maxTerminalOutboxRequestBytes = 4 * 1024

type terminalOutboxRedriveRequest struct {
	ID string `json:"id"`
}

func (s *Server) handleTerminalOutboxStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	if !s.authorizeLocalControl(w, r) || !s.requireTerminalOutbox(w) {
		return
	}
	status, err := s.outbox.TerminalOutboxStatus(r.Context())
	if err != nil {
		writeTerminalOutboxError(w, err)
		return
	}
	writeJSONResponse(w, status)
}

func (s *Server) handleTerminalOutboxRedrive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	if !s.authorizeLocalControl(w, r) || !s.requireTerminalOutbox(w) {
		return
	}
	var request terminalOutboxRedriveRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTerminalOutboxRequestBytes))
	if err := decoder.Decode(&request); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "terminal_outbox_invalid", "请求体过大")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "terminal_outbox_invalid", "请求 JSON 无效")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "terminal_outbox_invalid", "请求只能包含一个 JSON 值")
		return
	}
	result, err := s.outbox.RedriveTerminalOutbox(r.Context(), strings.TrimSpace(request.ID))
	if err != nil {
		writeTerminalOutboxError(w, err)
		return
	}
	writeJSONResponse(w, result)
}

func (s *Server) requireTerminalOutbox(w http.ResponseWriter) bool {
	if s.outbox != nil {
		return true
	}
	writeJSONError(w, http.StatusServiceUnavailable, "terminal_outbox_unavailable", "终态 outbox 未启用")
	return false
}

func writeTerminalOutboxError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, messaging.ErrTerminalOutboxNotFound):
		writeJSONError(w, http.StatusNotFound, "terminal_outbox_not_found", "未找到指定的终态投递项")
	case errors.Is(err, messaging.ErrTerminalOutboxUnavailable):
		writeJSONError(w, http.StatusServiceUnavailable, "terminal_outbox_unavailable", "终态 outbox 未启用")
	default:
		writeJSONError(w, http.StatusInternalServerError, "terminal_outbox_failed", "终态 outbox 操作失败")
	}
}
