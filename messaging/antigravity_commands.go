package messaging

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fastclaw-ai/weclaw/internal/antigravity"
	"github.com/fastclaw-ai/weclaw/platform"
)

const (
	antigravityNavModeProjects = "projects"
	antigravityNavModeSessions = "sessions"
)

type antigravityNavigationView struct {
	Token     string
	Mode      string
	ProjectID string
	Page      int
}

func isAntigravityCommand(trimmed string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(trimmed)))
	return len(fields) > 0 && fields[0] == "/ag"
}

// handleAntigravityCommand owns the /ag namespace. Every command requires a
// provable Feishu private chat.
func (h *Handler) handleAntigravityCommand(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, trimmed string) bool {
	if !isFeishuPrivateMessage(msg, routeUserID) {
		sendPlatformText(ctx, reply, msg.UserID, "Antigravity（/ag）仅支持飞书私聊。")
		return true
	}
	client, ok := h.antigravityClientSnapshot()
	if !ok || client == nil {
		sendPlatformText(ctx, reply, msg.UserID, "Antigravity Sidecar 未连接。请先启动 `weclaw ag-sidecar`。")
		return true
	}
	fields := strings.Fields(trimmed)
	sub := ""
	if len(fields) > 1 {
		sub = strings.ToLower(fields[1])
	}
	switch {
	case sub == "" || sub == "ls":
		h.handleAntigravityList(ctx, msg, routeUserID, reply)
	case sub == "project":
		h.handleAntigravityProject(ctx, msg, routeUserID, reply, fields)
	case sub == "page":
		h.handleAntigravityPage(ctx, msg, routeUserID, reply, fields)
	case sub == "switch":
		h.handleAntigravitySwitchCommand(ctx, msg, routeUserID, reply, fields)
	case sub == "new":
		h.handleAntigravityNew(ctx, msg, routeUserID, reply, trimmed)
	case sub == "new-select":
		h.handleAntigravityNewSelect(ctx, msg, routeUserID, reply, fields)
	case sub == "status":
		h.handleAntigravityStatus(ctx, msg, routeUserID, reply)
	case sub == "release":
		h.handleAntigravityRelease(ctx, msg, routeUserID, reply)
	case sub == "help":
		sendPlatformText(ctx, reply, msg.UserID, antigravityHelpText())
	default:
		if index, err := strconv.Atoi(sub); err == nil && index > 0 {
			h.handleAntigravitySwitchIndex(ctx, msg, routeUserID, reply, index)
			break
		}
		sendPlatformText(ctx, reply, msg.UserID, antigravityHelpText())
	}
	return true
}

func (h *Handler) antigravityNavScope(msg platform.IncomingMessage, routeUserID string) feishuNavigationSnapshotScope {
	return feishuNavigationSnapshotScope{
		AccountID: msg.AccountID, ActorUserID: msg.UserID, BindingKey: routeUserID,
		AgentKind: antigravityAgentKind, Section: feishuNavigationSectionSessions,
	}
}

func (h *Handler) antigravityNavView(accountID, routeUserID string) (antigravityNavigationView, bool) {
	h.antigravityMu.Lock()
	defer h.antigravityMu.Unlock()
	view, ok := h.antigravityNav[antigravityBindingKey(accountID, routeUserID)]
	return view, ok
}

func (h *Handler) setAntigravityNavView(accountID, routeUserID string, view antigravityNavigationView) {
	h.antigravityMu.Lock()
	if h.antigravityNav == nil {
		h.antigravityNav = make(map[string]antigravityNavigationView)
	}
	h.antigravityNav[antigravityBindingKey(accountID, routeUserID)] = view
	h.antigravityMu.Unlock()
}

