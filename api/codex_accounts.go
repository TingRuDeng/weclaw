package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
)

const maxCodexAccountRequestBytes = 64 * 1024

type codexAccountSaveRequest struct {
	Label          string `json:"label"`
	Replace        bool   `json:"replace"`
	AllowFileStore bool   `json:"allow_file_store"`
}

type codexAccountUseRequest struct {
	Profile          string `json:"profile"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

type codexAccountRemoveRequest struct {
	Profile string `json:"profile"`
}

func (s *Server) handleCodexAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	if !s.authorizeLocalControl(w, r) || !s.requireCodexAccounts(w) {
		return
	}
	status, err := s.accounts.ListCodexAccounts(r.Context())
	if err != nil {
		writeCodexAccountError(w, err)
		return
	}
	writeJSONResponse(w, status)
}

func (s *Server) handleCodexAccountCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	if !s.authorizeLocalControl(w, r) || !s.requireCodexAccounts(w) {
		return
	}
	withQuota := r.URL.Query().Get("quota") == "1" || strings.EqualFold(r.URL.Query().Get("quota"), "true")
	status, err := s.accounts.CurrentCodexAccount(r.Context(), withQuota)
	if err != nil {
		writeCodexAccountError(w, err)
		return
	}
	writeJSONResponse(w, status)
}

func (s *Server) handleCodexAccountSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	if !s.authorizeLocalControl(w, r) || !s.requireCodexAccounts(w) {
		return
	}
	var request codexAccountSaveRequest
	if !decodeCodexAccountRequest(w, r, &request) {
		return
	}
	profile, err := s.accounts.SaveCodexAccount(r.Context(), agent.CodexAccountSaveOptions{
		Label: request.Label, Replace: request.Replace, AllowFileStore: request.AllowFileStore,
	})
	if err != nil {
		writeCodexAccountError(w, err)
		return
	}
	writeJSONResponse(w, map[string]any{"status": "ok", "profile": profile})
}

func (s *Server) handleCodexAccountUse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	if !s.authorizeLocalControl(w, r) || !s.requireCodexAccounts(w) {
		return
	}
	var request codexAccountUseRequest
	if !decodeCodexAccountRequest(w, r, &request) {
		return
	}
	result, err := s.accounts.UseCodexAccount(r.Context(), request.Profile, request.ExpectedRevision)
	if err != nil {
		writeCodexAccountError(w, err)
		return
	}
	writeJSONResponse(w, map[string]any{"status": "ok", "result": result})
}

func (s *Server) handleCodexAccountRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	if !s.authorizeLocalControl(w, r) || !s.requireCodexAccounts(w) {
		return
	}
	var request codexAccountRemoveRequest
	if !decodeCodexAccountRequest(w, r, &request) {
		return
	}
	if err := s.accounts.RemoveCodexAccount(r.Context(), request.Profile); err != nil {
		writeCodexAccountError(w, err)
		return
	}
	writeJSONResponse(w, map[string]string{"status": "ok"})
}

func (s *Server) handleCodexAccountDoctor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	if !s.authorizeLocalControl(w, r) || !s.requireCodexAccounts(w) {
		return
	}
	result := s.accounts.DoctorCodexAccounts(r.Context())
	writeJSONResponse(w, map[string]any{
		"ok": result.OK, "message": result.Message, "host_id": result.HostID,
	})
}

func (s *Server) requireCodexAccounts(w http.ResponseWriter) bool {
	if s.accounts != nil {
		return true
	}
	writeJSONError(w, http.StatusServiceUnavailable, codexauth.CodeRuntimeUnavailable, "Codex 账号控制器不可用")
	return false
}

func decodeCodexAccountRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCodexAccountRequestBytes))
	if err := decoder.Decode(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, codexauth.CodeInvalid, "请求体过大")
			return false
		}
		writeJSONError(w, http.StatusBadRequest, codexauth.CodeInvalid, "请求 JSON 无效")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, codexauth.CodeInvalid, "请求只能包含一个 JSON 值")
		return false
	}
	return true
}

func writeCodexAccountError(w http.ResponseWriter, err error) {
	code := codexauth.ErrorCode(err)
	status := http.StatusInternalServerError
	switch code {
	case codexauth.CodeBusy, codexauth.CodeConflict:
		status = http.StatusConflict
	case codexauth.CodeNotFound:
		status = http.StatusNotFound
	case codexauth.CodeInvalid:
		status = http.StatusBadRequest
	case codexauth.CodeUnsupportedAuth, codexauth.CodeTargetMismatch:
		status = http.StatusUnprocessableEntity
	case codexauth.CodeFileStoreConsentRequired:
		status = http.StatusPreconditionRequired
	case codexauth.CodeUnmanagedHost, codexauth.CodeRuntimeUnavailable, codexauth.CodeCleanupPending:
		status = http.StatusServiceUnavailable
	case codexauth.CodeRollbackFailed:
		status = http.StatusInternalServerError
	default:
		code = codexauth.CodeRuntimeUnavailable
	}
	message := "Codex 账号操作失败"
	var accountErr *codexauth.Error
	if errors.As(err, &accountErr) && strings.TrimSpace(accountErr.Message) != "" {
		message = accountErr.Message
	}
	writeJSONError(w, status, code, message)
}
