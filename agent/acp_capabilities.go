package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	canonicalClaudeConfiguredName = "claude"
	claudeACPAgentName            = "claude-agent-acp"
	claudeACPScopedAgentName      = "@agentclientprotocol/claude-agent-acp"
)

type acpCapabilitySnapshot struct {
	ProtocolVersion int
	AgentInfo       acpInitializeAgentInfo
	Session         acpSessionCapabilitySnapshot
}

type acpSessionCapabilitySnapshot struct {
	List   bool
	Resume bool
}

// cacheAndValidateACPCapabilities 解析并缓存标准 ACP initialize 能力声明。
func (a *ACPAgent) cacheAndValidateACPCapabilities(result json.RawMessage) error {
	snapshot, err := parseACPCapabilitySnapshot(result)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.capabilities = snapshot
	a.mu.Unlock()
	if !requiresClaudeSessionCapabilities(a.configuredName, snapshot.AgentInfo) {
		return nil
	}
	return validateClaudeACPCapabilities(snapshot.Session)
}

// requiresClaudeSessionCapabilities 合并显式配置身份与标准握手身份。
func requiresClaudeSessionCapabilities(configuredName string, info acpInitializeAgentInfo) bool {
	if strings.EqualFold(strings.TrimSpace(configuredName), canonicalClaudeConfiguredName) {
		return true
	}
	return isClaudeACPAgentInfo(info)
}

// isClaudeACPAgentInfo 接受官方 scoped 名，并兼容既有 unscoped 名。
func isClaudeACPAgentInfo(info acpInitializeAgentInfo) bool {
	name := strings.ToLower(strings.TrimSpace(info.Name))
	return name == claudeACPAgentName || name == claudeACPScopedAgentName
}

// acpCapabilitiesSnapshot 返回不可变的初始化能力快照。
func (a *ACPAgent) acpCapabilitiesSnapshot() acpCapabilitySnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.capabilities
}

// parseACPCapabilitySnapshot 将标准 initialize result 收敛为运行时布尔能力。
func parseACPCapabilitySnapshot(result json.RawMessage) (acpCapabilitySnapshot, error) {
	var initialized acpInitializeResult
	if err := json.Unmarshal(result, &initialized); err != nil {
		return acpCapabilitySnapshot{}, fmt.Errorf("解析 ACP initialize result 失败: %w", err)
	}
	if initialized.ProtocolVersion != acpProtocolVersion {
		return acpCapabilitySnapshot{}, fmt.Errorf(
			"ACP initialize protocolVersion=%d，与客户端版本 %d 不匹配",
			initialized.ProtocolVersion, acpProtocolVersion,
		)
	}
	session, err := parseACPSessionCapabilities(initialized.AgentCapabilities.Session)
	if err != nil {
		return acpCapabilitySnapshot{}, err
	}
	return acpCapabilitySnapshot{
		ProtocolVersion: initialized.ProtocolVersion,
		AgentInfo:       initialized.AgentInfo,
		Session:         session,
	}, nil
}

// parseACPSessionCapabilities 要求已声明的 list/resume 能力都是 JSON object。
func parseACPSessionCapabilities(source acpInitializeSessionCapabilities) (acpSessionCapabilitySnapshot, error) {
	list, err := parseACPObjectCapability(source.List, "agentCapabilities.sessionCapabilities.list")
	if err != nil {
		return acpSessionCapabilitySnapshot{}, err
	}
	resume, err := parseACPObjectCapability(source.Resume, "agentCapabilities.sessionCapabilities.resume")
	if err != nil {
		return acpSessionCapabilitySnapshot{}, err
	}
	return acpSessionCapabilitySnapshot{List: list, Resume: resume}, nil
}

// parseACPObjectCapability 区分字段缺失与显式但格式错误的能力声明。
func parseACPObjectCapability(raw json.RawMessage, path string) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return false, fmt.Errorf("%s 必须是 JSON object", path)
	}
	return true, nil
}

// validateClaudeACPCapabilities 阻止缺少目录或恢复能力的 Claude adapter 启动。
func validateClaudeACPCapabilities(session acpSessionCapabilitySnapshot) error {
	missing := make([]string, 0, 2)
	if !session.List {
		missing = append(missing, "agentCapabilities.sessionCapabilities.list")
	}
	if !session.Resume {
		missing = append(missing, "agentCapabilities.sessionCapabilities.resume")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"Claude ACP 缺少必需能力 %s；请升级 claude-agent-acp，WeClaw 要求 sessionCapabilities.list 和 sessionCapabilities.resume",
		strings.Join(missing, "、"),
	)
}
