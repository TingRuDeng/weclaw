package feishu

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var feishuMarkdownImagePattern = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
var feishuFileMarkerPattern = regexp.MustCompile(`<file\s+[^>]*key="[^"]+"[^>]*/>`)

// stripFeishuResourceMarkers 移除富文本资源 key 占位，资源本体通过 Attachments 传递。
func stripFeishuResourceMarkers(text string) string {
	text = feishuMarkdownImagePattern.ReplaceAllString(text, "")
	text = feishuFileMarkerPattern.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// parseFeishuPostContent 兜底解析飞书富文本，覆盖 SDK normalize 无法识别的富文本变体。
func parseFeishuPostContent(content string) (string, []types.Resource) {
	var root any
	if err := json.Unmarshal([]byte(content), &root); err != nil {
		return "", nil
	}
	state := &postExtractState{seenImages: map[string]bool{}, seenFiles: map[string]bool{}}
	extractPostValue(root, state)
	return strings.TrimSpace(strings.Join(state.parts, "")), state.resources
}

type postExtractState struct {
	parts      []string
	resources  []types.Resource
	seenImages map[string]bool
	seenFiles  map[string]bool
}

func extractPostValue(value any, state *postExtractState) {
	switch v := value.(type) {
	case map[string]any:
		extractPostElement(v, state)
	case []any:
		for _, item := range v {
			extractPostValue(item, state)
		}
	}
}

func extractPostElement(element map[string]any, state *postExtractState) {
	switch stringField(element, "tag") {
	case "text", "md":
		appendPostText(state, stringField(element, "text"))
	case "a":
		appendPostLink(state, stringField(element, "text"), stringField(element, "href"))
	case "at":
		appendPostAt(state, element)
	case "img", "image":
		appendPostImage(state, stringField(element, "image_key"))
	case "media":
		appendPostMedia(state, element)
	case "code_block":
		appendPostText(state, stringField(element, "text"))
	default:
		extractPostChildren(element, state)
	}
}

func extractPostChildren(element map[string]any, state *postExtractState) {
	for key, child := range element {
		if key == "tag" || key == "title" {
			continue
		}
		extractPostValue(child, state)
	}
}

func appendPostText(state *postExtractState, text string) {
	if text != "" {
		state.parts = append(state.parts, text)
	}
}

func appendPostLink(state *postExtractState, label string, href string) {
	if label == "" {
		label = href
	}
	if href == "" {
		appendPostText(state, label)
		return
	}
	state.parts = append(state.parts, fmt.Sprintf("[%s](%s)", label, href))
}

func appendPostAt(state *postExtractState, element map[string]any) {
	name := stringField(element, "user_name")
	if name == "" {
		name = stringField(element, "user_id")
	}
	if name != "" {
		state.parts = append(state.parts, "@"+name)
	}
}

func appendPostImage(state *postExtractState, imageKey string) {
	if imageKey == "" || state.seenImages[imageKey] {
		return
	}
	state.seenImages[imageKey] = true
	state.resources = append(state.resources, types.Resource{Type: "image", FileKey: imageKey})
}

func appendPostMedia(state *postExtractState, element map[string]any) {
	fileKey := stringField(element, "file_key")
	if fileKey != "" && !state.seenFiles[fileKey] {
		state.seenFiles[fileKey] = true
		state.resources = append(state.resources, types.Resource{Type: "file", FileKey: fileKey})
	}
	appendPostImage(state, stringField(element, "image_key"))
}

func mergeFeishuResources(base []types.Resource, extra []types.Resource) []types.Resource {
	seen := make(map[string]bool, len(base)+len(extra))
	merged := make([]types.Resource, 0, len(base)+len(extra))
	for _, resource := range append(base, extra...) {
		key := resource.Type + "\x00" + resource.FileKey
		if resource.FileKey == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, resource)
	}
	return merged
}

func rawMessageContent(event *larkim.P2MessageReceiveV1) string {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.Content == nil {
		return ""
	}
	return *event.Event.Message.Content
}

func stringField(data map[string]any, key string) string {
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