func (h *Handler) handleAntigravityList(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier) {
	client, _ := h.antigravityClientSnapshot()
	conversations, err := client.ListConversations(ctx)
	if err != nil {
		sendPlatformText(ctx, reply, msg.UserID, "读取 Antigravity 会话失败："+err.Error())
		return
	}
	if len(conversations) == 0 {
		sendPlatformText(ctx, reply, msg.UserID, "桌面端暂无可用 Antigravity 会话。")
		return
	}
	choices := make([]platform.Choice, 0, len(conversations))
	for _, conversation := range conversations {
		choices = append(choices, platform.Choice{
			ID:    "/ag switch " + conversation.ID,
			Label: antigravityDisplayTitle(conversation),
			Metadata: map[string]string{
				"project_id":   antigravityProjectToken(conversation.ProjectID),
				"project_name": conversation.ProjectName,
			},
		})
	}
	token := h.feishuNavSnapshots.issue(h.antigravityNavScope(msg, routeUserID), choices)
	view := antigravityNavigationView{Token: token, Mode: antigravityNavModeProjects, Page: 1}
	if projects := antigravity.ProjectGroups(conversations); len(projects) == 1 {
		view.Mode = antigravityNavModeSessions
		view.ProjectID = antigravityProjectToken(projects[0].ID)
	}
	h.setAntigravityNavView(msg.AccountID, routeUserID, view)
	h.renderAntigravityNavigation(ctx, msg, routeUserID, reply, view)
}

func (h *Handler) renderAntigravityNavigation(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, view antigravityNavigationView) {
	choices, ok := h.feishuNavSnapshots.load(view.Token, h.antigravityNavScope(msg, routeUserID))
	if !ok {
		sendPlatformText(ctx, reply, msg.UserID, "Antigravity 列表已过期，请发送 /ag ls 刷新。")
		return
	}
	var rendered []platform.Choice
	var pageInfo feishuChoicePage
	var prompt string
	if view.Mode == antigravityNavModeProjects {
		rendered, pageInfo = antigravityProjectChoices(choices, view.Page)
		prompt = feishuPaginatedPrompt("Antigravity 项目\n请选择要进入的项目。", pageInfo)
	} else {
		rendered, pageInfo = antigravitySessionChoices(choices, view.ProjectID, view.Page)
		prompt = feishuPaginatedPrompt(
			fmt.Sprintf("%s 会话\n请选择要切换的会话。", antigravityProjectNameFromChoices(choices, view.ProjectID)),
			pageInfo,
		)
	}
	rendered = platformChoicesWithRoute(rendered, platformMessageChoiceRoute(msg, routeUserID))
	if err := reply.AskChoices(ctx, prompt, rendered); err != nil {
		log.Printf("[handler] failed to send antigravity choices: %v", err)
		sendPlatformText(ctx, reply, msg.UserID, antigravityChoiceFallback(rendered))
	}
}

func antigravityProjectChoices(choices []platform.Choice, page int) ([]platform.Choice, feishuChoicePage) {
	type projectBucket struct {
		id string
	}
	order := make([]projectBucket, 0, len(choices))
	counts := make(map[string]int)
	labels := make(map[string]string)
	for _, choice := range choices {
		id := antigravityProjectToken(choice.Metadata["project_id"])
		if _, ok := counts[id]; !ok {
			order = append(order, projectBucket{id: id})
		}
		counts[id]++
		if name := strings.TrimSpace(choice.Metadata["project_name"]); name != "" {
			labels[id] = name
		}
	}
	items := make([]platform.Choice, 0, len(order))
	for _, bucket := range order {
		name := labels[bucket.id]
		if name == "" {
			name = antigravity.ProjectName(nil, antigravityProjectIDFromToken(bucket.id))
		}
		items = append(items, platform.Choice{
			ID:    "/ag project " + bucket.id,
			Label: fmt.Sprintf("%s (%d)", name, counts[bucket.id]),
		})
	}
	return antigravityPaginate(items, page, true)
}

func antigravitySessionChoices(choices []platform.Choice, projectID string, page int) ([]platform.Choice, feishuChoicePage) {
	items := make([]platform.Choice, 0, len(choices))
	for _, choice := range choices {
		if antigravityProjectToken(choice.Metadata["project_id"]) != antigravityProjectToken(antigravityProjectIDFromToken(projectID)) {
			continue
		}
		items = append(items, platform.Choice{
			ID: choice.ID, Label: choice.Label,
			Metadata: mergeChoiceMetadata(choice.Metadata, nil),
		})
	}
	result, info := antigravityPaginate(items, page, true)
	if antigravityProjectCount(choices) > 1 {
		result = append(result, feishuNavigationChoice("/ag project ..", "← 返回上一级"))
	}
	return result, info
}

