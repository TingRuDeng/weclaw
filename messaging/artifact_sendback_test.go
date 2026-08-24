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

func TestArtifactSendBackUsesRouteWorkspace(t *testing.T) {
	globalWorkspace := t.TempDir()
	routeWorkspace := t.TempDir()
	reportPath := filepath.Join(routeWorkspace, "route-report.pdf")
	globalPath := filepath.Join(globalWorkspace, "global-report.pdf")
	if err := os.WriteFile(reportPath, []byte("route result"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, []byte("global result"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(nil, nil)
	h.agentWorkDirs = map[string]string{"codex": globalWorkspace}
	routeUserID := "feishu:thread:route"
	h.ensureCodexSessions().setActiveWorkspace(codexBindingKey(routeUserID, "codex"), routeWorkspace)

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, File: true})
	h.sendReplyWithMediaAfterStreamForRoute(
		context.Background(), reply, "owner", routeUserID, "codex", reportPath+"\n"+globalPath, false,
	)

	if len(reply.Files) != 1 || reply.Files[0] != reportPath {
		t.Fatalf("files=%#v, want only route workspace artifact %q", reply.Files, reportPath)
	}
}

func TestArtifactSendBackUsesTaskWorkspaceSnapshot(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	reportA := filepath.Join(workspaceA, "task-report.pdf")
	reportB := filepath.Join(workspaceB, "unrelated-report.pdf")
	for path, content := range map[string]string{
		reportA: "task result",
		reportB: "unrelated result",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	h := NewHandler(nil, nil)
	h.agentWorkDirs = map[string]string{"codex": workspaceA}
	task, taskCtx, started := h.beginActiveTask(context.Background(), "conversation-1", activeTaskMeta{
		owner: "user-1", agentName: "codex",
	})
	if !started {
		t.Fatal("failed to register active task")
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, File: true})
	lifecycle := h.startAgentTaskLifecycle(agentTaskLifecycleOptions{
		taskCtx: taskCtx, replyCtx: context.Background(), reply: reply,
		task: task, cancel: func() {}, executionKey: "conversation-1",
		userID: "user-1", agentName: "codex", workspaceRoot: workspaceA,
		message: "生成报告", progressConfig: config.DefaultProgressConfig(),
	})

	// 任务启动后切换当前目录，不得改变该任务的附件授权范围。
	h.agentWorkDirs = map[string]string{"codex": workspaceB}
	h.finishAgentTaskLifecycle(lifecycle, reportA+"\n"+reportB, nil)

	if len(reply.Files) != 1 || reply.Files[0] != reportA {
		t.Fatalf("files = %#v, want only task workspace artifact %q", reply.Files, reportA)
	}
}

func TestArtifactSendBackTaskWorkspaceSnapshotResolvesSymlinksAtStart(t *testing.T) {
	root := t.TempDir()
	workspaceA := filepath.Join(root, "workspace-a")
	workspaceB := filepath.Join(root, "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceB, 0o700); err != nil {
		t.Fatal(err)
	}
	reportA := filepath.Join(workspaceA, "task-report.pdf")
	reportB := filepath.Join(workspaceB, "unrelated-report.pdf")
	if err := os.WriteFile(reportA, []byte("task result"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportB, []byte("unrelated result"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceLink := filepath.Join(root, "current-workspace")
	if err := os.Symlink(workspaceA, workspaceLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	h := NewHandler(nil, nil)
	task, taskCtx, started := h.beginActiveTask(context.Background(), "conversation-symlink", activeTaskMeta{
		owner: "user-1", agentName: "codex",
	})
	if !started {
		t.Fatal("failed to register active task")
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, File: true})
	lifecycle := h.startAgentTaskLifecycle(agentTaskLifecycleOptions{
		taskCtx: taskCtx, replyCtx: context.Background(), reply: reply,
		task: task, cancel: func() {}, executionKey: "conversation-symlink",
		userID: "user-1", agentName: "codex", workspaceRoot: workspaceLink,
		message: "生成报告", progressConfig: config.DefaultProgressConfig(),
	})

	if err := os.Remove(workspaceLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(workspaceB, workspaceLink); err != nil {
		t.Fatal(err)
	}
	h.finishAgentTaskLifecycle(lifecycle, reportA+"\n"+reportB, nil)

	if len(reply.Files) != 1 || reply.Files[0] != reportA {
		t.Fatalf("files = %#v, want only original symlink target artifact %q", reply.Files, reportA)
	}
}

func TestArtifactSendBackProjectsAttachmentResultIntoStream(t *testing.T) {
	root := t.TempDir()
	pdfPath := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(pdfPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(nil, nil)
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
