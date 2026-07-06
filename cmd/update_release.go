package cmd

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const githubUserAgent = "weclaw-updater"
const updateHTTPTimeout = 60 * time.Second

// releaseAssetNameForRuntime 返回当前发布策略支持的 release 资产名。
func releaseAssetNameForRuntime(goos string, goarch string) (string, error) {
	if goos == "darwin" && goarch == "arm64" {
		return "weclaw_darwin_arm64", nil
	}
	return "", fmt.Errorf("当前 release 只提供 darwin/arm64 包，当前平台是 %s/%s", goos, goarch)
}

func getLatestVersion() (string, error) {
	req, err := newGitHubRequest(http.MethodGet, fmt.Sprintf("https://github.com/%s/releases/latest", githubRepo))
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Timeout: updateHTTPTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusPermanentRedirect {
		return "", fmt.Errorf("GitHub latest redirect returned %d", resp.StatusCode)
	}

	return releaseTagFromLatestRedirect(resp.Header.Get("Location"))
}

func releaseTagFromLatestRedirect(location string) (string, error) {
	location = strings.TrimSpace(location)
	const marker = "/releases/tag/"
	idx := strings.LastIndex(location, marker)
	if idx < 0 {
		return "", fmt.Errorf("missing release tag in redirect %q", location)
	}
	tag := strings.TrimSpace(location[idx+len(marker):])
	if cut := strings.IndexAny(tag, "?#"); cut >= 0 {
		tag = tag[:cut]
	}
	if tag == "" {
		return "", fmt.Errorf("empty release tag in redirect %q", location)
	}
	return tag, nil
}

func newGitHubRequest(method string, url string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", githubUserAgent)
	if token := githubAuthToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func githubAuthToken() string {
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}
