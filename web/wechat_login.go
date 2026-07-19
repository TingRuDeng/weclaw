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
	ctx       context.Context
	cancel    context.CancelFunc
	saving    bool
}

// wechatLoginStore 管理面板内微信扫码登录的临时会话(内存 + TTL)。
type wechatLoginStore struct {
	startMu  sync.Mutex
	mu       sync.Mutex
	sessions map[string]*wechatLoginSession
	activeID string
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
	ctx, cancel := context.WithTimeout(context.Background(), wechatLoginTTL)
	s.mu.Lock()
	s.cleanupLocked()
	if previous, ok := s.sessions[s.activeID]; ok {
		previous.status = "expired"
		previous.qrContent = ""
		if previous.cancel != nil {
			previous.cancel()
		}
	}
	s.activeID = id
	s.sessions[id] = &wechatLoginSession{
		status: "waiting", qrContent: qrContent, expiresAt: s.now().Add(wechatLoginTTL), ctx: ctx, cancel: cancel,
	}
	s.mu.Unlock()
	return id
}

func (s *wechatLoginStore) contextOf(id string) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok && sess.ctx != nil {
		return sess.ctx
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
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
	if sess, ok := s.sessions[id]; ok && s.activeID == id && !sess.saving && sess.status != "expired" && sess.status != "confirmed" {
		// confirmed 只有凭据落盘后才是终态；轮询接口先回调 confirmed、再返回
		// credentials，不能让该回调提前解除单飞保护。
		if status == "confirmed" {
			sess.status = "scanned"
		} else {
			sess.status = status
		}
	}
	s.mu.Unlock()
}

func (s *wechatLoginStore) expire(id string) {
	s.mu.Lock()
	if sess, ok := s.sessions[id]; ok {
		sess.status = "expired"
		sess.qrContent = ""
		if sess.cancel != nil {
			sess.cancel()
		}
	}
	if s.activeID == id {
		s.activeID = ""
	}
	s.mu.Unlock()
}

func (s *wechatLoginStore) complete(id string, creds *ilink.Credentials) error {
	// 与 start 共用 startMu，保证保存期间不会有新登录把 activeID 换走；
	// 磁盘 IO 不持有状态锁，因此 status/QR 查询仍可及时返回。
	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok || s.activeID != id || sess.status == "expired" || sess.saving {
		s.mu.Unlock()
		return context.Canceled
	}
	sess.saving = true
	save := s.save
	s.mu.Unlock()

	err := save(creds)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok = s.sessions[id]
	if !ok || s.activeID != id || !sess.saving {
		return context.Canceled
	}
	sess.saving = false
	if err != nil {
		sess.status = "expired"
		sess.qrContent = ""
		if sess.cancel != nil {
			sess.cancel()
		}
		s.activeID = ""
		return err
	}
	sess.status = "confirmed"
	sess.qrContent = ""
	if sess.cancel != nil {
		sess.cancel()
	}
	s.activeID = ""
	return nil
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
		// complete 已在 startMu 下确认该会话仍是唯一活动会话；保存期间
		// 不再由状态查询触发 TTL 删除，否则会出现“凭据已写入、会话却
		// 显示过期”的分裂终态。保存完成后会立即进入 confirmed/expired。
		if sess.saving {
			continue
		}
		if now.After(sess.expiresAt) {
			if sess.cancel != nil {
				sess.cancel()
			}
			if s.activeID == id {
				s.activeID = ""
			}
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
	store.startMu.Lock()
	qr, err := store.fetchQR(r.Context())
	if err != nil {
		store.startMu.Unlock()
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	id := store.begin(qr.QRCodeImgContent)
	pollCtx := store.contextOf(id)
	store.startMu.Unlock()
	// 后台轮询确认；二维码内容不持久化、不入日志。
	go func() {
		creds, err := store.poll(pollCtx, qr.QRCode, func(status string) {
			store.setStatus(id, status)
		})
		if err != nil || creds == nil {
			store.expire(id)
			return
		}
		if err := store.complete(id, creds); err != nil {
			return
		}
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
