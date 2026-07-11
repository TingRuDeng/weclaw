package messaging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const artifactSidecarSuffix = ".sidecar.md"

// writeUniqueArtifactPair 独占创建正文和 sidecar，并发同名保存时自动选择新后缀。
func writeUniqueArtifactPair(dir string, baseName string, ext string, data []byte, sidecar []byte) (string, error) {
	baseName = safeArtifactBaseName(baseName)
	for sequence := 1; ; sequence++ {
		path := artifactCandidatePath(dir, baseName, ext, sequence)
		mainFile, collision, err := createExclusiveArtifact(path)
		if collision {
			continue
		}
		if err != nil {
			return "", err
		}
		sidecarFile, sidecarCollision, sidecarErr := createExclusiveArtifact(path + artifactSidecarSuffix)
		if sidecarCollision {
			_ = mainFile.Close()
			_ = os.Remove(path)
			continue
		}
		if sidecarErr != nil {
			_ = mainFile.Close()
			_ = os.Remove(path)
			return "", sidecarErr
		}
		if err := writeArtifactPair(mainFile, sidecarFile, data, sidecar); err != nil {
			_ = os.Remove(path)
			_ = os.Remove(path + artifactSidecarSuffix)
			return "", err
		}
		return path, nil
	}
}

func safeArtifactBaseName(baseName string) string {
	baseName = filepath.Base(strings.TrimSpace(baseName))
	if baseName == "" || baseName == "." {
		return "untitled"
	}
	return baseName
}

func artifactCandidatePath(dir string, baseName string, ext string, sequence int) string {
	if sequence == 1 {
		return filepath.Join(dir, baseName+ext)
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", baseName, sequence, ext))
}

func createExclusiveArtifact(path string) (*os.File, bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("创建资料文件失败：%w", err)
	}
	return file, false, nil
}

func writeArtifactPair(mainFile *os.File, sidecarFile *os.File, data []byte, sidecar []byte) error {
	if _, err := mainFile.Write(data); err != nil {
		_ = mainFile.Close()
		_ = sidecarFile.Close()
		return fmt.Errorf("写入资料文件失败：%w", err)
	}
	if err := mainFile.Close(); err != nil {
		_ = sidecarFile.Close()
		return fmt.Errorf("关闭资料文件失败：%w", err)
	}
	if _, err := sidecarFile.Write(sidecar); err != nil {
		_ = sidecarFile.Close()
		return fmt.Errorf("写入资料索引失败：%w", err)
	}
	if err := sidecarFile.Close(); err != nil {
		return fmt.Errorf("关闭资料索引失败：%w", err)
	}
	return nil
}
