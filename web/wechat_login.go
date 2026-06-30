package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	qr "rsc.io/qr"
)

const wechatLoginTTL = 5 * time.Minute

type wechatLoginSession struct {
	status    string
	qrContent string
	expiresAt time.Time
}

// wechatLoginStore 管理面板内微信扫码登录的临时会话(内存 + TTL)。
type wechatLoginStore struct {
	mu       sync.Mutex
	sessions map[string]*wechatLoginSession
	now      func() time.Time
	// 测试注入点
	fetchQR func(ctx context.Context) (*ilink.QRCodeResponse, error)
	poll    func(ctx context.Context, qrCode string, onStatus func(string)) (*ilink.Credentials, error)
	save    func(*ilink.Credentials) error
}

func newWechatLoginStore() *wechatLoginStore {
	return &wechatLoginStore{
		sessions: make(map[string]*wechatLoginSession),
		now:      time.Now,
		fetchQR:  ilink.FetchQRCode,
		poll:     ilink.PollQRStatus,
		save:     ilink.SaveCredentials,
	}
}

func randomLoginID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// begin 登记一个等待中的登录会话。
func (s *wechatLoginStore) begin(qrContent string) string {
	id := randomLoginID()
	s.mu.Lock()
	s.cleanupLocked()
	s.sessions[id] = &wechatLoginSession{status: "waiting", qrContent: qrContent, expiresAt: s.now().Add(wechatLoginTTL)}
	s.mu.Unlock()
	return id
}

// qrContentOf 返回会话二维码内容；非法/过期返回空。
func (s *wechatLoginStore) qrContentOf(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	if sess, ok := s.sessions[id]; ok {
		return sess.qrContent
	}
	return ""
}

func (s *wechatLoginStore) setStatus(id, status string) {
	s.mu.Lock()
	if sess, ok := s.sessions[id]; ok {
		sess.status = status
	}
	s.mu.Unlock()
}

// status 返回会话状态；非法或过期返回 "expired"，不泄露其它信息。
func (s *wechatLoginStore) statusOf(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	sess, ok := s.sessions[id]
	if !ok {
		return "expired"
	}
	return sess.status
}

func (s *wechatLoginStore) cleanupLocked() {
	now := s.now()
	for id, sess := range s.sessions {
		if now.After(sess.expiresAt) {
			delete(s.sessions, id)
		}
	}
}

func (s *Server) handleWeChatLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	store := s.wechatLogins
	qr, err := store.fetchQR(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	id := store.begin(qr.QRCodeImgContent)
	// 后台轮询确认；二维码内容不持久化、不入日志。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), wechatLoginTTL)
		defer cancel()
		creds, err := store.poll(ctx, qr.QRCode, func(status string) {
			store.setStatus(id, status)
		})
		if err != nil || creds == nil {
			store.setStatus(id, "expired")
			return
		}
		if err := store.save(creds); err != nil {
			store.setStatus(id, "expired")
			return
		}
		store.setStatus(id, "confirmed")
	}()
	writeJSON(w, http.StatusOK, map[string]string{"login_id": id})
}

// handleWeChatLoginQR 在本机渲染二维码 PNG，避免把登录二维码内容外发第三方。
func (s *Server) handleWeChatLoginQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	content := s.wechatLogins.qrContentOf(r.URL.Query().Get("login_id"))
	if content == "" {
		http.Error(w, "expired", http.StatusNotFound)
		return
	}
	code, err := qr.Encode(content, qr.M)
	if err != nil {
		http.Error(w, "qr encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(code.PNG())
}

func (s *Server) handleWeChatLoginStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("login_id")
	writeJSON(w, http.StatusOK, map[string]string{"status": s.wechatLogins.statusOf(id)})
}
