package wechat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
)

type recordedCalls struct {
	messages []ilink.SendMessageRequest
	typing   []ilink.SendTypingRequest
}

func newRecordingClient(t *testing.T) (*ilink.Client, *recordedCalls, func()) {
	t.Helper()
	calls := &recordedCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getconfig":
			_ = json.NewEncoder(w).Encode(ilink.GetConfigResponse{Ret: 0, TypingTicket: "ticket-1"})
		case "/ilink/bot/sendtyping":
			var req ilink.SendTypingRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode typing request: %v", err)
			}
			calls.typing = append(calls.typing, req)
			_ = json.NewEncoder(w).Encode(ilink.SendTypingResponse{Ret: 0})
		case "/ilink/bot/sendmessage":
			var req ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode message request: %v", err)
			}
			calls.messages = append(calls.messages, req)
			_ = json.NewEncoder(w).Encode(ilink.SendMessageResponse{Ret: 0})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	client := ilink.NewClient(&ilink.Credentials{
		BotToken:   "token-1",
		ILinkBotID: "bot-1",
		BaseURL:    server.URL,
	})
	return client, calls, server.Close
}

func TestReadCDNBodyRejectsBodyAboveLimit(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader("123456789")),
		ContentLength: -1,
	}

	_, err := readCDNBody(resp, 8)
	if err == nil {
		t.Fatal("readCDNBody() error = nil, want body size rejection")
	}
}

func (c *recordedCalls) texts() []string {
	texts := make([]string, 0, len(c.messages))
	for _, msg := range c.messages {
		for _, item := range msg.Msg.ItemList {
			if item.TextItem != nil {
				texts = append(texts, item.TextItem.Text)
			}
		}
	}
	return texts
}

func (c *recordedCalls) clientIDs() []string {
	ids := make([]string, 0, len(c.messages))
	for _, msg := range c.messages {
		ids = append(ids, msg.Msg.ClientID)
	}
	return ids
}

func TestReplierSendTextFormatsForWeChat(t *testing.T) {
	client, calls, closeServer := newRecordingClient(t)
	defer closeServer()
	reply := NewReplier(client, "user-1", "ctx-1", "client-1")

	if err := reply.SendText(context.Background(), "# 标题\n正文 `code`"); err != nil {
		t.Fatalf("SendText error: %v", err)
	}

	texts := calls.texts()
	if len(texts) != 1 || texts[0] != "标题\n\n正文 code" {
		t.Fatalf("texts=%#v, want formatted plain text", texts)
	}
}

func TestReplierSendTextUsesUniqueClientIDPerSend(t *testing.T) {
	client, calls, closeServer := newRecordingClient(t)
	defer closeServer()
	reply := NewReplier(client, "user-1", "ctx-1", "client-1")

	if err := reply.SendText(context.Background(), "第一条"); err != nil {
		t.Fatalf("first SendText error: %v", err)
	}
	if err := reply.SendText(context.Background(), "第二条"); err != nil {
		t.Fatalf("second SendText error: %v", err)
	}

	ids := calls.clientIDs()
	if len(ids) != 2 {
		t.Fatalf("client ids=%#v, want two sends", ids)
	}
	if ids[0] != "client-1" {
		t.Fatalf("first client id=%q, want incoming client id", ids[0])
	}
	if ids[1] == "" || ids[1] == ids[0] {
		t.Fatalf("client ids=%#v, want unique id for second SendText call", ids)
	}
}

func TestReplierSendTextIdempotentUsesStableClientIDsPerChunk(t *testing.T) {
	client, calls, closeServer := newRecordingClient(t)
	defer closeServer()
	reply := NewReplier(client, "user-1", "ctx-1", "")
	reply.ChunkRunes = 5
	for attempt := 0; attempt < 2; attempt++ {
		if err := reply.SendTextIdempotent(context.Background(), "甲乙丙丁戊己", "delivery-1:text"); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	ids := calls.clientIDs()
	if len(ids) != 4 || ids[0] == "" || ids[1] == "" || ids[0] == ids[1] || ids[0] != ids[2] || ids[1] != ids[3] {
		t.Fatalf("client ids=%#v, want stable per-chunk retry IDs", ids)
	}
}

func TestReplierSendTextChunksLongText(t *testing.T) {
	client, calls, closeServer := newRecordingClient(t)
	defer closeServer()
	reply := NewReplier(client, "user-1", "ctx-1", "client-1")
	reply.ChunkRunes = 15
	text := strings.Join([]string{
		strings.Repeat("甲", 12),
		strings.Repeat("乙", 12),
		strings.Repeat("丙", 12),
	}, "\n")

	if err := reply.SendText(context.Background(), text); err != nil {
		t.Fatalf("SendText error: %v", err)
	}

	texts := calls.texts()
	if len(texts) != 3 {
		t.Fatalf("texts=%#v, want three chunks", texts)
	}
	for _, text := range texts {
		if len([]rune(text)) > 15 {
			t.Fatalf("chunk too long: %q", text)
		}
	}
}

func TestReplierTyping(t *testing.T) {
	client, calls, closeServer := newRecordingClient(t)
	defer closeServer()
	reply := NewReplier(client, "user-1", "ctx-1", "client-1")

	if err := reply.Typing(context.Background(), true); err != nil {
		t.Fatalf("Typing true error: %v", err)
	}
	if err := reply.Typing(context.Background(), false); err != nil {
		t.Fatalf("Typing false error: %v", err)
	}

	if len(calls.typing) != 2 || calls.typing[0].Status != ilink.TypingStatusTyping || calls.typing[1].Status != ilink.TypingStatusCancel {
		t.Fatalf("typing calls=%#v", calls.typing)
	}
}

func TestReplierSendImageUploadsAndSendsImageItem(t *testing.T) {
	client, calls, closeServer := newRecordingClient(t)
	defer closeServer()
	imagePath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(imagePath, []byte("png-data"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	originalUpload := uploadFileToCDN
	t.Cleanup(func() { uploadFileToCDN = originalUpload })
	uploadFileToCDN = func(ctx context.Context, client *ilink.Client, data []byte, toUserID string, mediaType int) (*UploadedFile, error) {
		if string(data) != "png-data" || toUserID != "user-1" || mediaType != ilink.CDNMediaTypeImage {
			t.Fatalf("unexpected upload args data=%q to=%q type=%d", string(data), toUserID, mediaType)
		}
		return &UploadedFile{
			DownloadParam: "download-param",
			AESKeyHex:     "00112233445566778899aabbccddeeff",
			FileSize:      len(data),
			CipherSize:    32,
		}, nil
	}
	reply := NewReplier(client, "user-1", "ctx-1", "client-1")

	if err := reply.SendImage(context.Background(), imagePath); err != nil {
		t.Fatalf("SendImage error: %v", err)
	}

	if len(calls.messages) != 1 {
		t.Fatalf("messages=%#v, want one image message", calls.messages)
	}
	item := calls.messages[0].Msg.ItemList[0]
	if item.Type != ilink.ItemTypeImage || item.ImageItem == nil || item.ImageItem.Media == nil {
		t.Fatalf("sent item=%#v, want image item", item)
	}
	if item.ImageItem.Media.EncryptQueryParam != "download-param" || item.ImageItem.MidSize != 32 {
		t.Fatalf("image item=%#v", item.ImageItem)
	}
}
