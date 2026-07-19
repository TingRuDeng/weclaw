package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type StoreOptions struct {
	DataDir    string
	CodexHome  string
	SocketPath string
	Keyring    KeyringClient
	Now        func() time.Time
}

// CodexAccountStore 描述单个 shared-host namespace 的账号索引能力。
type CodexAccountStore interface {
	List() (Index, error)
	Current() (*Profile, Index, error)
	Save(context.Context, *Snapshot, SaveOptions) (Profile, error)
	SaveAuthFile(context.Context, SaveOptions) (Profile, error)
	Remove(context.Context, string) error
	Status() (Status, error)
	Doctor() DoctorResult
}

// Store 保存单个 Codex shared-host namespace 下的账号索引与 secret 引用。
type Store struct {
	dataDir    string
	codexHome  string
	socketPath string
	hostID     string
	root       string
	indexPath  string
	authPath   string
	keyring    KeyringClient
	now        func() time.Time
}

var _ CodexAccountStore = (*Store)(nil)

func NewStore(options StoreOptions) (*Store, error) {
	dataDir, err := absoluteCleanPath(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve WeClaw data dir: %w", err)
	}
	codexHome, err := absoluteCleanPath(options.CodexHome)
	if err != nil {
		return nil, fmt.Errorf("resolve CODEX_HOME: %w", err)
	}
	socketPath, err := absoluteCleanPath(options.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex app-server socket: %w", err)
	}
	if err := ensureSecureDir(codexHome); err != nil {
		return nil, NewError(CodeInvalid, "CODEX_HOME 必须是当前用户持有的 0700 实体目录", err)
	}
	hostID, err := HostID(codexHome, socketPath)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dataDir, "codex-accounts", hostID)
	client := options.Keyring
	if client == nil {
		client = systemKeyringClient{}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Store{
		dataDir:    dataDir,
		codexHome:  codexHome,
		socketPath: socketPath,
		hostID:     hostID,
		root:       root,
		indexPath:  filepath.Join(root, "index.json"),
		authPath:   filepath.Join(codexHome, "auth.json"),
		keyring:    client,
		now:        now,
	}, nil
}

func (s *Store) HostID() string     { return s.hostID }
func (s *Store) CodexHome() string  { return s.codexHome }
func (s *Store) SocketPath() string { return s.socketPath }
func (s *Store) Root() string       { return s.root }
func (s *Store) AuthPath() string   { return s.authPath }

func (s *Store) List() (Index, error) {
	return s.readIndex()
}

func (s *Store) Current() (*Profile, Index, error) {
	index, err := s.readIndex()
	if err != nil {
		return nil, Index{}, err
	}
	if index.ActiveProfileID == "" {
		return nil, index, nil
	}
	profile, ok := profileByID(index.Profiles, index.ActiveProfileID)
	if !ok {
		return nil, Index{}, wrapInvalid("读取", fmt.Errorf("active profile missing"))
	}
	return &profile, index, nil
}

func (s *Store) Save(ctx context.Context, snapshot *Snapshot, options SaveOptions) (Profile, error) {
	var saved Profile
	err := s.WithTransaction(ctx, func(tx *Transaction) error {
		profile, err := tx.PutSnapshot(snapshot, options)
		if err != nil {
			return err
		}
		if err := tx.SetActive(profile.ID); err != nil {
			return err
		}
		saved = profile
		return nil
	})
	if err == nil {
		if current, _, currentErr := s.Current(); currentErr == nil && current != nil {
			saved = *current
		}
	}
	return saved, err
}

func (s *Store) SaveAuthFile(ctx context.Context, options SaveOptions) (Profile, error) {
	snapshot, err := ReadAuthFile(s.authPath)
	if err != nil {
		return Profile{}, err
	}
	return s.Save(ctx, snapshot, options)
}

func (s *Store) Remove(ctx context.Context, reference string) error {
	return s.WithTransaction(ctx, func(tx *Transaction) error {
		return tx.Remove(reference)
	})
}

func (s *Store) ReadProfileSecret(ctx context.Context, reference string) (Profile, *Snapshot, error) {
	var profile Profile
	var snapshot *Snapshot
	err := s.WithTransaction(ctx, func(tx *Transaction) error {
		resolved, ok := tx.Find(reference)
		if !ok {
			return NewError(CodeNotFound, "未找到 Codex 账号", nil)
		}
		value, err := tx.ReadSecret(resolved)
		if err != nil {
			return err
		}
		profile = resolved
		snapshot = value
		return nil
	})
	return profile, snapshot, err
}