func antigravityPaginate(items []platform.Choice, page int, indexed bool) ([]platform.Choice, feishuChoicePage) {
	pageItems, info := paginateFeishuChoices(items, page)
	result := append([]platform.Choice(nil), pageItems...)
	if indexed {
		for index := range result {
			result[index].Label = feishuIndexedChoiceLabel(index, result[index].Label)
		}
	}
	if info.TotalPages > 1 {
		if info.Number > 1 {
			result = append(result, feishuNavigationChoice(fmt.Sprintf("/ag page %d", info.Number-1), "← 上一页"))
		}
		if info.Number < info.TotalPages {
			result = append(result, feishuNavigationChoice(fmt.Sprintf("/ag page %d", info.Number+1), "下一页 →"))
		}
	}
	return result, info
}

func antigravityProjectNameFromChoices(choices []platform.Choice, projectID string) string {
	projectID = antigravityProjectToken(antigravityProjectIDFromToken(projectID))
	for _, choice := range choices {
		if antigravityProjectToken(choice.Metadata["project_id"]) == projectID {
			if name := strings.TrimSpace(choice.Metadata["project_name"]); name != "" {
				return name
			}
		}
	}
	return antigravity.ProjectName(nil, antigravityProjectIDFromToken(projectID))
}

func antigravityChoiceFallback(choices []platform.Choice) string {
	lines := make([]string, 0, len(choices))
	for _, choice := range choices {
		lines = append(lines, choice.ID+"  "+choice.Label)
	}
	return strings.Join(lines, "\n")
}

func (h *Handler) handleAntigravityProject(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, fields []string) {
	if len(fields) < 3 {
		sendPlatformText(ctx, reply, msg.UserID, "用法：/ag project <项目ID>")
		return
	}
	view, ok := h.antigravityNavView(msg.AccountID, routeUserID)
	if !ok {
		sendPlatformText(ctx, reply, msg.UserID, "Antigravity 列表已过期，请发送 /ag ls 刷新。")
		return
	}
	choices, ok := h.feishuNavSnapshots.load(view.Token, h.antigravityNavScope(msg, routeUserID))
	if !ok {
		sendPlatformText(ctx, reply, msg.UserID, "Antigravity 列表已过期，请发送 /ag ls 刷新。")
		return
	}
	if fields[2] == ".." {
		view.Mode = antigravityNavModeProjects
		view.ProjectID = ""
		view.Page = 1
		h.setAntigravityNavView(msg.AccountID, routeUserID, view)
		h.renderAntigravityNavigation(ctx, msg, routeUserID, reply, view)
		return
	}
	projectID := antigravityProjectToken(fields[2])
	found := false
	for _, choice := range choices {
		if antigravityProjectToken(choice.Metadata["project_id"]) == projectID {
			found = true
			break
		}
	}
	if !found {
		sendPlatformText(ctx, reply, msg.UserID, "项目不存在或列表已过期，请发送 /ag ls 刷新。")
		return
	}
	view.Mode = antigravityNavModeSessions
	view.ProjectID = projectID
	view.Page = 1
	h.setAntigravityNavView(msg.AccountID, routeUserID, view)
	h.renderAntigravityNavigation(ctx, msg, routeUserID, reply, view)
}

func (h *Handler) handleAntigravityPage(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, fields []string) {
	if len(fields) < 3 {
		sendPlatformText(ctx, reply, msg.UserID, "用法：/ag page <页码>")
		return
	}
	page, err := strconv.Atoi(fields[2])
	if err != nil || page < 1 {
		sendPlatformText(ctx, reply, msg.UserID, "页码无效，请发送 /ag ls 刷新。")
		return
	}
	view, ok := h.antigravityNavView(msg.AccountID, routeUserID)
	if !ok {
		sendPlatformText(ctx, reply, msg.UserID, "Antigravity 列表已过期，请发送 /ag ls 刷新。")
		return
	}
	choices, ok := h.feishuNavSnapshots.load(view.Token, h.antigravityNavScope(msg, routeUserID))
	if !ok {
		sendPlatformText(ctx, reply, msg.UserID, "Antigravity 列表已过期，请发送 /ag ls 刷新。")
		return
	}
	var total int
	if view.Mode == antigravityNavModeProjects {
		total = antigravityProjectCount(choices)
	} else {
		total = antigravitySessionCount(choices, view.ProjectID)
	}
	if total == 0 || page > (total+feishuNavigationPageSize-1)/feishuNavigationPageSize {
		sendPlatformText(ctx, reply, msg.UserID, "页码不存在，请发送 /ag ls 刷新。")
		return
	}
	view.Page = page
	h.setAntigravityNavView(msg.AccountID, routeUserID, view)
	h.renderAntigravityNavigation(ctx, msg, routeUserID, reply, view)
}

