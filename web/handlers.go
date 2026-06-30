package web

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/fastclaw-ai/weclaw/feishu"
)

const maxConfigBodyBytes = 1 << 20 // 1 MiB

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) handleIndex(sub fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "index not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		view, err := s.cfg.view()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, view)
	case http.MethodPut:
		var view configView
		if err := decodeJSON(w, r, &view); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		restartRequired, err := s.cfg.apply(view)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "restart_required": restartRequired})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.buildStatus())
}

func (s *Server) handleFeishuCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := feishu.SaveCredentials(feishu.Credentials{AppID: body.AppID, AppSecret: body.AppSecret}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"}) // 绝不回显 secret
}

func (s *Server) handleValidateFeishu(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	creds := feishu.Credentials{AppID: body.AppID, AppSecret: body.AppSecret}
	if body.AppSecret == "" {
		// 未填 secret 时用已保存凭证校验
		if saved, err := feishu.LoadCredentials(); err == nil {
			creds = saved
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := feishu.ValidateCredentials(ctx, creds); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "凭证有效"})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigBodyBytes))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errMultipleJSON
	}
	return nil
}

var errMultipleJSON = jsonError("request body must contain a single JSON value")

type jsonError string

func (e jsonError) Error() string { return string(e) }
