package messaging

import (
	"context"

	"github.com/google/uuid"
)

const defaultTextReplyChunkRunes = 1800
const weclawClientIDPrefix = "weclaw:"

type textReplyChunkLimitKey struct{}

// NewClientID 生成一次回复使用的稳定关联 ID。
func NewClientID() string {
	return weclawClientIDPrefix + uuid.New().String()
}

// textReplyChunkLimit 返回当前上下文的分段上限，默认使用微信友好的保守长度。
func textReplyChunkLimit(ctx context.Context) int {
	if limit, ok := ctx.Value(textReplyChunkLimitKey{}).(int); ok && limit > 0 {
		return limit
	}
	return defaultTextReplyChunkRunes
}

// withTextReplyChunkLimit 在测试中缩小分段上限，便于验证长文本拆分。
func withTextReplyChunkLimit(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, textReplyChunkLimitKey{}, limit)
}
