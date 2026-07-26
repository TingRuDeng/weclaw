package agent

import (
	"errors"
	"strings"
)

type codexStateRuntimeFailureKind string

const (
	codexStateRuntimeFailureUnknown      codexStateRuntimeFailureKind = "unknown"
	codexStateRuntimeFailureContention   codexStateRuntimeFailureKind = "contention"
	codexStateRuntimeFailureIncompatible codexStateRuntimeFailureKind = "incompatible"
	codexStateRuntimeFailureCorrupt      codexStateRuntimeFailureKind = "corrupt"
)

type codexStateRuntimeFailure struct {
	kind codexStateRuntimeFailureKind
	err  error
}

func (e *codexStateRuntimeFailure) Error() string {
	if e == nil || e.err == nil {
		return "Codex state runtime failure"
	}
	return e.err.Error()
}

func (e *codexStateRuntimeFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newCodexStateRuntimeFailure(kind codexStateRuntimeFailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &codexStateRuntimeFailure{kind: kind, err: err}
}

// classifyCodexStateRuntimeFailure keeps state-db contention, corruption and
// version incompatibility separate. The generic upstream wrapper is
// intentionally "unknown": the same text is emitted for a live Desktop lock,
// an I/O failure and a schema problem, so it must never authorize an update.
func classifyCodexStateRuntimeFailure(err error) codexStateRuntimeFailureKind {
	if err == nil {
		return ""
	}
	var typed *codexStateRuntimeFailure
	if errors.As(err, &typed) && typed.kind != "" {
		return typed.kind
	}
	text := strings.ToLower(err.Error())
	if !strings.Contains(text, "failed to initialize sqlite state runtime") &&
		!strings.Contains(text, "failed to initialize state runtime") {
		return ""
	}
	switch {
	case containsAnyCodexRuntimeMarker(text,
		"database is locked",
		"database table is locked",
		"resource temporarily unavailable",
		"another app-server",
		"another app server",
		"lock held by",
	):
		return codexStateRuntimeFailureContention
	case containsAnyCodexRuntimeMarker(text,
		"database disk image is malformed",
		"file is not a database",
		"database corruption",
		"database is corrupt",
	):
		return codexStateRuntimeFailureCorrupt
	case containsAnyCodexRuntimeMarker(text,
		"unsupported database version",
		"unsupported schema version",
		"database schema version is newer",
		"database version is newer",
		"no migration found for version",
		"requires a newer codex-cli",
	):
		return codexStateRuntimeFailureIncompatible
	default:
		return codexStateRuntimeFailureUnknown
	}
}

func containsAnyCodexRuntimeMarker(text string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
