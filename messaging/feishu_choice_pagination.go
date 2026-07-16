package messaging

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

const feishuNavigationPageSize = 7

type feishuChoicePage struct {
	Number     int
	TotalPages int
	TotalItems int
}

type feishuNavigationPageRequest struct {
	Kind string
	Page int
}

func paginateFeishuChoices(choices []platform.Choice, requestedPage int) ([]platform.Choice, feishuChoicePage) {
	total := len(choices)
	totalPages := (total + feishuNavigationPageSize - 1) / feishuNavigationPageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if requestedPage < 1 {
		requestedPage = 1
	}
	if requestedPage > totalPages {
		requestedPage = totalPages
	}
	start := (requestedPage - 1) * feishuNavigationPageSize
	end := start + feishuNavigationPageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	return choices[start:end], feishuChoicePage{Number: requestedPage, TotalPages: totalPages, TotalItems: total}
}

func appendFeishuPageNavigation(choices []platform.Choice, command string, kind string, page feishuChoicePage) []platform.Choice {
	if page.TotalPages <= 1 {
		return choices
	}
	if page.Number > 1 {
		choices = append(choices, feishuNavigationChoice(
			fmt.Sprintf("%s page %s %d", command, kind, page.Number-1), "← 上一页",
		))
	}
	if page.Number < page.TotalPages {
		choices = append(choices, feishuNavigationChoice(
			fmt.Sprintf("%s page %s %d", command, kind, page.Number+1), "下一页 →",
		))
	}
	return choices
}

func feishuNavigationChoice(id string, label string) platform.Choice {
	return platform.Choice{ID: id, Label: label, Metadata: map[string]string{
		platform.ChoiceMetadataButtonType: platform.ChoiceButtonTypeDefault,
		platform.ChoiceMetadataSection:    platform.ChoiceSectionNavigation,
	}}
}

func feishuPaginatedPrompt(prompt string, page feishuChoicePage) string {
	return fmt.Sprintf("%s\n\n第 %d/%d 页 · 共 %d 个", strings.TrimSpace(prompt), page.Number, page.TotalPages, page.TotalItems)
}

func parseFeishuNavigationPage(fields []string, command string) (feishuNavigationPageRequest, bool) {
	if len(fields) != 4 || fields[0] != command || fields[1] != "page" {
		return feishuNavigationPageRequest{}, false
	}
	kind := strings.TrimSpace(fields[2])
	if kind != "workspaces" && kind != "sessions" {
		return feishuNavigationPageRequest{}, false
	}
	page, err := strconv.Atoi(fields[3])
	if err != nil || page < 1 {
		return feishuNavigationPageRequest{}, false
	}
	return feishuNavigationPageRequest{Kind: kind, Page: page}, true
}
