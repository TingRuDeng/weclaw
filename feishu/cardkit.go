package feishu

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
)

type cardKitClient interface {
	CreateCard(ctx context.Context, cardJSON string) (string, error)
	SetStreaming(ctx context.Context, cardID string, enabled bool, sequence int) error
	StreamContent(ctx context.Context, cardID string, elementID string, content string, sequence int) error
	UpdateCard(ctx context.Context, cardID string, cardJSON string, sequence int) error
	DestroyCard(ctx context.Context, cardID string) error
}

type sdkCardKitClient struct {
	client *lark.Client
	appID  string
}

// newSDKCardKitClient 创建飞书 CardKit SDK 适配器。
func newSDKCardKitClient(client *lark.Client, appID string) cardKitClient {
	return &sdkCardKitClient{client: client, appID: appID}
}

// CreateCard 创建 CardKit 卡片实例并返回 card_id。
func (c *sdkCardKitClient) CreateCard(ctx context.Context, cardJSON string) (string, error) {
	req := larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().
			Type("card_json").
			Data(cardJSON).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.Card.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() || resp.Data == nil || resp.Data.CardId == nil {
		return "", formatFeishuAPIError(c.appID, resp.Code, resp.Msg)
	}
	return *resp.Data.CardId, nil
}

// SetStreaming 更新卡片 streaming_mode。
func (c *sdkCardKitClient) SetStreaming(ctx context.Context, cardID string, enabled bool, sequence int) error {
	settings, err := json.Marshal(map[string]any{"config": map[string]bool{"streaming_mode": enabled}})
	if err != nil {
		return err
	}
	req := larkcardkit.NewSettingsCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewSettingsCardReqBodyBuilder().
			Uuid(uuid.NewString()).
			Sequence(sequence).
			Settings(string(settings)).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.Card.Settings(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return formatFeishuAPIError(c.appID, resp.Code, resp.Msg)
	}
	return nil
}

// StreamContent 更新指定文本组件内容，触发飞书打字机效果。
func (c *sdkCardKitClient) StreamContent(ctx context.Context, cardID string, elementID string, content string, sequence int) error {
	req := larkcardkit.NewContentCardElementReqBuilder().
		CardId(cardID).
		ElementId(elementID).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().
			Uuid(uuid.NewString()).
			Sequence(sequence).
			Content(content).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.CardElement.Content(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return formatFeishuAPIError(c.appID, resp.Code, resp.Msg)
	}
	return nil
}

// UpdateCard 全量更新卡片内容。
func (c *sdkCardKitClient) UpdateCard(ctx context.Context, cardID string, cardJSON string, sequence int) error {
	req := larkcardkit.NewUpdateCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewUpdateCardReqBodyBuilder().
			Uuid(uuid.NewString()).
			Sequence(sequence).
			Card(larkcardkit.NewCardBuilder().Type("card_json").Data(cardJSON).Build()).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.Card.Update(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return formatFeishuAPIError(c.appID, resp.Code, resp.Msg)
	}
	return nil
}

// DestroyCard 当前 SDK 未暴露整卡删除接口，卡片实例依赖飞书侧 TTL 自动回收。
func (c *sdkCardKitClient) DestroyCard(ctx context.Context, cardID string) error {
	if cardID == "" {
		return fmt.Errorf("card_id is required")
	}
	return nil
}
