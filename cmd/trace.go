package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/observability"
	"github.com/spf13/cobra"
)

type traceCommandOptions struct {
	traceID   string
	messageID string
	taskID    string
	threadID  string
	turnID    string
	stage     string
	since     string
	limit     int
	json      bool
}

var traceOptions traceCommandOptions

var traceCmd = &cobra.Command{
	Use:   "trace [trace-id]",
	Short: "查询消息、任务和 Agent 协议 Trace",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runTrace,
}

func init() {
	rootCmd.AddCommand(traceCmd)
	traceCmd.Flags().StringVar(&traceOptions.traceID, "trace-id", "", "按 trace ID 查询")
	traceCmd.Flags().StringVar(&traceOptions.messageID, "message-id", "", "按平台消息 ID 查询")
	traceCmd.Flags().StringVar(&traceOptions.taskID, "task-id", "", "按任务 ID 查询")
	traceCmd.Flags().StringVar(&traceOptions.threadID, "thread-id", "", "按 Codex thread ID 查询")
	traceCmd.Flags().StringVar(&traceOptions.turnID, "turn-id", "", "按 Codex turn ID 查询")
	traceCmd.Flags().StringVar(&traceOptions.stage, "stage", "", "按精确阶段名查询")
	traceCmd.Flags().StringVar(&traceOptions.since, "since", "", "只显示该 RFC3339 时间之后的事件")
	traceCmd.Flags().IntVar(&traceOptions.limit, "limit", 100, "最多返回 1-1000 条最近事件")
	traceCmd.Flags().BoolVar(&traceOptions.json, "json", false, "输出 JSON")
}

func runTrace(cmd *cobra.Command, args []string) error {
	query, err := traceQueryFromOptions(args, traceOptions)
	if err != nil {
		return err
	}
	cfg, online, err := loadCodexAccountRuntime()
	if err != nil {
		return err
	}
	var page observability.Page
	if online {
		page, err = queryTraceAPI(cmd.Context(), cfg, query)
		if err != nil {
			return fmt.Errorf("WeClaw 服务正在运行，但本机 Trace API 不可用: %w", err)
		}
	} else {
		page, err = observability.QueryPath(cmd.Context(), observability.DefaultPath(), query)
		if err != nil {
			return fmt.Errorf("读取本地 Trace: %w", err)
		}
	}
	return writeTracePage(cmd, page, traceOptions.json)
}

func traceQueryFromOptions(args []string, options traceCommandOptions) (observability.Query, error) {
	if len(args) == 1 {
		if strings.TrimSpace(options.traceID) != "" && strings.TrimSpace(options.traceID) != strings.TrimSpace(args[0]) {
			return observability.Query{}, fmt.Errorf("位置参数和 --trace-id 不能指定不同值")
		}
		options.traceID = args[0]
	}
	if options.limit <= 0 || options.limit > 1000 {
		return observability.Query{}, fmt.Errorf("--limit 必须在 1 到 1000 之间")
	}
	query := observability.Query{
		TraceID: options.traceID, MessageID: options.messageID, TaskID: options.taskID,
		ThreadID: options.threadID, TurnID: options.turnID, Stage: options.stage, Limit: options.limit,
	}
	if raw := strings.TrimSpace(options.since); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return observability.Query{}, fmt.Errorf("--since 必须是 RFC3339 时间")
		}
		query.Since = since
	}
	return query, nil
}

func queryTraceAPI(ctx context.Context, cfg *config.Config, query observability.Query) (observability.Page, error) {
	values := url.Values{}
	setTraceQueryValue(values, "trace_id", query.TraceID)
	setTraceQueryValue(values, "message_id", query.MessageID)
	setTraceQueryValue(values, "task_id", query.TaskID)
	setTraceQueryValue(values, "thread_id", query.ThreadID)
	setTraceQueryValue(values, "turn_id", query.TurnID)
	setTraceQueryValue(values, "stage", query.Stage)
	if !query.Since.IsZero() {
		values.Set("since", query.Since.Format(time.RFC3339))
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	path := "/api/traces"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	endpoint, err := runtimeAPIURL(cfg.APIAddr, path)
	if err != nil {
		return observability.Page{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return observability.Page{}, err
	}
	if token := strings.TrimSpace(cfg.APIToken); token != "" {
		request.Header.Set("X-WeClaw-Token", token)
	}
	response, err := codexAccountHTTPClient.Do(request)
	if err != nil {
		return observability.Page{}, err
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Message string `json:"message"`
		}
		if err := decoder.Decode(&apiError); err == nil && strings.TrimSpace(apiError.Message) != "" {
			return observability.Page{}, fmt.Errorf("%s", apiError.Message)
		}
		return observability.Page{}, fmt.Errorf("Trace API 返回 HTTP %d", response.StatusCode)
	}
	var page observability.Page
	if err := decoder.Decode(&page); err != nil {
		return observability.Page{}, err
	}
	return page, nil
}

func setTraceQueryValue(values url.Values, key string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values.Set(key, value)
	}
}

func writeTracePage(cmd *cobra.Command, page observability.Page, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(page)
	}
	if len(page.Events) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "未找到匹配的 Trace 事件。")
		return err
	}
	for _, event := range page.Events {
		identity := firstTraceIdentity(event)
		line := fmt.Sprintf("%s  %-24s %-10s %s", event.CreatedAt.Local().Format("2006-01-02 15:04:05.000"), event.Stage, event.State, identity)
		if summary := strings.TrimSpace(event.Summary); summary != "" {
			line += "  " + summary
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(line)); err != nil {
			return err
		}
	}
	if page.Truncated {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "结果已截断，请增加筛选条件或调整 --limit。")
		return err
	}
	return nil
}

func firstTraceIdentity(event observability.Event) string {
	parts := make([]string, 0, 5)
	for _, pair := range []struct{ key, value string }{
		{"agent", event.AgentName}, {"task", event.TaskID}, {"thread", event.ThreadID},
		{"turn", event.TurnID}, {"seq", sequenceString(event.Sequence)},
	} {
		if strings.TrimSpace(pair.value) != "" {
			parts = append(parts, pair.key+"="+pair.value)
		}
	}
	return strings.Join(parts, " ")
}

func sequenceString(sequence uint64) string {
	if sequence == 0 {
		return ""
	}
	return strconv.FormatUint(sequence, 10)
}
