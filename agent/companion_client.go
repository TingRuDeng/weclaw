package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

// CompanionRequest 是本地可见 Companion 收到的一次微信输入请求。
type CompanionRequest struct {
	ConversationID string
	Text           string
}

type CompanionRequestHandler interface {
	HandleCompanionRequest(ctx context.Context, req CompanionRequest, progress func(string)) (string, error)
}

type CompanionHandlerFunc func(ctx context.Context, req CompanionRequest, progress func(string)) (string, error)

func (f CompanionHandlerFunc) HandleCompanionRequest(ctx context.Context, req CompanionRequest, progress func(string)) (string, error) {
	return f(ctx, req, progress)
}

// RunCompanionClient 连接后台 WeClaw，并把请求交给本地可见运行时处理。
func RunCompanionClient(ctx context.Context, endpoint CompanionEndpoint, handler CompanionRequestHandler) error {
	if handler == nil {
		return errors.New("Companion 处理器为空")
	}
	conn, err := net.Dial("tcp", endpoint.Address())
	if err != nil {
		return fmt.Errorf("连接 Companion 入口失败: %w", err)
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)
	if err := sendCompanionHello(encoder, endpoint.Token); err != nil {
		return err
	}
	for {
		var message companionEnvelope
		if err := decoder.Decode(&message); err != nil {
			return fmt.Errorf("读取 Companion 请求失败: %w", err)
		}
		if message.Type != companionMessageRequest || message.Request == nil {
			continue
		}
		handleCompanionRequest(ctx, encoder, handler, message)
	}
}

func sendCompanionHello(encoder *json.Encoder, token string) error {
	if err := encoder.Encode(companionEnvelope{
		Type:  companionMessageHello,
		Token: token,
		PID:   os.Getpid(),
	}); err != nil {
		return fmt.Errorf("发送 Companion 握手失败: %w", err)
	}
	return nil
}

func handleCompanionRequest(ctx context.Context, encoder *json.Encoder, handler CompanionRequestHandler, message companionEnvelope) {
	progress := func(text string) {
		_ = encoder.Encode(companionEnvelope{
			Type:  companionMessageEvent,
			ID:    message.ID,
			Event: &companionEvent{Name: "progress", Text: text},
		})
	}
	reply, err := handler.HandleCompanionRequest(ctx, CompanionRequest{
		ConversationID: message.Request.ConversationID,
		Text:           message.Request.Text,
	}, progress)
	response := companionResponse{OK: true, Text: reply}
	if err != nil {
		response = companionResponse{OK: false, Error: err.Error()}
	}
	_ = encoder.Encode(companionEnvelope{
		Type:     companionMessageResponse,
		ID:       message.ID,
		Response: &response,
	})
}
