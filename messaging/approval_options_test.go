package messaging

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
)

// TestApprovalChoiceLabelDistinguishesClaudeAllowScopes 验证 Claude 的持久授权和单次授权不会显示为相同文案。
func TestApprovalChoiceLabelDistinguishesClaudeAllowScopes(t *testing.T) {
	tests := []struct {
		name   string
		option agent.ApprovalOption
		want   string
	}{
		{
			name:   "始终允许保留具体范围",
			option: agent.ApprovalOption{ID: "allow_always", Name: "Always Allow Bash(npm test:*)", Kind: "allow"},
			want:   "始终允许：Bash(npm test:*)",
		},
		{
			name:   "单次允许明确标注仅本次",
			option: agent.ApprovalOption{ID: "allow", Name: "Allow", Kind: "allow"},
			want:   "仅本次允许",
		},
		{
			name:   "拒绝保持简洁",
			option: agent.ApprovalOption{ID: "reject", Name: "Reject", Kind: "deny"},
			want:   "拒绝",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalChoiceLabel(tt.option); got != tt.want {
				t.Fatalf("approvalChoiceLabel()=%q，期望 %q", got, tt.want)
			}
		})
	}
}