func (h *Handler) handleAntigravitySwitchCommand(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, fields []string) {
	if len(fields) < 3 {
		sendPlatformText(ctx, reply, msg.UserID, "用法：/ag switch <编号|会话ID>")
		return
	}
	if index, err := strconv.Atoi(fields[2]); err == nil {
		h.handleAntigravitySwitchIndex(ctx, msg, routeUserID, reply, index)
		return
	}
	h.bindAntigravityConversation(ctx, msg, routeUserID, reply, fields[2])
}

func (h *Handler) handleAntigravitySwitchIndex(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, index int) {
	view, ok := h.antigravityNavView(msg.AccountID, routeUserID)
	if !ok {
		sendPlatformText(ctx, reply, msg.UserID, "列表已过期，请发送 /ag ls 刷新。")
		return
	}
	choices, ok := h.feishuNavSnapshots.load(view.Token, h.antigravityNavScope(msg, routeUserID))
	if !ok {
		sendPlatformText(ctx, reply, msg.UserID, "列表已过期，请发送 /ag ls 刷新。")
		return
	}
	if view.Mode == antigravityNavModeProjects {
		items, _ := antigravityProjectChoices(choices, view.Page)
		projects := make([]platform.Choice, 0, len(items))
		for _, choice := range items {
			if strings.HasPrefix(choice.ID, "/ag project ") {
				projects = append(projects, choice)
			}
		}
		if index < 1 || index > len(projects) {
			sendPlatformText(ctx, reply, msg.UserID, "编号不存在，请发送 /ag ls 刷新。")
			return
		}
		projectID := strings.TrimPrefix(projects[index-1].ID, "/ag project ")
		view.Mode = antigravityNavModeSessions
		view.ProjectID = projectID
		view.Page = 1
		h.setAntigravityNavView(msg.AccountID, routeUserID, view)
		h.renderAntigravityNavigation(ctx, msg, routeUserID, reply, view)
		return
	}
	items, _ := antigravitySessionChoices(choices, view.ProjectID, view.Page)
	sessions := make([]platform.Choice, 0, len(items))
	for _, choice := range items {
		if strings.HasPrefix(choice.ID, "/ag switch ") {
			sessions = append(sessions, choice)
		}
	}
	if index < 1 || index > len(sessions) {
		sendPlatformText(ctx, reply, msg.UserID, "编号不存在，请发送 /ag ls 刷新。")
		return
	}
	conversationID := strings.TrimPrefix(sessions[index-1].ID, "/ag switch ")
	h.bindAntigravityConversation(ctx, msg, routeUserID, reply, conversationID)
}

func (h *Handler) bindAntigravityConversation(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, conversationID string) {
	client, _ := h.antigravityClientSnapshot()
	conversation, baseline, err := client.GetConversation(ctx, strings.TrimSpace(conversationID))
	if err != nil {
		sendPlatformText(ctx, reply, msg.UserID, "切换失败，保留原绑定："+err.Error())
		return
	}
	h.installAntigravityBinding(msg, routeUserID, reply, conversation, baseline)
	text := fmt.Sprintf("已切换并绑定 Antigravity 会话：%s", antigravityDisplayTitle(conversation))
	switch {
	case conversation.RunState == "RUN_STATE_RUNNING":
		text += "\n\n桌面端正在运行，已开始实时同步本轮进展。"
	case baseline.HasHistory && strings.TrimSpace(baseline.LatestReply) != "":
		text += "\n\n最近答复：\n" + baseline.LatestReply
	}
	sendPlatformText(ctx, reply, msg.UserID, text)
}

