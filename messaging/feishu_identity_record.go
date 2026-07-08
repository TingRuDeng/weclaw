package messaging

import (
	"sort"
	"strings"

	"github.com/fastclaw-ai/weclaw/platform"
)

func mergeOpenIDRecord(record feishuIdentityRecord, records map[string]feishuIdentityRecord, identity feishuIdentityCandidate) feishuIdentityRecord {
	if identity.OpenID == "" || identity.OpenID == identity.Key {
		return record
	}
	old := records[identity.OpenID]
	if old.Key == "" {
		return record
	}
	delete(records, identity.OpenID)
	return mergeFeishuIdentityRecords(record, old)
}

func applyFeishuIdentity(record feishuIdentityRecord, identity feishuIdentityCandidate, now string) feishuIdentityRecord {
	record.Key = identity.Key
	record.UnionID = firstNonBlank(identity.UnionID, record.UnionID)
	record.UserID = firstNonBlank(identity.UserID, record.UserID)
	record.OpenID = firstNonBlank(identity.OpenID, record.OpenID)
	record.OpenIDs = addFeishuOpenID(record.OpenIDs, identity.AccountID, identity.OpenID)
	record.Accounts = appendUniqueString(record.Accounts, identity.AccountID)
	record.LastSeen = now
	record.Pending = !record.Approved
	if record.FirstSeen == "" {
		record.FirstSeen = now
	}
	return record
}

func mergeFeishuIdentityRecords(current feishuIdentityRecord, incoming feishuIdentityRecord) feishuIdentityRecord {
	current.Approved = current.Approved || incoming.Approved
	current.Pending = current.Pending || incoming.Pending
	current.FirstSeen = earliestNonEmptyTime(current.FirstSeen, incoming.FirstSeen)
	current.LastSeen = latestNonEmptyTime(current.LastSeen, incoming.LastSeen)
	current.DisplayName = firstNonBlank(current.DisplayName, incoming.DisplayName)
	current.AuthCode = firstNonBlank(current.AuthCode, incoming.AuthCode)
	current.AuthCodeExpiresAt = firstNonBlank(current.AuthCodeExpiresAt, incoming.AuthCodeExpiresAt)
	current.UnionID = firstNonBlank(current.UnionID, incoming.UnionID)
	current.UserID = firstNonBlank(current.UserID, incoming.UserID)
	current.OpenID = firstNonBlank(current.OpenID, incoming.OpenID)
	current.OpenIDs = mergeStringMap(current.OpenIDs, incoming.OpenIDs)
	current.Accounts = mergeStringSlices(current.Accounts, incoming.Accounts)
	return current
}

func extractFeishuIdentity(msg platform.IncomingMessage) (feishuIdentityCandidate, bool) {
	if msg.Platform != platform.PlatformFeishu {
		return feishuIdentityCandidate{}, false
	}
	metadata := msg.Metadata
	openID := firstNonBlank(metadata["feishu_open_id"], msg.UserID, firstIdentityWithPrefix(msg.UserIdentityKeys(), "ou_"))
	userID := firstNonBlank(metadata["feishu_user_id"], firstIdentityWithPrefix(msg.UserIdentityKeys(), "user_"))
	unionID := firstNonBlank(metadata["feishu_union_id"], firstIdentityWithPrefix(msg.UserIdentityKeys(), "on_"))
	key := firstNonBlank(unionID, userID, openID)
	if key == "" {
		return feishuIdentityCandidate{}, false
	}
	return feishuIdentityCandidate{
		Key:       key,
		AccountID: strings.TrimSpace(msg.AccountID),
		OpenID:    openID,
		UserID:    userID,
		UnionID:   unionID,
	}, true
}

func firstIdentityWithPrefix(values []string, prefix string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, prefix) {
			return value
		}
	}
	return ""
}

func addFeishuOpenID(values map[string]string, accountID string, openID string) map[string]string {
	accountID = strings.TrimSpace(accountID)
	openID = strings.TrimSpace(openID)
	if accountID == "" || openID == "" {
		return values
	}
	if values == nil {
		values = make(map[string]string)
	}
	values[accountID] = openID
	return values
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func copyFeishuIdentityRecord(record feishuIdentityRecord) feishuIdentityRecord {
	record.OpenIDs = mergeStringMap(nil, record.OpenIDs)
	record.Accounts = append([]string(nil), record.Accounts...)
	return record
}

func sortFeishuIdentityRecords(records []feishuIdentityRecord) {
	sort.Slice(records, func(i int, j int) bool {
		if records[i].LastSeen != records[j].LastSeen {
			return records[i].LastSeen > records[j].LastSeen
		}
		return records[i].Key < records[j].Key
	})
}

func (s *feishuIdentityStore) resolveKeyLocked(selector string) string {
	for key, record := range s.records {
		if feishuIdentityRecordMatches(record, selector) {
			return key
		}
	}
	return ""
}

func feishuIdentityRecordMatches(record feishuIdentityRecord, selector string) bool {
	if selector == record.Key || selector == record.UnionID || selector == record.UserID || selector == record.OpenID {
		return true
	}
	for _, openID := range record.OpenIDs {
		if selector == openID {
			return true
		}
	}
	return false
}

func mergeStringMap(current map[string]string, incoming map[string]string) map[string]string {
	if len(current) == 0 && len(incoming) == 0 {
		return nil
	}
	merged := make(map[string]string, len(current)+len(incoming))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range incoming {
		merged[key] = value
	}
	return merged
}

func mergeStringSlices(current []string, incoming []string) []string {
	out := append([]string(nil), current...)
	for _, value := range incoming {
		out = appendUniqueString(out, value)
	}
	return out
}

func earliestNonEmptyTime(left string, right string) string {
	if left == "" || (right != "" && right < left) {
		return right
	}
	return left
}

func latestNonEmptyTime(left string, right string) string {
	if left == "" || right > left {
		return right
	}
	return left
}
