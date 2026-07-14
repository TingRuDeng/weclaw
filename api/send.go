package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/wechat"
)

const maxSendRequestBytes = 1 * 1024 * 1024

// SendRequest 是 POST /api/send 的 JSON 请求体。
type SendRequest struct {
	Platform  string `json:"platform,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	To        string `json:"to"`
	Text      string `json:"text,omitempty"`
	MediaURL  string `json:"media_url,omitempty"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRead(w, r) {
		return
	}
	req, ok := decodeSendRequest(w, r)
	if !ok {
		return
	}
	reply, ok := s.replierFor(w, req)
	if !ok {
		return
	}
	if err := s.sendRequest(r.Context(), reply, req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]string{"status": "ok"})
}

func decodeSendRequest(w http.ResponseWriter, r *http.Request) (SendRequest, bool) {
	var req SendRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSendRequestBytes))
	if err := decoder.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return SendRequest{}, false
		}
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return SendRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid JSON: multiple JSON values", http.StatusBadRequest)
		return SendRequest{}, false
	}
	if req.To == "" {
		http.Error(w, `"to" is required`, http.StatusBadRequest)
		return SendRequest{}, false
	}
	if req.Text == "" && req.MediaURL == "" {
		http.Error(w, `"text" or "media_url" is required`, http.StatusBadRequest)
		return SendRequest{}, false
	}
	return req, true
}

func (s *Server) replierFor(w http.ResponseWriter, req SendRequest) (platform.Replier, bool) {
	if s.registry == nil {
		if len(s.clients) == 0 {
			http.Error(w, "no accounts configured", http.StatusServiceUnavailable)
			return nil, false
		}
		return wechat.NewReplier(s.clients[0], req.To, "", ""), true
	}
	platformName := platform.PlatformName(strings.TrimSpace(req.Platform))
	if platformName == "" {
		platformName = platform.PlatformWeChat
	}
	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" && s.registry.OutboundAccountCount(platformName) > 1 {
		http.Error(w, `"account_id" is required when target platform has multiple accounts`, http.StatusBadRequest)
		return nil, false
	}
	reply, ok := s.registry.ReplierFor(platformName, accountID, req.To)
	if !ok {
		http.Error(w, "target platform or account not configured", http.StatusServiceUnavailable)
		return nil, false
	}
	return reply, true
}

func (s *Server) sendRequest(ctx context.Context, reply platform.Replier, req SendRequest) error {
	if req.Text != "" {
		if err := reply.SendText(ctx, req.Text); err != nil {
			return fmt.Errorf("send text failed: %w", err)
		}
		log.Printf("[api] sent text to %s (runes=%d)", req.To, utf8.RuneCountInString(req.Text))
		s.sendExtractedImages(ctx, reply, req)
	}
	if req.MediaURL == "" {
		return nil
	}
	remoteReply, ok := reply.(interface {
		SendMediaFromURL(context.Context, string) error
	})
	if !ok {
		return fmt.Errorf("target platform does not support remote media URL sending")
	}
	if err := remoteReply.SendMediaFromURL(ctx, req.MediaURL); err != nil {
		return fmt.Errorf("send media failed: %w", err)
	}
	log.Printf("[api] sent media to %s", req.To)
	return nil
}

func (s *Server) sendExtractedImages(ctx context.Context, reply platform.Replier, req SendRequest) {
	remoteReply, ok := reply.(interface {
		SendMediaFromURL(context.Context, string) error
	})
	if !ok {
		return
	}
	for _, imageURL := range messaging.ExtractImageURLs(req.Text) {
		if err := remoteReply.SendMediaFromURL(ctx, imageURL); err != nil {
			log.Printf("[api] send extracted image failed: %v", err)
			continue
		}
		log.Printf("[api] sent extracted image to %s", req.To)
	}
}
