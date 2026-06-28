package messaging

import "testing"

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
