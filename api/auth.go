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

// authorizeLocalControl 对账号切换等高风险端点同时校验真实 TCP 来源、Host/Origin
// 与已配置的现有 token。即使 API 监听 0.0.0.0，远端也不能伪造 Host 调用控制面。
func (s *Server) authorizeLocalControl(w http.ResponseWriter, r *http.Request) bool {
	if !isActualLoopbackRequest(r) || !isTrustedLoopbackRequest(r) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "该控制接口仅允许本机访问")
		return false
	}
	if s.token == "" || constantTimeEqual(sendAuthToken(r), s.token) {
		return true
	}
	writeJSONError(w, http.StatusUnauthorized, "unauthorized", "未授权")
	return false
}

func isActualLoopbackRequest(r *http.Request) bool {
	remote := strings.TrimSpace(r.RemoteAddr)
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = strings.Trim(remote, "[]")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