func (s *Store) WithTransaction(ctx context.Context, fn func(*Transaction) error) error {
	if err := s.ensureStoreRoot(); err != nil {
		return wrapInvalid("打开", err)
	}
	lock, err := acquireFileLock(ctx, filepath.Join(s.root, "switch.lock"))
	if err != nil {
		return err
	}
	defer lock.release()
	index, err := s.readIndex()
	if err != nil {
		return err
	}
	tx := &Transaction{store: s, index: index}
	if err := fn(tx); err != nil {
		tx.cleanupNewSecrets()
		return err
	}
	if err := tx.Flush(); err != nil {
		tx.cleanupNewSecrets()
		return err
	}
	return nil
}

func (s *Store) Doctor() DoctorResult {
	result := DoctorResult{HostID: s.hostID, Store: s.root, Auth: s.authPath}
	if _, err := s.readIndex(); err != nil {
		result.Message = err.Error()
		return result
	}
	if _, err := ReadAuthFile(s.authPath); err != nil {
		result.Message = err.Error()
		return result
	}
	result.OK = true
	result.Message = "Codex 账号存储与当前认证文件可用"
	return result
}

func (s *Store) readIndex() (Index, error) {
	if err := s.ensureStoreRoot(); err != nil {
		return Index{}, wrapInvalid("打开索引目录", err)
	}
	data, err := readSecureFile(s.indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Index{Version: indexVersion, Profiles: []Profile{}}, nil
		}
		return Index{}, wrapInvalid("读取索引", err)
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return Index{}, wrapInvalid("解析索引", err)
	}
	if index.Version != indexVersion {
		return Index{}, wrapInvalid("读取索引", fmt.Errorf("unsupported index version %d", index.Version))
	}
	if index.Profiles == nil {
		index.Profiles = []Profile{}
	}
	if err := validateIndex(index); err != nil {
		return Index{}, wrapInvalid("校验索引", err)
	}
	sortProfiles(index.Profiles)
	return index, nil
}

func (s *Store) ensureStoreRoot() error {
	for _, directory := range []string{s.dataDir, filepath.Join(s.dataDir, "codex-accounts"), s.root} {
		if err := ensureSecureDir(directory); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) writeIndex(index Index) error {
	if err := validateIndex(index); err != nil {
		return wrapInvalid("保存索引", err)
	}
	index.Version = indexVersion
	index.Revision++
	sortProfiles(index.Profiles)
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return wrapInvalid("序列化索引", err)
	}
	data = append(data, '\n')
	return atomicWriteSecureFile(s.indexPath, data)
}

func validateIndex(index Index) error {
	labels := make(map[string]struct{}, len(index.Profiles))
	ids := make(map[ProfileID]struct{}, len(index.Profiles))
	for _, profile := range index.Profiles {
		if _, err := uuid.Parse(string(profile.ID)); err != nil {
			return fmt.Errorf("invalid profile id")
		}
		label := normalizeLabel(profile.Label)
		if label == "" {
			return fmt.Errorf("empty profile label")
		}
		if _, exists := labels[label]; exists {
			return fmt.Errorf("duplicate profile label")
		}
		if _, exists := ids[profile.ID]; exists {
			return fmt.Errorf("duplicate profile id")
		}
		if profile.AuthMode != "chatgpt" {
			return fmt.Errorf("unsupported profile auth mode")
		}
		if profile.SecretBackend != SecretBackendKeyring && profile.SecretBackend != SecretBackendFile {
			return fmt.Errorf("unsupported secret backend")
		}
		if _, err := uuid.Parse(profile.SecretRef); err != nil {
			return fmt.Errorf("invalid secret reference")
		}
		labels[label] = struct{}{}
		ids[profile.ID] = struct{}{}
	}
	if index.ActiveProfileID != "" {
		if _, exists := ids[index.ActiveProfileID]; !exists {
			return fmt.Errorf("active profile does not exist")
		}
	}
	return nil
}

func normalizeLabel(label string) string {
	return strings.ToLower(strings.TrimSpace(label))
}

