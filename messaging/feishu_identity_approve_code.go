package messaging

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

type feishuIdentityApproveCodeOptions struct {
	Code        string
	BotRef      string
	DisplayName string
	AccountID   string
}

// FeishuIdentityApproveCodeRequest 描述一次基于授权码的飞书身份授权请求。
type FeishuIdentityApproveCodeRequest struct {
	Code        string
	BotRef      string
	DisplayName string
	FilePath    string
}

func (h *Handler) handleFeishuIdentityApproveCode(msg platform.IncomingMessage, args []string) string {
	opts, err := parseFeishuIdentityApproveCodeOptions(args)
	if err != nil {
		return err.Error()
	}
	opts.BotRef, err = remoteFeishuIdentityBotRef(msg.AccountID, opts.BotRef)
	if err != nil {
		return err.Error()
	}
	opts.AccountID = strings.TrimSpace(msg.AccountID)
	h.feishuIdentityMutationMu.Lock()
	defer h.feishuIdentityMutationMu.Unlock()
	result, err := approveFeishuIdentityByCode(h.ensureFeishuIdentities(), opts)
	if err != nil {
		return err.Error()
	}
	h.refreshFeishuAccountAccess(opts.AccountID, result.allowedUsers)
	h.auditFeishuIdentityMutation(msg, "feishu_identity_approve", result.Identity, result.Bots)
	return RenderFeishuIdentityApproval(result)
}

func parseFeishuIdentityApproveCodeOptions(args []string) (feishuIdentityApproveCodeOptions, error) {
	if len(args) == 0 {
		return feishuIdentityApproveCodeOptions{}, fmt.Errorf("用法: /feishu users approve-code <授权码> [--bot <name|app_id>] [--name <显示名>]")
	}
	opts := feishuIdentityApproveCodeOptions{Code: strings.TrimSpace(args[0])}
	for i := 1; i < len(args); i++ {
		next, skip, err := applyFeishuApproveCodeFlag(opts, args, i)
		if err != nil {
			return opts, err
		}
		opts = next
		i += skip
	}
	return opts, nil
}

func applyFeishuApproveCodeFlag(opts feishuIdentityApproveCodeOptions, args []string, index int) (feishuIdentityApproveCodeOptions, int, error) {
	switch args[index] {
	case "--bot":
		value, err := feishuApproveCodeFlagValue(args, index, "--bot 需要指定机器人 name 或 app_id")
		opts.BotRef = value
		return opts, 1, err
	case "--name":
		value, err := feishuApproveCodeFlagValue(args, index, "--name 需要指定显示名")
		opts.DisplayName = value
		return opts, 1, err
	default:
		return opts, 0, fmt.Errorf("未知参数: %s", args[index])
	}
}

func feishuApproveCodeFlagValue(args []string, index int, message string) (string, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
		return "", errors.New(message)
	}
	return strings.TrimSpace(args[index+1]), nil
}

// ApproveFeishuIdentityByCode 使用短期授权码确认飞书用户，并写入配置。
func ApproveFeishuIdentityByCode(req FeishuIdentityApproveCodeRequest) (FeishuIdentityApproveResult, error) {
	store := newFeishuIdentityStore()
	store.SetFilePath(firstNonBlank(req.FilePath, DefaultFeishuIdentityFile()))
	if err := store.LoadError(); err != nil {
		return FeishuIdentityApproveResult{}, err
	}
	opts := feishuIdentityApproveCodeOptions{
		Code:        strings.TrimSpace(req.Code),
		BotRef:      strings.TrimSpace(req.BotRef),
		DisplayName: strings.TrimSpace(req.DisplayName),
	}
	return approveFeishuIdentityByCode(store, opts)
}

func approveFeishuIdentityByCode(store *feishuIdentityStore, opts feishuIdentityApproveCodeOptions) (FeishuIdentityApproveResult, error) {
	record, ok := store.FindByAuthCode(opts.Code, time.Now().UTC())
	if !ok {
		return FeishuIdentityApproveResult{}, fmt.Errorf("授权码无效或已过期。")
	}
	if opts.DisplayName != "" {
		record = renameFeishuRecordForApproval(store, record, opts.DisplayName)
	}
	result, err := approveFeishuIdentity(store, feishuIdentityApproveOptions{
		Selector:  record.Key,
		BotRef:    opts.BotRef,
		AccountID: opts.AccountID,
	})
	if err != nil {
		return FeishuIdentityApproveResult{}, err
	}
	result.DisplayName = record.DisplayName
	return result, nil
}

func renameFeishuRecordForApproval(store *feishuIdentityStore, record feishuIdentityRecord, displayName string) feishuIdentityRecord {
	if renamed, ok := store.Rename(record.Key, displayName); ok {
		return renamed
	}
	return record
}
