package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	claudeQuotaTimeout       = 15 * time.Second
	claudeQuotaInitializeID  = "weclaw-claude-quota-initialize"
	claudeQuotaReadRequestID = "weclaw-claude-quota-read"
)

var claudeQuotaCommandArgs = []string{
	"--output-format", "stream-json",
	"--verbose",
	"--input-format", "stream-json",
	"--no-session-persistence",
	"--permission-mode", "dontAsk",
	"--setting-sources", "",
	"--strict-mcp-config",
}

// ReadClaudeQuota 优先复用 Claude Code OAuth 登录查询账号额度，并回退到原生控制协议。
func (a *ACPAgent) ReadClaudeQuota(ctx context.Context) (ClaudeQuota, error) {
	a.claudeQuotaMu.Lock()
	defer a.claudeQuotaMu.Unlock()

	var oauthErr error
	oauthCtx, cancelOAuth := context.WithTimeout(ctx, claudeQuotaTimeout)
	readToken := a.claudeQuotaOAuthToken
	if readToken == nil {
		readToken = a.readClaudeOAuthAccessToken
	}
	token, err := readToken(oauthCtx)
	if err != nil {
		oauthErr = err
	}
	if token != "" {
		query := a.claudeQuotaOAuthQuery
		if query == nil {
			query = queryClaudeOAuthQuotaDefault
		}
		quota, queryErr := query(oauthCtx, token)
		token = ""
		if queryErr == nil {
			cancelOAuth()
			return quota, nil
		}
		oauthErr = queryErr
	}
	cancelOAuth()

	localCommand := strings.TrimSpace(a.localCommand)
	if localCommand == "" {
		if oauthErr != nil {
			return ClaudeQuota{}, fmt.Errorf("query Claude quota from local OAuth login: %w", oauthErr)
		}
		return ClaudeQuota{}, fmt.Errorf("未找到可读取的 Claude OAuth 登录，且当前 Agent 未配置 local_command")
	}

	commandCtx, cancelCommand := context.WithTimeout(ctx, claudeQuotaTimeout)
	defer cancelCommand()
	quota, commandErr := a.readClaudeQuotaFromCommand(commandCtx, localCommand)
	if commandErr == nil {
		return quota, nil
	}
	if oauthErr != nil {
		return ClaudeQuota{}, errors.Join(
			fmt.Errorf("query Claude quota from local OAuth login: %w", oauthErr),
			fmt.Errorf("query Claude quota through native command: %w", commandErr),
		)
	}
	return ClaudeQuota{}, commandErr
}

// readClaudeQuotaFromCommand 通过 Claude Code 原生控制协议查询账号额度，不发送模型提示词。
func (a *ACPAgent) readClaudeQuotaFromCommand(queryCtx context.Context, localCommand string) (ClaudeQuota, error) {
	cmd, err := a.newClaudeQuotaCommand(queryCtx, localCommand)
	if err != nil {
		return ClaudeQuota{}, err
	}
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ClaudeQuota{}, fmt.Errorf("create Claude quota stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return ClaudeQuota{}, fmt.Errorf("create Claude quota stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return ClaudeQuota{}, fmt.Errorf("start Claude quota command %s: %w", localCommand, err)
	}
	defer stopACPProcess(stdin, cmd)

	quota, err := queryClaudeQuota(stdin, stdout)
	if err == nil {
		return quota, nil
	}
	if queryErr := queryCtx.Err(); queryErr != nil {
		err = queryErr
	}
	return ClaudeQuota{}, fmt.Errorf("query Claude quota: %w", err)
}

// newClaudeQuotaCommand 复用 Agent 的用户隔离和环境，但强制使用原生 CLI 入口语义。
func (a *ACPAgent) newClaudeQuotaCommand(ctx context.Context, localCommand string) (*exec.Cmd, error) {
	command, args := a.runAs.wrapCommand(localCommand, claudeQuotaCommandArgs)
	cmd := exec.CommandContext(ctx, command, args...)
	configureACPProcess(cmd)
	a.mu.Lock()
	cmd.Dir = a.cwd
	extraEnv := make(map[string]string, len(a.env)+1)
	for key, value := range a.env {
		extraEnv[key] = value
	}
	a.mu.Unlock()
	extraEnv["CLAUDE_CODE_ENTRYPOINT"] = "cli"
	env, err := mergeEnv(os.Environ(), extraEnv)
	if err != nil {
		return nil, fmt.Errorf("build Claude quota env: %w", err)
	}
	cmd.Env = env
	return cmd, nil
}

// queryClaudeQuota 执行最小控制协议：初始化后只读取一次结构化 usage。
func queryClaudeQuota(stdin io.Writer, stdout io.Reader) (ClaudeQuota, error) {
	scanner := newACPScanner(stdout)
	if err := writeClaudeControlRequest(stdin, claudeQuotaInitializeID, "initialize"); err != nil {
		return ClaudeQuota{}, err
	}
	if _, err := readClaudeControlResponse(scanner, claudeQuotaInitializeID); err != nil {
		return ClaudeQuota{}, fmt.Errorf("initialize Claude quota control: %w", err)
	}
	if err := writeClaudeControlRequest(stdin, claudeQuotaReadRequestID, "get_usage"); err != nil {
		return ClaudeQuota{}, err
	}
	payload, err := readClaudeControlResponse(scanner, claudeQuotaReadRequestID)
	if err != nil {
		return ClaudeQuota{}, fmt.Errorf("read Claude usage: %w", err)
	}
	return parseClaudeQuota(payload)
}

