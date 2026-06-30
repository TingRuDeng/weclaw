package messaging

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestArtifactSendBackRoutesImageVsFile(t *testing.T) {
	root := t.TempDir()
	imgPath := filepath.Join(root, "chart.png")
	pdfPath := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(imgPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdfPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(nil, nil)
	h.SetAllowedWorkspaceRoots([]string{root})
	h.agentWorkDirs = map[string]string{"codex": root}

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Image: true, File: true})
	body := "已生成产物：\n" + imgPath + "\n" + pdfPath
	h.sendReplyWithMediaAfterStream(context.Background(), reply, "u1", "codex", body, false)

	if len(reply.Images) != 1 || reply.Images[0] != imgPath {
		t.Fatalf("expected image routed to SendImage, got %v", reply.Images)
	}
	if len(reply.Files) != 1 || reply.Files[0] != pdfPath {
		t.Fatalf("expected pdf routed to SendFile, got %v", reply.Files)
	}
}

func TestArtifactSendBackRejectsOutsideRoots(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	outsidePDF := filepath.Join(outside, "secret.pdf")
	if err := os.WriteFile(outsidePDF, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(nil, nil)
	h.agentWorkDirs = map[string]string{"codex": allowed}

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Image: true, File: true})
	h.sendReplyWithMediaAfterStream(context.Background(), reply, "u1", "codex", "见 "+outsidePDF+"\n"+outsidePDF, false)

	if len(reply.Files) != 0 || len(reply.Images) != 0 {
		t.Fatalf("artifact outside allowed roots must not be sent, files=%v images=%v", reply.Files, reply.Images)
	}
}
