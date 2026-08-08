package api

import (
	"net/http"

	"github.com/fastclaw-ai/weclaw/observability"
)

func (s *Server) handleCodexCLIPrepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	if !s.authorizeLocalControl(w, r) {
		return
	}
	if s.codexCLI == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "codex_cli_unavailable", "受控 Codex CLI 入口不可用")
		return
	}
	host, err := s.codexCLI.PrepareCodexCLIHost(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "codex_cli_host_unavailable", observability.SanitizeText(err.Error()))
		return
	}
	writeJSONResponse(w, map[string]any{"status": "ok", "host": host})
}