func writeClaudeControlRequest(writer io.Writer, requestID string, subtype string) error {
	request := claudeControlRequest{
		Type:      "control_request",
		RequestID: requestID,
		Request:   claudeControlRequestBody{Subtype: subtype},
	}
	if err := json.NewEncoder(writer).Encode(request); err != nil {
		return fmt.Errorf("write Claude %s control request: %w", subtype, err)
	}
	return nil
}

func readClaudeControlResponse(scanner interface {
	Scan() bool
	Bytes() []byte
	Err() error
}, requestID string) (json.RawMessage, error) {
	for scanner.Scan() {
		var message claudeControlMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil || message.Type != "control_response" {
			continue
		}
		if message.Response.RequestID != requestID {
			continue
		}
		switch message.Response.Subtype {
		case "success":
			return message.Response.Payload, nil
		case "error":
			detail := strings.TrimSpace(message.Response.Error)
			if strings.Contains(strings.ToLower(detail), "not supported") {
				return nil, fmt.Errorf("Claude control request is not supported")
			}
			return nil, fmt.Errorf("Claude control request failed")
		default:
			return nil, fmt.Errorf("unexpected control response subtype %q", message.Response.Subtype)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Claude control response: %w", err)
	}
	return nil, fmt.Errorf("Claude quota command exited before response %s", requestID)
}

// parseClaudeQuota 只提取账号额度相关字段；上游接口目前仍标记为 experimental。
func parseClaudeQuota(data json.RawMessage) (ClaudeQuota, error) {
	var payload claudeUsagePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ClaudeQuota{}, fmt.Errorf("parse Claude usage result: %w", err)
	}
	quota := ClaudeQuota{
		SubscriptionType:    strings.TrimSpace(payload.SubscriptionType),
		RateLimitsAvailable: payload.RateLimitsAvailable,
	}
	if payload.RateLimits == nil {
		return quota, nil
	}
	quota.Limits = appendClaudeQuotaLimit(quota.Limits, "five_hour", "", payload.RateLimits.FiveHour)
	quota.Limits = appendClaudeQuotaLimit(quota.Limits, "seven_day", "", payload.RateLimits.SevenDay)
	quota.Limits = appendClaudeQuotaLimit(quota.Limits, "seven_day_oauth_apps", "", payload.RateLimits.SevenDayOAuthApps)
	quota.Limits = appendClaudeQuotaLimit(quota.Limits, "seven_day_opus", "", payload.RateLimits.SevenDayOpus)
	quota.Limits = appendClaudeQuotaLimit(quota.Limits, "seven_day_sonnet", "", payload.RateLimits.SevenDaySonnet)
	for _, scoped := range payload.RateLimits.ModelScoped {
		window := claudeUsageWindow{Utilization: scoped.Utilization, ResetsAt: scoped.ResetsAt}
		quota.Limits = appendClaudeQuotaLimit(quota.Limits, "model_scoped", scoped.DisplayName, &window)
	}
	if extra := payload.RateLimits.ExtraUsage; extra != nil {
		quota.ExtraUsage = &ClaudeExtraUsage{
			Enabled:      extra.Enabled,
			UsedPercent:  extra.Utilization,
			MonthlyLimit: extra.MonthlyLimit,
			UsedCredits:  extra.UsedCredits,
			Currency:     strings.TrimSpace(extra.Currency),
		}
	}
	return quota, nil
}

func appendClaudeQuotaLimit(limits []ClaudeRateLimit, id string, name string, window *claudeUsageWindow) []ClaudeRateLimit {
	if window == nil {
		return limits
	}
	return append(limits, ClaudeRateLimit{
		ID: id, Name: strings.TrimSpace(name), UsedPercent: window.Utilization, ResetsAt: strings.TrimSpace(window.ResetsAt),
	})
}

type claudeControlRequest struct {
	Type      string                   `json:"type"`
	RequestID string                   `json:"request_id"`
	Request   claudeControlRequestBody `json:"request"`
}

type claudeControlRequestBody struct {
	Subtype string `json:"subtype"`
}

type claudeControlMessage struct {
	Type     string                `json:"type"`
	Response claudeControlResponse `json:"response"`
}

type claudeControlResponse struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Payload   json.RawMessage `json:"response"`
	Error     string          `json:"error"`
}

type claudeUsagePayload struct {
	SubscriptionType    string                 `json:"subscription_type"`
	RateLimitsAvailable bool                   `json:"rate_limits_available"`
	RateLimits          *claudeUsageRateLimits `json:"rate_limits"`
}

type claudeUsageRateLimits struct {
	FiveHour          *claudeUsageWindow       `json:"five_hour"`
	SevenDay          *claudeUsageWindow       `json:"seven_day"`
	SevenDayOAuthApps *claudeUsageWindow       `json:"seven_day_oauth_apps"`
	SevenDayOpus      *claudeUsageWindow       `json:"seven_day_opus"`
	SevenDaySonnet    *claudeUsageWindow       `json:"seven_day_sonnet"`
	ModelScoped       []claudeUsageModelWindow `json:"model_scoped"`
	ExtraUsage        *claudeUsageExtraUsage   `json:"extra_usage"`
}

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type claudeUsageModelWindow struct {
	DisplayName string   `json:"display_name"`
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type claudeUsageExtraUsage struct {
	Enabled      bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
	Currency     string   `json:"currency"`
}
