package messaging

import (
	"strings"
	"testing"
)

func TestDetectChoicesFindsNumberedOptions(t *testing.T) {
	reply := "请选择一个方案：\n1. 继续执行\n2. 暂停任务\n3. 查看状态"

	detected, ok := detectChoices(reply)

	if !ok {
		t.Fatal("detectChoices ok=false, want true")
	}
	if detected.Prompt != "请选择一个方案：" {
		t.Fatalf("prompt=%q, want original prompt", detected.Prompt)
	}
	if len(detected.Choices) != 3 || detected.Choices[0].ID != "1" || detected.Choices[0].Label != "继续执行" {
		t.Fatalf("choices=%#v, want numbered choices", detected.Choices)
	}
	if detected.CleanText != "请选择一个方案：" {
		t.Fatalf("clean=%q, want prompt only", detected.CleanText)
	}
}

func TestDetectChoicesIgnoresPlainNumberedList(t *testing.T) {
	reply := "处理结果：\n1. 已读取文件\n2. 已运行测试"

	_, ok := detectChoices(reply)

	if ok {
		t.Fatal("plain numbered list should not be detected as choices")
	}
}

func TestDetectChoicesIgnoresPlanListMentioningSelection(t *testing.T) {
	reply := strings.Join([]string{
		"本轮未联网检索，未使用 subagent。",
		"",
		"1. 流水页切到“消费”时，顶部显示本月摘要。",
		"2. 摘要卡支持用户选择左右切换。",
		"3. 点击摘要卡进入过滤后的流水列表。",
		"4. 先加 `MonthlyConsumptionSummary` 纯计算模型和单测。",
		"5. 在“流水-消费”顶部展示本月摘要卡。",
		"6. 点摘要卡后复用现有筛选能力，进入当月消费列表。",
		"7. 补测试覆盖：消费、退款、多币种、跨月、空数据。",
	}, "\n")

	_, ok := detectChoices(reply)

	if ok {
		t.Fatal("ordinary implementation plan should not be detected as choices")
	}
}

func TestDetectChoicesSupportsChineseSeparators(t *testing.T) {
	reply := "回复编号继续。\n1）接受\n2）拒绝"

	detected, ok := detectChoices(reply)

	if !ok {
		t.Fatal("detectChoices ok=false, want true")
	}
	if len(detected.Choices) != 2 || detected.Choices[1].Label != "拒绝" {
		t.Fatalf("choices=%#v, want Chinese separator choices", detected.Choices)
	}
}
