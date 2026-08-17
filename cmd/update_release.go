package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const githubUserAgent = "weclaw-updater"
const updateMetadataHTTPTimeout = 60 * time.Second
const updateAssetHTTPTimeout = 10 * time.Minute
const updateReleaseTagEnv = "WECLAW_UPDATE_RELEASE_TAG"
const githubAPIBaseURL = "https://api.github.com"
const giteeAPIBaseURL = "https://gitee.com/api/v5"

type releaseSource string

const (
	releaseSourceAuto   releaseSource = "auto"
	releaseSourceGitHub releaseSource = "github"
	releaseSourceGitee  releaseSource = "gitee"
)

type releaseAvailabilityError struct {
	err error
}

func (e releaseAvailabilityError) Error() string { return e.err.Error() }
func (e releaseAvailabilityError) Unwrap() error { return e.err }

func releaseUnavailable(err error) error {
	if err == nil {
		return nil
	}
	return releaseAvailabilityError{err: err}
}

func isReleaseUnavailable(err error) bool {
	var unavailable releaseAvailabilityError
	return errors.As(err, &unavailable)
}

var stableUpdateReleaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type githubReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

func parseReleaseSource(value string) (releaseSource, error) {
	source := releaseSource(strings.ToLower(strings.TrimSpace(value)))
	if source == "" {
		source = releaseSourceAuto
	}
	switch source {
	case releaseSourceAuto, releaseSourceGitHub, releaseSourceGitee:
		return source, nil
	default:
		return "", fmt.Errorf("不支持的更新来源 %q；可选 auto、github、gitee", value)
	}
}

func resolveLatestRelease(
	source releaseSource,
	githubLatest func() (string, error),
	giteeLatest func() (string, error),
) (string, []releaseSource, error) {
	switch source {
	case releaseSourceGitHub:
		version, err := githubLatest()
		return version, []releaseSource{releaseSourceGitHub}, err
	case releaseSourceGitee:
		version, err := giteeLatest()
		return version, []releaseSource{releaseSourceGitee}, err
	case releaseSourceAuto:
		version, err := githubLatest()
		if err == nil {
			return version, []releaseSource{releaseSourceGitHub, releaseSourceGitee}, nil
		}
		if !isReleaseUnavailable(err) {
			return "", nil, err
		}
		fmt.Printf("GitHub 发布源不可用（%v），切换到 Gitee 镜像。\n", err)
		version, err = giteeLatest()
		if err != nil {
			return "", nil, err
		}
		return version, []releaseSource{releaseSourceGitee}, nil
	default:
		return "", nil, fmt.Errorf("不支持的更新来源 %q", source)
	}
}

func tryReleaseSources(sources []releaseSource, attempt func(releaseSource) (string, error)) (string, error) {
	if len(sources) == 0 {
		return "", fmt.Errorf("没有可用的 release 来源")
	}
	for index, source := range sources {
		result, err := attempt(source)
		if err == nil {
			return result, nil
		}
		if !isReleaseUnavailable(err) || index == len(sources)-1 {
			return "", err
		}
		fmt.Printf("%s 发布资产不可用（%v），切换到 %s。\n", source, err, sources[index+1])
	}
	return "", fmt.Errorf("没有可用的 release 来源")
}

func getGiteeLatestVersionFromBase(apiBaseURL string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(apiBaseURL, "/"), giteeRepo)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", githubUserAgent)
	resp, err := (&http.Client{Timeout: updateMetadataHTTPTimeout}).Do(req)
	if err != nil {
		return "", releaseUnavailable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("Gitee latest release returned %d", resp.StatusCode)
		if resp.StatusCode >= http.StatusInternalServerError {
			return "", releaseUnavailable(err)
		}
		return "", err
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&release); err != nil {
		return "", fmt.Errorf("decode Gitee latest release: %w", err)
	}
	if !stableUpdateReleaseTagPattern.MatchString(release.TagName) {
		return "", fmt.Errorf("Gitee latest release tag %q 必须是 vX.Y.Z 格式", release.TagName)
	}
	return release.TagName, nil
}

func releaseAssetURLForSource(source releaseSource, version string, filename string) (string, error) {
	if !stableUpdateReleaseTagPattern.MatchString(version) {
		return "", fmt.Errorf("release tag %q 必须是 vX.Y.Z 格式", version)
	}
	if filename == "" || strings.ContainsAny(filename, `/\\`) {
		return "", fmt.Errorf("无效的 release 资产名 %q", filename)
	}
	switch source {
	case releaseSourceGitHub:
		return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", githubRepo, version, filename), nil
	case releaseSourceGitee:
		if strings.HasPrefix(filename, "weclaw_") {
			filename += ".gz"
		}
		return fmt.Sprintf("https://gitee.com/%s/releases/download/%s/%s", giteeRepo, version, filename), nil
	default:
		return "", fmt.Errorf("来源 %q 不能生成 release 资产地址", source)
	}
}

// releaseAssetNameForRuntime 返回当前发布策略支持的 release 资产名。
func releaseAssetNameForRuntime(goos string, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64", "linux/amd64":
		return fmt.Sprintf("weclaw_%s_%s", goos, goarch), nil
	default:
		return "", fmt.Errorf("当前 release 支持 darwin/arm64、linux/amd64，当前平台是 %s/%s", goos, goarch)
	}
}

func getLatestVersion() (string, error) {
	req, err := newGitHubRequest(http.MethodGet, fmt.Sprintf("https://github.com/%s/releases/latest", githubRepo))
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Timeout: updateMetadataHTTPTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", releaseUnavailable(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusPermanentRedirect {
		err := fmt.Errorf("GitHub latest redirect returned %d", resp.StatusCode)
		if resp.StatusCode >= http.StatusInternalServerError {
			return "", releaseUnavailable(err)
		}
		return "", err
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

func downloadReleaseAssetFromSource(source releaseSource, version string, filename string) (string, error) {
	if source == releaseSourceGitHub {
		return downloadReleaseAsset(version, filename)
	}
	url, err := releaseAssetURLForSource(source, version, filename)
	if err != nil {
		return "", err
	}
	path, err := downloadFile(url)
	if err != nil {
		return "", err
	}
	if source != releaseSourceGitee || !strings.HasPrefix(filename, "weclaw_") {
		return path, nil
	}
	defer os.Remove(path)
	return decompressGiteeReleaseAsset(path)
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
	if token := githubAuthToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: updateMetadataHTTPTimeout}
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
	if token := githubAuthToken(); token != "" && isGitHubHost(req.URL) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func isGitHubHost(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com", "api.github.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}

func githubAuthToken() string {
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}
