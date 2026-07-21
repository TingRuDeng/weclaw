package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const githubUserAgent = "weclaw-updater"
const updateHTTPTimeout = 60 * time.Second
const updateReleaseTagEnv = "WECLAW_UPDATE_RELEASE_TAG"
const githubAPIBaseURL = "https://api.github.com"

var stableUpdateReleaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type githubReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

// releaseAssetNameForRuntime 返回当前发布策略支持的 release 资产名。
func releaseAssetNameForRuntime(goos string, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64":
		return fmt.Sprintf("weclaw_%s_%s", goos, goarch), nil
	default:
		return "", fmt.Errorf("当前 release 支持 darwin/arm64、darwin/amd64、linux/arm64、linux/amd64，当前平台是 %s/%s", goos, goarch)
	}
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

// updateReleaseTagOverride 仅供正式发布烟测选择尚处于 draft 的目标 tag。
// 严格限制为稳定版语义化 tag，避免把环境变量直接拼入下载路径。
func updateReleaseTagOverride() (string, bool, error) {
	tag := strings.TrimSpace(os.Getenv(updateReleaseTagEnv))
	if tag == "" {
		return "", false, nil
	}
	if !stableUpdateReleaseTagPattern.MatchString(tag) {
		return "", true, fmt.Errorf("%s 必须是 vX.Y.Z 格式", updateReleaseTagEnv)
	}
	return tag, true, nil
}

// downloadReleaseAsset 让普通更新走公开下载地址，让 draft 烟测通过 GitHub API
// 下载受保护资产；两条路径最终仍复用相同的大小限制、checksum 和原子替换。
func downloadReleaseAsset(version string, filename string) (string, error) {
	tag, overridden, err := updateReleaseTagOverride()
	if err != nil {
		return "", err
	}
	if !overridden || tag != version {
		return downloadFile(fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", githubRepo, version, filename))
	}
	assetURL, err := githubReleaseAssetAPIURL(version, filename)
	if err != nil {
		return "", err
	}
	return downloadFileWithAccept(assetURL, "application/octet-stream")
}

func githubReleaseAssetAPIURL(version string, filename string) (string, error) {
	return githubReleaseAssetAPIURLFromBase(githubAPIBaseURL, version, filename)
}

// githubReleaseAssetAPIURLFromBase 通过 release list 查找目标 draft。
// GitHub 的 releases/tags/{tag} 端点只返回已发布版本，认证后的 list 才包含 draft。
func githubReleaseAssetAPIURLFromBase(apiBaseURL string, version string, filename string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=100", strings.TrimRight(apiBaseURL, "/"), githubRepo)
	req, err := newGitHubRequest(http.MethodGet, endpoint)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: updateHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub release list returned %d", resp.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&releases); err != nil {
		return "", fmt.Errorf("decode GitHub release list: %w", err)
	}
	for _, release := range releases {
		if release.TagName == version {
			return findGitHubReleaseAssetAPIURL(release, version, filename)
		}
	}
	return "", fmt.Errorf("release %s not found in authenticated GitHub release list", version)
}

func findGitHubReleaseAssetAPIURL(release githubRelease, version string, filename string) (string, error) {
	for _, asset := range release.Assets {
		if asset.Name == filename && strings.HasPrefix(asset.URL, "https://api.github.com/repos/") {
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("release asset %s not found for %s", filename, version)
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