func sortProfiles(profiles []Profile) {
	sort.SliceStable(profiles, func(i, j int) bool {
		return strings.ToLower(profiles[i].Label) < strings.ToLower(profiles[j].Label)
	})
}

func profileByID(profiles []Profile, id ProfileID) (Profile, bool) {
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return Profile{}, false
}

type pendingSecret struct {
	backend SecretBackend
	ref     string
}

type Transaction struct {
	store          *Store
	index          Index
	dirty          bool
	newSecrets     []pendingSecret
	pendingDeletes []pendingSecret
}

func (tx *Transaction) Index() Index { return cloneIndex(tx.index) }

func (tx *Transaction) Find(reference string) (Profile, bool) {
	reference = strings.TrimSpace(reference)
	for _, profile := range tx.index.Profiles {
		if string(profile.ID) == reference || strings.EqualFold(profile.Label, reference) {
			return profile, true
		}
	}
	return Profile{}, false
}

func (tx *Transaction) PutSnapshot(snapshot *Snapshot, options SaveOptions) (Profile, error) {
	if snapshot == nil || len(snapshot.Bytes()) == 0 {
		return Profile{}, NewError(CodeInvalid, "Codex 认证快照为空", nil)
	}
	label := strings.TrimSpace(options.Label)
	if label == "" {
		return Profile{}, NewError(CodeInvalid, "Codex 账号标签不能为空", nil)
	}
	if len([]rune(label)) > 64 {
		return Profile{}, NewError(CodeInvalid, "Codex 账号标签不能超过 64 个字符", nil)
	}
	var existing *Profile
	var existingIndex int
	for i := range tx.index.Profiles {
		if strings.EqualFold(tx.index.Profiles[i].Label, label) {
			profile := tx.index.Profiles[i]
			existing = &profile
			existingIndex = i
			break
		}
	}
	if existing != nil && !options.Replace {
		return Profile{}, NewError(CodeConflict, "Codex 账号标签已存在；如需覆盖请使用 --replace", nil)
	}
	backend, ref, err := tx.writeSecret(snapshot, options.AllowFileStore)
	if err != nil {
		return Profile{}, err
	}
	now := tx.store.now().UTC()
	profile := Profile{
		ID:                 ProfileID(uuid.NewString()),
		Label:              label,
		AuthMode:           snapshot.AuthMode(),
		AccountFingerprint: snapshot.AccountFingerprint(),
		EmailMasked:        snapshot.EmailMasked(),
		EmailFingerprint:   snapshot.EmailFingerprint(),
		SecretBackend:      backend,
		SecretRef:          ref,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if existing != nil {
		profile.ID = existing.ID
		profile.CreatedAt = existing.CreatedAt
		profile.LastUsedAt = existing.LastUsedAt
		tx.pendingDeletes = append(tx.pendingDeletes, pendingSecret{backend: existing.SecretBackend, ref: existing.SecretRef})
		tx.index.Profiles[existingIndex] = profile
	} else {
		tx.index.Profiles = append(tx.index.Profiles, profile)
	}
	tx.dirty = true
	return profile, nil
}

// ReplaceProfileSnapshot 用当前运行时刷新后的认证覆盖既有 profile，且不改变后端。
// 账号切换会在停止 host 前先调用并 Flush，确保最新 refresh token 已持久化。
func (tx *Transaction) ReplaceProfileSnapshot(profile Profile, snapshot *Snapshot) (Profile, error) {
	if snapshot == nil || snapshot.AccountFingerprint() != profile.AccountFingerprint ||
		snapshot.EmailFingerprint() != profile.EmailFingerprint {
		return Profile{}, NewError(CodeTargetMismatch, "当前 Codex 运行时与活动账号记录不匹配", nil)
	}
	ref := uuid.NewString()
	secret := pendingSecret{backend: profile.SecretBackend, ref: ref}
	switch profile.SecretBackend {
	case SecretBackendKeyring:
		if err := tx.store.keyring.Set(keyringService, tx.store.hostID+":"+ref, string(snapshot.Bytes())); err != nil {
			return Profile{}, NewError(CodeRuntimeUnavailable, "无法回填当前 Codex 账号凭据", err)
		}
	case SecretBackendFile:
		path := filepath.Join(tx.store.root, "secrets", ref+".json")
		if err := atomicWriteSecureFile(path, snapshot.Bytes()); err != nil {
			return Profile{}, wrapInvalid("回填当前凭据", err)
		}
	default:
		return Profile{}, NewError(CodeInvalid, "Codex 账号使用了未知凭据后端", nil)
	}
	tx.newSecrets = append(tx.newSecrets, secret)
	now := tx.store.now().UTC()
	updated := profile
	updated.SecretRef = ref
	updated.UpdatedAt = now
	found := false
	for i := range tx.index.Profiles {
		if tx.index.Profiles[i].ID == profile.ID {
			tx.index.Profiles[i] = updated
			found = true
			break
		}
	}
	if !found {
		return Profile{}, NewError(CodeNotFound, "未找到当前 Codex 账号", nil)
	}
	tx.pendingDeletes = append(tx.pendingDeletes, pendingSecret{backend: profile.SecretBackend, ref: profile.SecretRef})
	tx.dirty = true
	return updated, nil
}

func (tx *Transaction) ReadSecret(profile Profile) (*Snapshot, error) {
	var data []byte
	var err error
	switch profile.SecretBackend {
	case SecretBackendKeyring:
		var value string
		value, err = tx.store.keyring.Get(keyringService, tx.store.hostID+":"+profile.SecretRef)
		data = []byte(value)
	case SecretBackendFile:
		data, err = readSecureFile(filepath.Join(tx.store.root, "secrets", profile.SecretRef+".json"))
	default:
		return nil, NewError(CodeInvalid, "Codex 账号使用了未知凭据后端", nil)
	}
	if err != nil {
		return nil, NewError(CodeRuntimeUnavailable, "无法读取 Codex 账号凭据", err)
	}
	snapshot, err := ParseSnapshot(data)
	if err != nil {
		return nil, err
	}
	if snapshot.AccountFingerprint() != profile.AccountFingerprint || snapshot.EmailFingerprint() != profile.EmailFingerprint {
		return nil, NewError(CodeTargetMismatch, "Codex 账号索引与凭据不匹配", nil)
	}
	return snapshot, nil
}

func (tx *Transaction) SetActive(id ProfileID) error {
	profile, ok := profileByID(tx.index.Profiles, id)
	if !ok {
		return NewError(CodeNotFound, "未找到 Codex 账号", nil)
	}
	now := tx.store.now().UTC()
	for i := range tx.index.Profiles {
		if tx.index.Profiles[i].ID == profile.ID {
			tx.index.Profiles[i].LastUsedAt = &now
			tx.index.Profiles[i].UpdatedAt = now
			break
		}
	}
	tx.index.ActiveProfileID = profile.ID
	tx.dirty = true
	return nil
}

// RestoreActive 仅供切换回滚恢复事务入口时的 active pointer。
func (tx *Transaction) RestoreActive(id ProfileID) error {
	if id != "" {
		if _, ok := profileByID(tx.index.Profiles, id); !ok {
			return NewError(CodeNotFound, "无法恢复原 Codex 账号指针", nil)
		}
	}
	tx.index.ActiveProfileID = id
	tx.dirty = true
	return nil
}

// RestoreProfileUsage 撤销尚未提交成功的切换对目标 profile 使用时间的修改。
func (tx *Transaction) RestoreProfileUsage(profile Profile) error {
	for i := range tx.index.Profiles {
		if tx.index.Profiles[i].ID != profile.ID {
			continue
		}
		tx.index.Profiles[i].LastUsedAt = profile.LastUsedAt
		tx.index.Profiles[i].UpdatedAt = profile.UpdatedAt
		tx.dirty = true
		return nil
	}
	return NewError(CodeNotFound, "无法恢复目标 Codex 账号的使用状态", nil)
}

func (tx *Transaction) SetLastSwitch(record SwitchRecord) {
	record.At = record.At.UTC()
	tx.index.LastSwitch = &record
	tx.dirty = true
}

func (tx *Transaction) Remove(reference string) error {
	profile, ok := tx.Find(reference)
	if !ok {
		return NewError(CodeNotFound, "未找到 Codex 账号", nil)
	}
	if tx.index.ActiveProfileID == profile.ID {
		return NewError(CodeConflict, "当前正在使用的 Codex 账号不能删除", nil)
	}
	profiles := make([]Profile, 0, len(tx.index.Profiles)-1)
	for _, item := range tx.index.Profiles {
		if item.ID != profile.ID {
			profiles = append(profiles, item)
		}
	}
	tx.index.Profiles = profiles
	tx.pendingDeletes = append(tx.pendingDeletes, pendingSecret{backend: profile.SecretBackend, ref: profile.SecretRef})
	tx.dirty = true
	return nil
}

func (tx *Transaction) Flush() error {
	if !tx.dirty {
		return nil
	}
	if err := tx.store.writeIndex(tx.index); err != nil {
		return err
	}
	tx.index.Revision++
	tx.dirty = false
	tx.newSecrets = nil
	for _, secret := range tx.pendingDeletes {
		_ = tx.deleteSecret(secret)
	}
	tx.pendingDeletes = nil
	return nil
}

func (tx *Transaction) writeSecret(snapshot *Snapshot, allowFile bool) (SecretBackend, string, error) {
	ref := uuid.NewString()
	value := string(snapshot.Bytes())
	if err := tx.store.keyring.Set(keyringService, tx.store.hostID+":"+ref, value); err == nil {
		tx.newSecrets = append(tx.newSecrets, pendingSecret{backend: SecretBackendKeyring, ref: ref})
		return SecretBackendKeyring, ref, nil
	} else if !allowFile {
		return "", "", NewError(CodeFileStoreConsentRequired, "系统凭据库不可用；如确认使用 0600 文件存储，请加 --allow-file-store", err)
	}
	path := filepath.Join(tx.store.root, "secrets", ref+".json")
	if err := atomicWriteSecureFile(path, snapshot.Bytes()); err != nil {
		return "", "", wrapInvalid("保存凭据", err)
	}
	tx.newSecrets = append(tx.newSecrets, pendingSecret{backend: SecretBackendFile, ref: ref})
	return SecretBackendFile, ref, nil
}

func (tx *Transaction) deleteSecret(secret pendingSecret) error {
	switch secret.backend {
	case SecretBackendKeyring:
		return tx.store.keyring.Delete(keyringService, tx.store.hostID+":"+secret.ref)
	case SecretBackendFile:
		err := os.Remove(filepath.Join(tx.store.root, "secrets", secret.ref+".json"))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	default:
		return fmt.Errorf("unknown secret backend")
	}
}

func (tx *Transaction) cleanupNewSecrets() {
	for _, secret := range tx.newSecrets {
		_ = tx.deleteSecret(secret)
	}
	tx.newSecrets = nil
}

func cloneIndex(index Index) Index {
	clone := index
	clone.Profiles = append([]Profile(nil), index.Profiles...)
	if index.LastSwitch != nil {
		record := *index.LastSwitch
		clone.LastSwitch = &record
	}
	return clone
}

func ReadAuthFile(path string) (*Snapshot, error) {
	data, err := readSecureFile(path)
	if err != nil {
		return nil, NewError(CodeRuntimeUnavailable, "无法读取 Codex 当前认证文件", err)
	}
	return ParseSnapshot(data)
}

func WriteAuthFile(path string, snapshot *Snapshot) error {
	if snapshot == nil {
		return NewError(CodeInvalid, "Codex 认证快照为空", nil)
	}
	if err := atomicWriteSecureFile(path, snapshot.Bytes()); err != nil {
		return wrapInvalid("写入认证", err)
	}
	return nil
}

func ReadAuthFileBytes(path string) ([]byte, error) {
	return readSecureFile(path)
}

func RestoreAuthFile(path string, data []byte) error {
	if _, err := ParseSnapshot(data); err != nil {
		return err
	}
	return atomicWriteSecureFile(path, data)
}

func (s *Store) Status() (Status, error) {
	index, err := s.readIndex()
	if err != nil {
		return Status{}, err
	}
	status := Status{
		HostID:     s.hostID,
		Revision:   index.Revision,
		Profiles:   append([]Profile(nil), index.Profiles...),
		LastSwitch: index.LastSwitch,
		CodexHome:  s.codexHome,
		SocketPath: s.socketPath,
		StorePath:  s.root,
		AuthPath:   s.authPath,
	}
	if index.ActiveProfileID != "" {
		profile, ok := profileByID(index.Profiles, index.ActiveProfileID)
		if !ok {
			return Status{}, wrapInvalid("读取", errors.New("active profile missing"))
		}
		status.Current = &profile
	}
	return status, nil
}
