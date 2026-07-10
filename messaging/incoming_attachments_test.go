package messaging

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
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
