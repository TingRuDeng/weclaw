package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

func (s *Server) authorizeRead(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" {
		if isTrustedLoopbackRequest(r) {
			return true
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if constantTimeEqual(sendAuthToken(r), s.token) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

// isTrustedLoopbackRequest 拒绝 DNS rebinding Host，并限制浏览器请求必须同源。
func isTrustedLoopbackRequest(r *http.Request) bool {
	host, port, ok := parseRequestAuthority(r.Host)
	if !ok || !isLoopbackHost(host) {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return false
	}
	originHost, originPort, ok := parseRequestAuthority(parsed.Host)
	if !ok || originHost != host || originPort != port {
		return false
	}
	wantScheme := "http"
	if r.TLS != nil {
		wantScheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, wantScheme)
}

func parseRequestAuthority(authority string) (string, string, bool) {
	parsed, err := url.Parse("http://" + strings.TrimSpace(authority))
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" {
		return "", "", false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return "", "", false
	}
	return host, parsed.Port(), true
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
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