func (h *Handler) handleAntigravityNew(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, trimmed string) {
	body := strings.Join(strings.Fields(trimmed)[2:], " ")
	if strings.TrimSpace(body) == "" {
		sendPlatformText(ctx, reply, msg.UserID, "用法：/ag new [项目] <内容>")
		return
	}
	client, _ := h.antigravityClientSnapshot()
	conversations, err := client.ListConversations(ctx)
	if err != nil {
		sendPlatformText(ctx, reply, msg.UserID, "读取 Antigravity 项目失败："+err.Error())
		return
	}
	projects := antigravity.ProjectGroups(conversations)
	project, body, matched := matchAntigravityProject(projects, body)
	if matched && strings.TrimSpace(body) == "" {
		sendPlatformText(ctx, reply, msg.UserID, "用法：/ag new [项目] <内容>")
		return
	}
	if !matched {
		if binding, ok := h.antigravityBindingSnapshot(msg.AccountID, routeUserID); ok && strings.TrimSpace(binding.projectID) != "" {
			project = antigravity.Project{ID: binding.projectID, Name: binding.projectName}
			matched = true
		}
	}
	if !matched && len(projects) == 1 {
		project = projects[0]
		matched = true
	}
	if !matched {
		if len(projects) == 0 {
			sendPlatformText(ctx, reply, msg.UserID, "没有可用的 Antigravity 项目，请先在桌面端创建项目。")
			return
		}
		token := newAntigravityPendingToken()
		h.storeAntigravityPending(antigravityPendingNew{
			token: token, accountID: msg.AccountID, actorUserID: msg.UserID, routeUserID: routeUserID,
			body: body, expiresAt: time.Now().Add(antigravityPendingNewTTL),
		})
		choices := make([]platform.Choice, 0, len(projects))
		for index, item := range projects {
			label := fmt.Sprintf("%d. %s", index+1, antigravityProjectLabel(item))
			if strings.TrimSpace(item.Name) != "" {
				label = fmt.Sprintf("%d. %s", index+1, item.Name)
			}
			choices = append(choices, platform.Choice{ID: "/ag new-select " + token + " " + antigravityProjectToken(item.ID), Label: label})
		}
		choices = platformChoicesWithRoute(choices, platformMessageChoiceRoute(msg, routeUserID))
		if err := reply.AskChoices(ctx, "Antigravity\n请选择新会话所属项目。", choices); err != nil {
			sendPlatformText(ctx, reply, msg.UserID, antigravityChoiceFallback(choices))
		}
		return
	}
	h.createAntigravityConversation(ctx, msg, routeUserID, reply, project, body)
}

func (h *Handler) handleAntigravityNewSelect(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, fields []string) {
	if len(fields) < 4 {
		sendPlatformText(ctx, reply, msg.UserID, "项目选择无效，请重新发送 /ag new <内容>。")
		return
	}
	pending, ok := h.consumeAntigravityPending(fields[2], msg.AccountID, msg.UserID, routeUserID)
	if !ok {
		sendPlatformText(ctx, reply, msg.UserID, "选择已过期或不属于当前窗口，请重新发送 /ag new <内容>。")
		return
	}
	project := antigravity.Project{ID: antigravityProjectIDFromToken(fields[3])}
	h.createAntigravityConversation(ctx, msg, routeUserID, reply, project, pending.body)
}

func (h *Handler) createAntigravityConversation(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier, project antigravity.Project, body string) {
	if strings.TrimSpace(project.ID) == "" {
		sendPlatformText(ctx, reply, msg.UserID, "无法在未归属项目中新建会话，请先在 Antigravity 桌面端选择项目。")
		return
	}
	client, _ := h.antigravityClientSnapshot()
	title := antigravityTitleFromBody(body)
	conversation, baseline, err := client.CreateConversation(ctx, antigravity.CreateConversationRequest{
		ProjectID: project.ID, Title: title, Prompt: body,
	})
	if err != nil {
		sendPlatformText(ctx, reply, msg.UserID, "创建 Antigravity 会话失败："+err.Error())
		return
	}
	h.installAntigravityBinding(msg, routeUserID, reply, conversation, baseline)
	sendPlatformText(ctx, reply, msg.UserID, "已创建并绑定 Antigravity 会话："+antigravityDisplayTitle(conversation)+"\n首轮进展会实时同步到本窗口。")
}

