package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// verifyReleaseAssetChecksum 校验 release 资产，避免下载内容被截断或替换后直接安装。
func verifyReleaseAssetChecksum(version string, filename string, assetPath string) error {
	checksumFile, err := downloadReleaseAsset(version, "checksums.txt")
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	defer os.Remove(checksumFile)

	data, err := os.ReadFile(checksumFile)
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}
	want, err := parseReleaseChecksums(string(data), filename)
	if err != nil {
		return err
	}
	return verifyDownloadedAssetChecksum(assetPath, want)
}

// parseReleaseChecksums 从 checksums.txt 中查找指定资产的 sha256。
func parseReleaseChecksums(content string, filename string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == filename {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", filename)
}

// verifyDownloadedAssetChecksum 对本地下载文件计算 sha256 并和 release 校验值比较。
func verifyDownloadedAssetChecksum(path string, want string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", path, got, want)
	}
	return nil
}
