package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type localPackageManifest struct {
	Schema  int               `json:"schema"`
	Version string            `json:"version"`
	Commit  string            `json:"commit"`
	Assets  map[string]string `json:"assets"`
}

var localPackageCommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var localPackageDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func requireLocalPackageFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("本地包必须使用普通文件：%s", path)
	}
	return nil
}

func readLocalPackage(directory string) (localPackageManifest, error) {
	var manifest localPackageManifest
	info, err := os.Lstat(directory)
	if err != nil {
		return manifest, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return manifest, fmt.Errorf("本地包必须使用真实目录")
	}
	path := directory + ".package.json"
	if err := requireLocalPackageFile(path); err != nil {
		return manifest, err
	}
	file, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return manifest, fmt.Errorf("本地包清单包含额外数据")
	}
	if manifest.Schema != 1 || !stableUpdateReleaseTagPattern.MatchString(manifest.Version) || !localPackageCommitPattern.MatchString(manifest.Commit) {
		return manifest, fmt.Errorf("本地包清单版本或提交无效")
	}
	if filepath.Base(directory) != manifest.Version {
		return manifest, fmt.Errorf("本地包目录与版本不一致")
	}
	names := []string{"weclaw_darwin_arm64", "weclaw_linux_amd64", "checksums.txt"}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return manifest, err
	}
	if len(entries) != len(names) || len(manifest.Assets) != len(names) {
		return manifest, fmt.Errorf("本地包资产数量不匹配")
	}
	for _, name := range names {
		path := filepath.Join(directory, name)
		if err := requireLocalPackageFile(path); err != nil {
			return manifest, err
		}
		if !localPackageDigestPattern.MatchString(manifest.Assets[name]) {
			return manifest, fmt.Errorf("本地包摘要无效：%s", name)
		}
		if err := verifyDownloadedAssetChecksum(path, manifest.Assets[name]); err != nil {
			return manifest, err
		}
	}
	checksums, err := os.ReadFile(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		return manifest, err
	}
	if len(strings.Fields(string(checksums))) != 4 {
		return manifest, fmt.Errorf("本地包摘要清单条目无效")
	}
	for _, name := range names[:2] {
		want, err := parseReleaseChecksums(string(checksums), name)
		if err != nil {
			return manifest, err
		}
		if want != manifest.Assets[name] {
			return manifest, fmt.Errorf("本地包摘要清单不一致：%s", name)
		}
	}
	return manifest, nil
}

func localBinaryVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", fmt.Errorf("读取本地程序版本：%w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 || fields[0] != "weclaw" || fields[2] != "("+runtime.GOOS+"/"+runtime.GOARCH+")" {
		return "", fmt.Errorf("本地程序版本或平台无效：%s", path)
	}
	return fields[1], nil
}

func installLocalPackage(ctx context.Context, directory, target string, completion updateCompletionOps, out io.Writer) error {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	manifest, err := readLocalPackage(directory)
	if err != nil {
		return fmt.Errorf("校验本地包失败：%w", err)
	}
	filename, err := releaseAssetNameForRuntime(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if target == "" {
		target, err = os.Executable()
		if err != nil {
			return err
		}
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	if err := requireLocalPackageFile(target); err != nil {
		return err
	}
	resolvedDir, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	if filepath.Dir(target) == resolvedDir {
		return fmt.Errorf("安装目标不能是待发布包内的资产")
	}
	if err := validateUpdateTargetMatchesRuntime(target); err != nil {
		return err
	}
	current, err := localBinaryVersion(ctx, target)
	if err != nil {
		return err
	}
	if stableUpdateReleaseTagPattern.MatchString(current) {
		comparison, err := compareStableReleaseTags(current, manifest.Version)
		if err != nil {
			return err
		}
		if comparison > 0 {
			return fmt.Errorf("拒绝从 %s 降级到 %s", current, manifest.Version)
		}
	}
	// Install a verified snapshot so an edited package cannot race the replacement.
	source, err := os.Open(filepath.Join(directory, filename))
	if err != nil {
		return err
	}
	defer source.Close()
	snapshot, err := os.CreateTemp("", "weclaw-local-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(snapshot.Name())
	written, copyErr := io.Copy(snapshot, io.LimitReader(source, maxUpdateDownloadBytes+1))
	closeErr := snapshot.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxUpdateDownloadBytes {
		return fmt.Errorf("本地包资产过大")
	}
	if err := verifyDownloadedAssetChecksum(snapshot.Name(), manifest.Assets[filename]); err != nil {
		return err
	}
	if err := os.Chmod(snapshot.Name(), 0o755); err != nil {
		return err
	}
	version, err := localBinaryVersion(ctx, snapshot.Name())
	if err != nil {
		return err
	}
	if version != manifest.Version {
		return fmt.Errorf("本地包程序版本与清单不一致")
	}
	if err := verifyDownloadedAssetChecksum(target, manifest.Assets[filename]); err == nil {
		fmt.Fprintf(out, "本机已安装相同包 (%s)。\n", manifest.Version)
		return nil
	}
	transaction, err := installBinaryWithRollback(snapshot.Name(), target)
	if err != nil {
		return err
	}
	if err := completeUpdateWithRollback(ctx, false, false, completion, transaction.Rollback); err != nil {
		return err
	}
	transaction.Commit()
	fmt.Fprintf(out, "本地包已安装：%s -> %s (%s)，未重启服务。\n", current, manifest.Version, manifest.Commit[:12])
	return nil
}