func (h *Handler) handleAntigravityStatus(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier) {
	client, _ := h.antigravityClientSnapshot()
	sidecar := "正常"
	var healthErr error
	if client != nil {
		_, healthErr = client.Health(ctx)
	}
	if healthErr != nil {
		sidecar = "不可用（" + healthErr.Error() + "）"
	}
	binding, bound := h.antigravityBindingSnapshot(msg.AccountID, routeUserID)
	if !bound {
		sendPlatformText(ctx, reply, msg.UserID, fmt.Sprintf("Antigravity\n\nSidecar：%s\n当前窗口：未绑定", sidecar))
		return
	}
	conversation, _, err := client.GetConversation(ctx, binding.conversationID)
	if err != nil {
		sendPlatformText(ctx, reply, msg.UserID, fmt.Sprintf("Antigravity\n\nSidecar：%s\n项目：%s\n标题：%s\n会话 ID：%s\n官方状态：不可用（%v）\n飞书观察异常", sidecar, binding.projectName, binding.title, binding.conversationID, err))
		return
	}
	runState := strings.TrimSpace(conversation.RunState)
	desktop := "桌面空闲"
	switch runState {
	case "RUN_STATE_RUNNING":
		desktop = "桌面运行中"
	case "RUN_STATE_CANCELLING":
		desktop = "桌面正在取消"
	}
	observerState := "未观察"
	if binding.observer != nil && !binding.observer.closed() {
		observerState = "飞书观察中"
	}
	if healthErr != nil {
		observerState = "飞书观察异常"
	}
	sendPlatformText(ctx, reply, msg.UserID, fmt.Sprintf(
		"Antigravity\n\nSidecar：%s\n项目：%s\n标题：%s\n会话 ID：%s\n官方 runState：%s（%s）\n观察：%s",
		sidecar, binding.projectName, binding.title, binding.conversationID, runState, desktop, observerState,
	))
}

func (h *Handler) handleAntigravityRelease(ctx context.Context, msg platform.IncomingMessage, routeUserID string, reply platform.Replier) {
	if !h.hasAntigravityBinding(msg.AccountID, routeUserID) {
		sendPlatformText(ctx, reply, msg.UserID, "当前窗口没有绑定 Antigravity 会话。")
		return
	}
	h.clearAntigravityBinding(msg.AccountID, routeUserID)
	sendPlatformText(ctx, reply, msg.UserID, "已解除当前窗口的 Antigravity 绑定，桌面任务不受影响。")
}

func matchAntigravityProject(projects []antigravity.Project, body string) (antigravity.Project, string, bool) {
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return antigravity.Project{}, body, false
	}
	first := strings.ToLower(strings.TrimSpace(fields[0]))
	for _, project := range projects {
		if first == strings.ToLower(strings.TrimSpace(project.ID)) || first == strings.ToLower(strings.TrimSpace(project.Name)) ||
			first == antigravityProjectToken(project.ID) {
			return project, strings.Join(fields[1:], " "), true
		}
	}
	return antigravity.Project{}, body, false
}

func antigravityTitleFromBody(body string) string {
	body = strings.TrimSpace(strings.Split(strings.TrimSpace(body), "\n")[0])
	runes := []rune(body)
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return body
}

func newAntigravityPendingToken() string {
	return "@ag_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func antigravityProjectCount(choices []platform.Choice) int {
	seen := make(map[string]bool, len(choices))
	for _, choice := range choices {
		seen[antigravityProjectToken(choice.Metadata["project_id"])] = true
	}
	return len(seen)
}

func antigravitySessionCount(choices []platform.Choice, projectID string) int {
	count := 0
	for _, choice := range choices {
		if antigravityProjectToken(choice.Metadata["project_id"]) == antigravityProjectToken(antigravityProjectIDFromToken(projectID)) {
			count++
		}
	}
	return count
}

func antigravityHelpText() string {
	return `Antigravity（/ag，仅飞书私聊）

/ag ls 查看桌面会话
/ag <编号> 切换最近列表中的会话
/ag switch <编号|会话ID> 切换会话
/ag project <项目ID> 查看项目会话
/ag page <页码> 翻页
/ag new [项目] <内容> 新建会话并发送首句
/ag status 查看 Sidecar、会话与观察状态
/ag release 解除当前窗口绑定
/ag help 查看本帮助

绑定后，飞书和桌面端的后续回合都会实时同步；切走或解除后立即静音。`
}
