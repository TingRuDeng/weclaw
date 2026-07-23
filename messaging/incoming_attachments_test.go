package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestReadAttachmentDataRejectsOversizedLocalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxIncomingAttachmentBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	_, err = NewHandler(nil, nil).readAttachmentData(context.Background(), platform.Attachment{Path: path})
	if err == nil {
		t.Fatal("oversized attachment should be rejected")
	}
}

func TestReadAttachmentDataRequiresInjectedRemoteDownloader(t *testing.T) {
	_, err := NewHandler(nil, nil).readAttachmentData(context.Background(), platform.Attachment{
		Metadata: map[string]string{
			"encrypt_query_param": "download-param",
			"aes_key":             "aes-key",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "平台未配置附件下载能力") {
		t.Fatalf("err=%v, want missing platform downloader error", err)
	}
}

func TestHandleImageAttachmentSaveReportsDirectoryCreationFailure(t *testing.T) {
	h := NewHandler(nil, nil)
	invalidDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidDir, []byte("占位"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.saveDir = filepath.Join(invalidDir, "child")
	imagePath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(imagePath, []byte{0x89, 0x50, 0x4e, 0x47}, 0o600); err != nil {
		t.Fatal(err)
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.handleImageAttachmentSave(context.Background(), "user-1", reply, platform.Attachment{Path: imagePath})
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "Failed to save image") {
		t.Fatalf("replies=%#v，期望目录创建失败反馈", reply.Texts)
	}
}

func TestSaveIncomingAttachmentUsesUniquePrivateFileAndCleansTemporarySource(t *testing.T) {
	h := NewHandler(nil, nil)
	h.saveDir = t.TempDir()
	source := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(source, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment := platform.Attachment{
		Path: source, FileName: "report.txt", Metadata: map[string]string{"temporary": "true"},
	}

	first, err := h.saveIncomingAttachment(context.Background(), attachment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("temporary source still exists: %v", err)
	}
	secondSource := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(secondSource, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment.Path = secondSource
	second, err := h.saveIncomingAttachment(context.Background(), attachment)
	if err != nil {
		t.Fatal(err)
	}
	if first.path == second.path {
		t.Fatalf("attachments reused path %q", first.path)
	}
	info, err := os.Stat(first.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved mode=%v, want 0600", info.Mode().Perm())
	}
}
