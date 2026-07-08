package messaging

type FeishuIdentityView struct {
	Key      string
	UnionID  string
	UserID   string
	OpenID   string
	OpenIDs  map[string]string
	Accounts []string
	Pending  bool
	Approved bool
}

// LoadFeishuIdentityViews 读取飞书自动发现身份，供本地 CLI 做只读展示。
func LoadFeishuIdentityViews(filePath string, pendingOnly bool) ([]FeishuIdentityView, error) {
	store := newFeishuIdentityStore()
	store.SetFilePath(firstNonBlank(filePath, DefaultFeishuIdentityFile()))
	if err := store.LoadError(); err != nil {
		return nil, err
	}
	records := store.ListRecords()
	if pendingOnly {
		records = store.ListPending()
	}
	views := make([]FeishuIdentityView, 0, len(records))
	for _, record := range records {
		views = append(views, feishuIdentityViewFromRecord(record))
	}
	return views, nil
}

func feishuIdentityViewFromRecord(record feishuIdentityRecord) FeishuIdentityView {
	return FeishuIdentityView{
		Key:      record.Key,
		UnionID:  record.UnionID,
		UserID:   record.UserID,
		OpenID:   record.OpenID,
		OpenIDs:  cloneStringMap(record.OpenIDs),
		Accounts: append([]string(nil), record.Accounts...),
		Pending:  record.Pending,
		Approved: record.Approved,
	}
}

// cloneStringMap 复制身份映射，避免 CLI 展示层误改持久化记录。
func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
