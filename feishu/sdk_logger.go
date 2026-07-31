package feishu

import "context"

// silentFeishuSDKLogger prevents SDK-controlled URLs and payloads from entering
// the daemon log. Adapter lifecycle errors still propagate through Run.
type silentFeishuSDKLogger struct{}

func (silentFeishuSDKLogger) Debug(context.Context, ...interface{}) {}
func (silentFeishuSDKLogger) Info(context.Context, ...interface{})  {}
func (silentFeishuSDKLogger) Warn(context.Context, ...interface{})  {}
func (silentFeishuSDKLogger) Error(context.Context, ...interface{}) {}
