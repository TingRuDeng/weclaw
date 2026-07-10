package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func (s *Server) authorizeRead(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" {
		return true
	}
	if constantTimeEqual(sendAuthToken(r), s.token) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func sendAuthToken(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-WeClaw-Token")); token != "" {
		return token
	}
	fields := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return strings.TrimSpace(fields[1])
	}
	return ""
}

func constantTimeEqual(got string, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func isLoopbackListenAddr(addr string) bool {
	host := listenHost(addr)
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func listenHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(addr, "[]")
}
