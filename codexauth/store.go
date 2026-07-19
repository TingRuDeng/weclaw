package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
func (s *Store) IndexPath() string  { return s.indexPath }

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
	tx := &Transaction{store: s, index: index, baseIndex: cloneIndex(index)}
	if err := tx.retryPendingSecretDeletes(); err != nil {
		if ErrorCode(err) != CodeCleanupPending {
			return err
		}
		tx.reportDeferredSecretCleanup()
	}
	if err := fn(tx); err != nil {
		if cleanupErr := tx.cleanupNewSecrets(); cleanupErr != nil {
			return errors.Join(err, cleanupErr)
		}
		return err
	}
	if err := tx.Flush(); err != nil {
		if cleanupErr := tx.cleanupNewSecrets(); cleanupErr != nil {
			return errors.Join(err, cleanupErr)
		}
		return err
	}
	return nil
}

func (s *Store) Doctor() DoctorResult {
	result := DoctorResult{HostID: s.hostID, Store: s.root, Auth: s.authPath}
	index, err := s.readIndex()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	if IsUnsafeSwitchRecord(index.LastSwitch) {
		result.Message = "Codex 上次账号切换终态不安全，当前必须禁止写入；请停服后显式执行离线 account use 恢复认证"
		return result
	}
	if len(index.PendingSecretDeletes) > 0 {
		result.Message = fmt.Sprintf("Codex 账号索引仍有 %d 个旧凭据等待清理；请在凭据库恢复后重试账号操作", len(index.PendingSecretDeletes))
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
	cleanupRefs := make(map[string]struct{}, len(index.PendingSecretDeletes))
	for _, pending := range index.PendingSecretDeletes {
		if pending.Backend != SecretBackendKeyring && pending.Backend != SecretBackendFile {
			return fmt.Errorf("unsupported pending secret backend")
		}
		if _, err := uuid.Parse(pending.Ref); err != nil {
			return fmt.Errorf("invalid pending secret reference")
		}
		key := string(pending.Backend) + "\x00" + pending.Ref
		if _, exists := cleanupRefs[key]; exists {
			return fmt.Errorf("duplicate pending secret reference")
		}
		cleanupRefs[key] = struct{}{}
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
	baseIndex      Index
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
	if len(tx.pendingDeletes) > 0 {
		existing := make(map[string]struct{}, len(tx.index.PendingSecretDeletes)+len(tx.pendingDeletes))
		for _, pending := range tx.index.PendingSecretDeletes {
			existing[string(pending.Backend)+"\x00"+pending.Ref] = struct{}{}
		}
		for _, secret := range tx.pendingDeletes {
			key := string(secret.backend) + "\x00" + secret.ref
			if _, found := existing[key]; found {
				continue
			}
			tx.index.PendingSecretDeletes = append(tx.index.PendingSecretDeletes, PendingSecretDelete{Backend: secret.backend, Ref: secret.ref})
			existing[key] = struct{}{}
		}
		tx.pendingDeletes = nil
		tx.dirty = true
	}
	if tx.dirty {
		if err := tx.store.writeIndex(tx.index); err != nil {
			return err
		}
		tx.index.Revision++
		tx.dirty = false
		tx.newSecrets = nil
		tx.baseIndex = cloneIndex(tx.index)
	}
	if err := tx.retryPendingSecretDeletes(); err != nil {
		if ErrorCode(err) == CodeCleanupPending {
			// 主索引已提交，不能把成功操作伪装成完全回滚；待清理引用仍在
			// 0600 索引中，Status/Doctor 会暴露数量，下一次事务先重试。
			tx.reportDeferredSecretCleanup()
			return nil
		}
		return err
	}
	return nil
}

func (tx *Transaction) reportDeferredSecretCleanup() {
	log.Printf("[codexauth] OAuth secret cleanup deferred (pending=%d)", len(tx.index.PendingSecretDeletes))
}

func (tx *Transaction) retryPendingSecretDeletes() error {
	if len(tx.index.PendingSecretDeletes) == 0 {
		return nil
	}
	remaining := make([]PendingSecretDelete, 0, len(tx.index.PendingSecretDeletes))
	var cleanupErrs []error
	for _, pending := range tx.index.PendingSecretDeletes {
		secret := pendingSecret{backend: pending.Backend, ref: pending.Ref}
		if err := tx.deleteSecret(secret); err != nil {
			remaining = append(remaining, pending)
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	if len(remaining) != len(tx.index.PendingSecretDeletes) {
		tx.index.PendingSecretDeletes = remaining
		if err := tx.store.writeIndex(tx.index); err != nil {
			return NewError(CodeCleanupPending, "旧 Codex 凭据已删除，但清理记录未能提交；后续会安全重试", err)
		}
		tx.index.Revision++
		tx.baseIndex = cloneIndex(tx.index)
	}
	if len(cleanupErrs) > 0 {
		return NewError(CodeCleanupPending, "Codex 账号索引已更新，但旧凭据清理仍待重试", errors.Join(cleanupErrs...))
	}
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

func (tx *Transaction) cleanupNewSecrets() error {
	if len(tx.newSecrets) == 0 {
		return nil
	}
	failed := make([]pendingSecret, 0, len(tx.newSecrets))
	var cleanupErrs []error
	for _, secret := range tx.newSecrets {
		if err := tx.deleteSecret(secret); err != nil {
			failed = append(failed, secret)
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	tx.newSecrets = nil
	if len(failed) == 0 {
		return nil
	}
	// 事务主体尚未提交，不能把 tx.index 中的 profile 变化写入磁盘；只在
	// 最近一次成功提交的 baseIndex 上追加 orphan cleanup tombstone。
	cleanupIndex := cloneIndex(tx.baseIndex)
	existing := make(map[string]struct{}, len(cleanupIndex.PendingSecretDeletes)+len(failed))
	for _, pending := range cleanupIndex.PendingSecretDeletes {
		existing[string(pending.Backend)+"\x00"+pending.Ref] = struct{}{}
	}
	for _, secret := range failed {
		key := string(secret.backend) + "\x00" + secret.ref
		if _, found := existing[key]; found {
			continue
		}
		cleanupIndex.PendingSecretDeletes = append(cleanupIndex.PendingSecretDeletes, PendingSecretDelete{Backend: secret.backend, Ref: secret.ref})
		existing[key] = struct{}{}
	}
	writeErr := tx.store.writeIndex(cleanupIndex)
	if writeErr == nil {
		cleanupIndex.Revision++
		tx.baseIndex = cloneIndex(cleanupIndex)
	}
	return NewError(
		CodeCleanupPending,
		"Codex 账号事务已回滚，但新凭据清理仍待重试",
		errors.Join(append(cleanupErrs, writeErr)...),
	)
}

func cloneIndex(index Index) Index {
	clone := index
	clone.Profiles = append([]Profile(nil), index.Profiles...)
	clone.PendingSecretDeletes = append([]PendingSecretDelete(nil), index.PendingSecretDeletes...)
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
		HostID:               s.hostID,
		Revision:             index.Revision,
		Profiles:             append([]Profile(nil), index.Profiles...),
		LastSwitch:           index.LastSwitch,
		PendingSecretDeletes: len(index.PendingSecretDeletes),
		CodexHome:            s.codexHome,
		SocketPath:           s.socketPath,
		StorePath:            s.root,
		AuthPath:             s.authPath,
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
