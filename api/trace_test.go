package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/weclaw/observability"
)

type traceQueryStub struct {
	query observability.Query
	page  observability.Page
}

func (stub *traceQueryStub) Query(_ context.Context, query observability.Query) (observability.Page, error) {
	stub.query = query
	return stub.page, nil
}

func (*traceQueryStub) Status() observability.Status {
	return observability.Status{Enabled: true, Writable: true}
}

func TestHandleTraceQueryRequiresActualLoopbackAndMapsFilters(t *testing.T) {
	stub := &traceQueryStub{page: observability.Page{Events: []observability.Event{{TraceID: "trace-1"}}}}
	server := NewServer(nil, "127.0.0.1:18011", WithToken("secret"), WithTraceQueryProvider(stub))
	req := httptest.NewRequest(http.MethodGet, "/api/traces?message_id=message-1&thread_id=thread-1&limit=12", nil)
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("X-WeClaw-Token", "secret")
	rec := httptest.NewRecorder()

	server.handleTraceQuery(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if stub.query.MessageID != "message-1" || stub.query.ThreadID != "thread-1" || stub.query.Limit != 12 {
		t.Fatalf("query=%#v", stub.query)
	}
	var page observability.Page
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Events) != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}

	remote := httptest.NewRequest(http.MethodGet, "/api/traces", nil)
	remote.Host = "127.0.0.1:18011"
	remote.RemoteAddr = "203.0.113.8:43210"
	remote.Header.Set("X-WeClaw-Token", "secret")
	remoteRec := httptest.NewRecorder()
	server.handleTraceQuery(remoteRec, remote)
	if remoteRec.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d, want forbidden", remoteRec.Code)
	}
}

func TestHandleTraceQueryRejectsInvalidLimit(t *testing.T) {
	server := NewServer(nil, "127.0.0.1:18011", WithTraceQueryProvider(&traceQueryStub{}))
	req := httptest.NewRequest(http.MethodGet, "/api/traces?limit=1001", nil)
	req.Host = "127.0.0.1:18011"
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()
	server.handleTraceQuery(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
