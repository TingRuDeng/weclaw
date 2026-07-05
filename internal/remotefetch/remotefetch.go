package remotefetch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Options 定义远程文件下载的网络超时、重定向次数和响应体大小上限。
type Options struct {
	Timeout             time.Duration
	DialTimeout         time.Duration
	TLSHandshakeTimeout time.Duration
	IdleTimeout         time.Duration
	MaxRedirects        int
	MaxBytes            int64
}

// DefaultOptions 返回平台共享的安全默认下载参数。
func DefaultOptions() Options {
	return Options{
		Timeout:             60 * time.Second,
		DialTimeout:         15 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleTimeout:         30 * time.Second,
		MaxRedirects:        5,
		MaxBytes:            25 * 1024 * 1024,
	}
}

// Download 下载远程文件，并在发起请求、重定向和读取响应时统一执行安全校验。
func Download(ctx context.Context, rawURL string, opts Options, inferContentType func(string) string) ([]byte, string, error) {
	opts = normalizeOptions(opts)
	if err := ValidateURL(rawURL); err != nil {
		return nil, "", err
	}
	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := NewHTTPClient(opts).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := ReadBody(resp, opts.MaxBytes)
	if err != nil {
		return nil, "", err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" && inferContentType != nil {
		contentType = inferContentType(rawURL)
	}
	return data, contentType, nil
}

// NewHTTPClient 创建禁用代理且带 SSRF 防护的 HTTP client。
func NewHTTPClient(opts Options) *http.Client {
	opts = normalizeOptions(opts)
	return &http.Client{
		Timeout: opts.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= opts.MaxRedirects {
				return fmt.Errorf("remote media redirects exceed %d", opts.MaxRedirects)
			}
			return ValidateURL(req.URL.String())
		},
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           safeDialContext(opts),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          opts.MaxRedirects * 2,
			IdleConnTimeout:       opts.IdleTimeout,
			TLSHandshakeTimeout:   opts.TLSHandshakeTimeout,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// ValidateURL 校验远程文件 URL 的协议和主机，先阻止明显不安全的目标。
func ValidateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("remote media scheme %q is not allowed", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("remote media host is required")
	}
	return validateHost(host)
}

// ReadBody 在大小上限内读取响应体，避免未知 Content-Length 撑爆内存。
func ReadBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultOptions().MaxBytes
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("remote media is too large: %d > %d", resp.ContentLength, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("remote media exceeds %d bytes", maxBytes)
	}
	return data, nil
}

// normalizeOptions 用默认值补齐调用方未显式配置的下载参数。
func normalizeOptions(opts Options) Options {
	defaults := DefaultOptions()
	if opts.Timeout <= 0 {
		opts.Timeout = defaults.Timeout
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaults.DialTimeout
	}
	if opts.TLSHandshakeTimeout <= 0 {
		opts.TLSHandshakeTimeout = defaults.TLSHandshakeTimeout
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = defaults.IdleTimeout
	}
	if opts.MaxRedirects <= 0 {
		opts.MaxRedirects = defaults.MaxRedirects
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaults.MaxBytes
	}
	return opts
}

// validateHost 在 DNS 解析前先处理 localhost 和直接 IP 访问。
func validateHost(host string) error {
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("remote media host %q is not allowed", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return validateIP(ip)
	}
	return nil
}

// safeDialContext 在真实拨号前解析并校验目标 IP，避免域名绕过 URL 校验。
func safeDialContext(opts Options) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ip, err := resolveSafeIP(ctx, host)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: opts.DialTimeout}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
}

// resolveSafeIP 返回第一个可用公网 IP，并拒绝任何解析到内网地址的主机。
func resolveSafeIP(ctx context.Context, host string) (netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	var selected netip.Addr
	for _, ipAddr := range ips {
		ip, ok := netip.AddrFromSlice(ipAddr.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if err := validateIP(ip); err != nil {
			return netip.Addr{}, fmt.Errorf("remote media host %q resolves to unsafe ip %s: %w", host, ip, err)
		}
		if !selected.IsValid() {
			selected = ip
		}
	}
	if !selected.IsValid() {
		return netip.Addr{}, fmt.Errorf("remote media host %q has no usable ip", host)
	}
	return selected, nil
}

// validateIP 拒绝本机、内网、链路本地、多播和未指定地址。
func validateIP(ip netip.Addr) error {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("ip %s is not allowed", ip)
	}
	return nil
}
