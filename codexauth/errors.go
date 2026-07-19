package codexauth

import (
	"errors"
	"fmt"
)

const (
	CodeBusy                     = "codex_account_busy"
	CodeUnsupportedAuth          = "codex_account_unsupported_auth"
	CodeUnmanagedHost            = "codex_account_unmanaged_host"
	CodeTargetMismatch           = "codex_account_target_mismatch"
	CodeRuntimeUnavailable       = "codex_account_runtime_unavailable"
	CodeRollbackFailed           = "codex_account_rollback_failed"
	CodeFileStoreConsentRequired = "codex_account_file_store_consent_required"
	CodeNotFound                 = "codex_account_not_found"
	CodeConflict                 = "codex_account_conflict"
	CodeInvalid                  = "codex_account_invalid"
)

// Error 为账号管理提供稳定、可映射到 CLI/API/卡片的错误码。
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Cause }

func NewError(code, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func ErrorCode(err error) string {
	var accountErr *Error
	if errors.As(err, &accountErr) {
		return accountErr.Code
	}
	return ""
}

func wrapInvalid(action string, err error) error {
	return NewError(CodeInvalid, fmt.Sprintf("Codex 账号%s失败", action), err)
}
