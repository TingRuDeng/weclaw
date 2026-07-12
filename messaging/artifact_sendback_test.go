package messaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
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

func TestArtifactSendBackProjectsAttachmentResultIntoStream(t *testing.T) {
	root := t.TempDir()
	pdfPath := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(pdfPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(nil, nil)
	h.SetAllowedWorkspaceRoots([]string{root})
	h.agentWorkDirs = map[string]string{"codex": root}
	reply := platformtest.NewReplier(platform.Capabilities{
		Text: true, File: true, Streaming: true, StreamCompletionNotification: true,
	})
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	_, finish := h.startProgressSessionWithFinal(context.Background(), reply, "", "生成报告", cfg)

	h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: context.Background(), replyWriter: reply, userID: "u1",
			agentName: "codex", reply: "报告：\n" + pdfPath,
		},
		finish: finish,
	})

	if len(reply.Files) != 1 || reply.Files[0] != pdfPath {
		t.Fatalf("files = %#v", reply.Files)
	}
	if strings.Contains(reply.Stream.Completed, pdfPath) || !strings.Contains(reply.Stream.Completed, "已发送附件：report.pdf") {
		t.Fatalf("completed = %q", reply.Stream.Completed)
	}
}

func TestTerminalCardFailureFallsBackToFullText(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{
		Text: true, Streaming: true, StreamCompletionNotification: true,
	})
	reply.Stream.CompleteErr = errors.New("update failed")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	_, finish := h.startProgressSessionWithFinal(context.Background(), reply, "", "任务", cfg)

	h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: context.Background(), replyWriter: reply, userID: "u1",
			agentName: "codex", reply: "完整结果",
		},
		finish: finish,
	})

	if len(reply.Texts) != 1 || reply.Texts[0] != "完整结果" {
		t.Fatalf("texts = %#v", reply.Texts)
	}
}

func TestStreamCreationFailureStillDeliversFullText(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	reply.OpenStreamErr = errors.New("create failed")
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	_, finish := h.startProgressSessionWithFinal(context.Background(), reply, "", "任务", cfg)

	h.finishAndSendProgressReply(progressReplyDelivery{
		delivery: replyDeliveryRequest{
			ctx: context.Background(), replyWriter: reply, userID: "u1",
			agentName: "codex", reply: "完整结果",
		},
		finish: finish,
	})

	if len(reply.Texts) != 2 || reply.Texts[1] != "完整结果" {
		t.Fatalf("texts = %#v", reply.Texts)
	}
}
