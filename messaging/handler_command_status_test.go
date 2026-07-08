package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestBuildHelpText(t *testing.T) {
	text := buildHelpText()
	if text == "" {
		t.Error("help text is empty")
	}
	for _, want := range []string{
		"WeClaw 帮助",
		"常用：",
		"Codex：",
		"发送消息：",
		"更多：",
		"/status 查看 WeClaw 运行态",
		"/new 新建会话",
		"/cwd <路径> 切换工作目录",
		"/cx status 查看 Codex 会话状态",
		"/cx quota 查看 Codex 账号额度",
		"/cx ls",
		"/cx <编号|..> 选择或返回",
		"/cx cli 打开本地 CLI",
		"/cx app 打开 Codex App",
		"/codex <内容> 发给 Codex",
		"/cc <内容> 发给 Claude",
		"@cc @cx <内容> 同时发送",
		"/cx help Codex 高级命令",
		"/progress 查看进度模式",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help text should mention %q", want)
		}
	}
	if strings.Contains(text, "Available commands") || strings.Contains(text, "Aliases:") {
		t.Error("help text should not use old English headings")
	}
	if strings.Contains(text, "/codex where") || strings.Contains(text, "/codex workspace") {
		t.Error("help text should not mention old Codex session commands")
	}
	for _, hidden := range []string{
		"Codex 主路径：",
		"指定 Agent：",
		"常用别名：",
		"高级能力：",
		"Codex 账号：",
		"/info",
		"/cx usage",
		"/guide",
		"/cancel",
		"/claude 任务",
		"/cs = /cursor",
		"/km = /kimi",
		"/gm = /gemini",
		"/progress、/sw",
		"/sw reload",
		"/cx attach app",
		"/cx detach",
		"/progress 查看或切换进度模式",
	} {
		if strings.Contains(text, hidden) {
			t.Errorf("main help should hide advanced command %q", hidden)
		}
	}
	for _, want := range []string{
		"常用：\n\n/status 查看 WeClaw 运行态",
		"/status 查看 WeClaw 运行态\n\n/new 新建会话",
		"Codex：\n\n/cx status 查看 Codex 会话状态",
		"/cx ls 查看列表\n\n/cx <编号|..> 选择或返回",
		"发送消息：\n\n/codex <内容> 发给 Codex",
		"更多：\n\n/cx help Codex 高级命令",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help text should use blank lines for WeChat rendering, missing %q", want)
		}
	}
}

func TestFeishuHelpSendsChoiceCard(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-help-1",
		Text:      "/help",
	}, reply)

	if len(reply.Texts) != 0 {
		t.Fatalf("feishu help should use card choices, got texts %#v", reply.Texts)
	}
	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v, want one help card", reply.Choices)
	}
	got := reply.Choices[0]
	if !strings.Contains(got.Prompt, "WeClaw 帮助") {
		t.Fatalf("prompt=%q, want help title", got.Prompt)
	}
	wants := map[string]string{
		"/status":    "运行状态",
		"/cx ls":     "Codex 工作空间",
		"/cx status": "Codex 会话状态",
		"/cx help":   "Codex 高级命令",
		"/mode":      "确认模式",
		"/stop":      "停止当前任务",
	}
	if len(got.Choices) != len(wants) {
		t.Fatalf("choices=%#v, want %d entries", got.Choices, len(wants))
	}
	for _, choice := range got.Choices {
		if wants[choice.ID] != choice.Label {
			t.Fatalf("choice=%#v, want label %q", choice, wants[choice.ID])
		}
	}
}

func TestFeishuHelpShowsAdminChoicesOnlyForAdmin(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_admin",
		MessageID: "feishu-admin-help-1",
		Text:      "/help",
		Metadata:  map[string]string{"feishu_union_id": "on_admin"},
	}, reply)

	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v, want one help card", reply.Choices)
	}
	got := helpChoiceIDs(reply.Choices[0].Choices)
	for _, want := range []string{"/update", "/restart", "/feishu users pending", "/feishu users list"} {
		if !got[want] {
			t.Fatalf("admin help choices=%#v, want %q", reply.Choices[0].Choices, want)
		}
	}
	for _, want := range []string{"/feishu users approve-code <授权码>", "/feishu users revoke <用户ID>"} {
		if !strings.Contains(reply.Choices[0].Prompt, want) {
			t.Fatalf("admin help prompt=%q, want %q", reply.Choices[0].Prompt, want)
		}
	}
}

