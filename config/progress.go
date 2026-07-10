package config

// ProgressConfig 控制微信侧进度反馈的展示粒度。
type ProgressConfig struct {
	Mode                   string `json:"mode,omitempty"`
	SendAcceptance         *bool  `json:"send_acceptance,omitempty"`
	EnableTyping           *bool  `json:"enable_typing,omitempty"`
	TypingHeartbeatSeconds int    `json:"typing_heartbeat_seconds,omitempty"`
	InitialDelaySeconds    int    `json:"initial_delay_seconds,omitempty"`
	SummaryIntervalSeconds int    `json:"summary_interval_seconds,omitempty"`
	MaxProgressMessages    int    `json:"max_progress_messages,omitempty"`
	ShowTextPreview        *bool  `json:"show_text_preview,omitempty"`
	PreviewRunes           int    `json:"preview_runes,omitempty"`
	MaxTailRunes           int    `json:"max_tail_runes,omitempty"`
	DuplicateTTLSeconds    int    `json:"duplicate_ttl_seconds,omitempty"`
	TaskTimeoutSeconds     int    `json:"task_timeout_seconds,omitempty"`
	IncludePartialOnError  *bool  `json:"include_partial_on_error,omitempty"`
}

// DefaultProgressConfig 返回微信场景下更安静的默认进度体验。
func DefaultProgressConfig() ProgressConfig {
	sendAcceptance := false
	enableTyping := true
	showTextPreview := false
	includePartialOnError := false
	return ProgressConfig{
		Mode: "typing", SendAcceptance: &sendAcceptance, EnableTyping: &enableTyping,
		TypingHeartbeatSeconds: 8, InitialDelaySeconds: 10, SummaryIntervalSeconds: 20,
		MaxProgressMessages: 4, ShowTextPreview: &showTextPreview, PreviewRunes: 180,
		MaxTailRunes: 1800, DuplicateTTLSeconds: 300,
		IncludePartialOnError: &includePartialOnError,
	}
}

// NormalizeProgressConfig 用局部配置覆盖基础配置，未填写字段沿用基础值。
func NormalizeProgressConfig(base ProgressConfig, override *ProgressConfig) ProgressConfig {
	if override == nil {
		return base
	}
	cfg := base
	if override.Mode != "" {
		cfg.Mode = override.Mode
	}
	if override.SendAcceptance != nil {
		cfg.SendAcceptance = override.SendAcceptance
	}
	if override.EnableTyping != nil {
		cfg.EnableTyping = override.EnableTyping
	}
	cfg = mergeProgressNumbers(cfg, *override)
	if override.ShowTextPreview != nil {
		cfg.ShowTextPreview = override.ShowTextPreview
	}
	if override.IncludePartialOnError != nil {
		cfg.IncludePartialOnError = override.IncludePartialOnError
	}
	return cfg
}

func mergeProgressNumbers(base ProgressConfig, override ProgressConfig) ProgressConfig {
	values := []struct {
		source int
		target *int
	}{
		{override.TypingHeartbeatSeconds, &base.TypingHeartbeatSeconds},
		{override.InitialDelaySeconds, &base.InitialDelaySeconds},
		{override.SummaryIntervalSeconds, &base.SummaryIntervalSeconds},
		{override.MaxProgressMessages, &base.MaxProgressMessages},
		{override.PreviewRunes, &base.PreviewRunes},
		{override.MaxTailRunes, &base.MaxTailRunes},
		{override.DuplicateTTLSeconds, &base.DuplicateTTLSeconds},
		{override.TaskTimeoutSeconds, &base.TaskTimeoutSeconds},
	}
	for _, value := range values {
		if value.source > 0 {
			*value.target = value.source
		}
	}
	return base
}
