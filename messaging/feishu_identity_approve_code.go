package messaging

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type feishuIdentityApproveCodeOptions struct {
	Code        string
	BotRef      string
	Admin       bool
	DisplayName string
}

// FeishuIdentityApproveCodeRequest 描述一次基于授权码的飞书身份授权请求。
type FeishuIdentityApproveCodeRequest struct {
	Code        string
	BotRef      string
	Admin       bool
	DisplayName string
	FilePath    string
}

func (h *Handler) handleFeishuIdentityApproveCode(args []string) string {
	opts, err := parseFeishuIdentityApproveCodeOptions(args)
	if err != nil {
		return err.Error()
	}
	result, err := approveFeishuIdentityByCode(h.ensureFeishuIdentities(), opts)
	if err != nil {
		return err.Error()
	}
	return RenderFeishuIdentityApproval(result)
}

func parseFeishuIdentityApproveCodeOptions(args []string) (feishuIdentityApproveCodeOptions, error) {
	if len(args) == 0 {
		return feishuIdentityApproveCodeOptions{}, fmt.Errorf("用法: /feishu users approve-code <授权码> [--bot <name|app_id>] [--admin] [--name <显示名>]")
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
	case "--admin":
		opts.Admin = true
		return opts, 0, nil
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
		Admin:       req.Admin,
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
		Selector: record.Key,
		BotRef:   opts.BotRef,
		Admin:    opts.Admin,
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