func TestHelpHidesAdminCommandsForNonAdmin(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"on_admin"})
	feishuReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	wechatReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformFeishu,
		UserID:    "ou_user",
		MessageID: "feishu-user-help-1",
		Text:      "/help",
		Metadata:  map[string]string{"feishu_union_id": "on_user"},
	}, feishuReply)
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformWeChat,
		UserID:    "wx_user",
		MessageID: "wechat-user-help-1",
		Text:      "/help",
	}, wechatReply)

	got := helpChoiceIDs(feishuReply.Choices[0].Choices)
	for _, hidden := range []string{"/update", "/restart", "/feishu users pending", "/feishu users list"} {
		if got[hidden] {
			t.Fatalf("non-admin feishu help choices=%#v, should hide %q", feishuReply.Choices[0].Choices, hidden)
		}
		if strings.Contains(feishuReply.Choices[0].Prompt, hidden) {
			t.Fatalf("non-admin feishu help prompt=%q, should hide %q", feishuReply.Choices[0].Prompt, hidden)
		}
		if strings.Contains(wechatReply.Texts[0], hidden) {
			t.Fatalf("non-admin wechat help=%q, should hide %q", wechatReply.Texts[0], hidden)
		}
	}
}

func TestWeChatHelpShowsAdminCommandsForAdmin(t *testing.T) {
	h := NewHandler(nil, nil)
	h.SetAdminUsers([]string{"wx_admin"})
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformWeChat,
		UserID:    "wx_admin",
		MessageID: "wechat-admin-help-1",
		Text:      "/help",
	}, reply)

	if len(reply.Texts) != 1 {
		t.Fatalf("texts=%#v, want one help text", reply.Texts)
	}
	for _, want := range []string{
		"管理员：",
		"/update 远程更新 WeClaw",
		"/restart 重启 WeClaw",
		"/feishu users pending 查看待授权飞书用户",
		"/feishu users revoke <用户ID> 取消飞书用户授权",
	} {
		if !strings.Contains(reply.Texts[0], want) {
			t.Fatalf("admin wechat help=%q, want %q", reply.Texts[0], want)
		}
	}
}

func TestNonFeishuHelpKeepsText(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformWeChat,
		UserID:    "user-1",
		MessageID: "wechat-help-1",
		Text:      "/help",
	}, reply)

	if len(reply.Choices) != 0 {
		t.Fatalf("non-feishu help should not send choice card: %#v", reply.Choices)
	}
	if len(reply.Texts) != 1 || !strings.Contains(reply.Texts[0], "WeClaw 帮助") {
		t.Fatalf("texts=%#v, want help text", reply.Texts)
	}
}

func helpChoiceIDs(choices []platform.Choice) map[string]bool {
	ids := make(map[string]bool, len(choices))
	for _, choice := range choices {
		ids[choice.ID] = true
	}
	return ids
}

func TestBuildCodexSessionHelpTextIncludesDescriptions(t *testing.T) {
	text := buildCodexSessionHelpText()
	for _, want := range []string{
		"/cx ls 查看工作空间或当前工作空间会话",
		"/cx <编号|..> 选择当前列表项或返回上一级",
		"/cx cd <编号|工作空间名|..> 进入工作空间或返回工作空间列表",
		"/cx switch <编号> 切换当前工作空间会话",
		"/cx new 新建当前工作空间会话",
		"/cx pwd 查看当前工作空间",
		"/cx cli 打开本地 CLI 接手当前 thread",
		"/cx app 打开 Codex App 到当前工作空间",
		"/cx status 查看 remote、thread 和本地入口状态",
		"/cx quota 查看 Codex 账号额度",
		"/cx clean 清理已不存在的 WeClaw 工作空间记录",
		"/cx model status 查看 Codex 模型状态",
		"/cx model ls 查看可用 Codex 模型",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Codex help should describe %q, got %q", want, text)
		}
	}
}
