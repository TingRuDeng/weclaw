package api

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingResponseWriter struct {
	header http.Header
}

// Header 返回可写响应头。
func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

// Write 模拟客户端连接在 JSON 响应写入时断开。
func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("connection closed")
}

// WriteHeader 接受状态码，测试只关注响应体写入错误。
func (w *failingResponseWriter) WriteHeader(int) {}

// TestRuntimeStatusLogsJSONWriteFailure 验证 API 响应写入失败不会被静默忽略。
func TestRuntimeStatusLogsJSONWriteFailure(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011")
	req := httptest.NewRequest(http.MethodGet, "/api/runtime", nil)
	req.Host = "127.0.0.1:18011"
	w := &failingResponseWriter{header: make(http.Header)}
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldOutput)

	server.handleRuntimeStatus(w, req)

	if !strings.Contains(logs.String(), "JSON response") || !strings.Contains(logs.String(), "connection closed") {
		t.Fatalf("logs=%q，期望记录 JSON 响应写入错误", logs.String())
	}
}
