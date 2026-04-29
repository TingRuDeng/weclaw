package messaging

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

const (
	remoteMediaDownloadTimeout = 60 * time.Second
	remoteMediaDialTimeout     = 15 * time.Second
	remoteMediaTLSHandshake    = 10 * time.Second
	remoteMediaIdleTimeout     = 30 * time.Second
	maxRemoteMediaRedirects    = 5
	maxRemoteMediaBytes        = 25 * 1024 * 1024
)

func downloadFile(ctx context.Context, url string) ([]byte, string, error) {
	if err := validateRemoteMediaURL(url); err != nil {
		return nil, "", err
	}

	ctx, cancel := context.WithTimeout(ctx, remoteMediaDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := newRemoteMediaHTTPClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := readRemoteMediaBody(resp, maxRemoteMediaBytes)
	if err != nil {
		return nil, "", err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = inferContentType(url)
	}

	return data, contentType, nil
}

func newRemoteMediaHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       remoteMediaDownloadTimeout,
		CheckRedirect: checkRemoteMediaRedirect,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           safeRemoteMediaDialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          maxRemoteMediaRedirects * 2,
			IdleConnTimeout:       remoteMediaIdleTimeout,
			TLSHandshakeTimeout:   remoteMediaTLSHandshake,
			ExpectContinueTimeout: time.Second,
		},
	}
}

func checkRemoteMediaRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRemoteMediaRedirects {
		return fmt.Errorf("remote media redirects exceed %d", maxRemoteMediaRedirects)
	}
	return validateRemoteMediaURL(req.URL.String())
}

func validateRemoteMediaURL(rawURL string) error {
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
	return validateRemoteMediaHost(host)
}

func validateRemoteMediaHost(host string) error {
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("remote media host %q is not allowed", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return validateRemoteMediaIP(ip)
	}
	return nil
}

func safeRemoteMediaDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip, err := resolveSafeRemoteMediaIP(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: remoteMediaDialTimeout}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
}

func resolveSafeRemoteMediaIP(ctx context.Context, host string) (netip.Addr, error) {
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
		if err := validateRemoteMediaIP(ip); err != nil {
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

func validateRemoteMediaIP(ip netip.Addr) error {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("ip %s is not allowed", ip)
	}
	return nil
}

func readRemoteMediaBody(resp *http.Response, maxBytes int64) ([]byte, error) {
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
