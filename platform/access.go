package platform

import "strings"
import "sync"

// AccessControlledPlatform 表示可接收 Registry 访问控制器的平台实例。
type AccessControlledPlatform interface {
	SetAccessControl(AccessControl)
}

// AccessControl 保存单个平台的允许用户集合，空集合按默认拒绝处理。
type AccessControl struct {
	state *accessState
}

type accessState struct {
	mu      sync.RWMutex
	allowed map[string]bool
	users   []string
}

// NewAccessControl 构造访问控制器，忽略空白用户 ID。
func NewAccessControl(allowed []string) AccessControl {
	result := AccessControl{
		state: &accessState{
			allowed: make(map[string]bool, len(allowed)),
			users:   make([]string, 0, len(allowed)),
		},
	}
	for _, userID := range allowed {
		userID = strings.TrimSpace(userID)
		if userID == "" || result.state.allowed[userID] {
			continue
		}
		result.state.allowed[userID] = true
		result.state.users = append(result.state.users, userID)
	}
	return result
}

// Allowed 判断用户是否在白名单内；空白名单默认拒绝所有用户。
func (a AccessControl) Allowed(userID string) bool {
	if a.state == nil {
		return false
	}
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	return a.state.allowed[strings.TrimSpace(userID)]
}

// AllowedUsers 返回白名单副本，避免调用方修改内部状态。
func (a AccessControl) AllowedUsers() []string {
	if a.state == nil {
		return nil
	}
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	users := make([]string, len(a.state.users))
	copy(users, a.state.users)
	return users
}

// SetAllowed 热更新白名单，供软配置重载在不重启平台连接时生效。
func (a AccessControl) SetAllowed(allowed []string) {
	if a.state == nil {
		return
	}
	next := NewAccessControl(allowed)
	a.state.mu.Lock()
	a.state.allowed = next.state.allowed
	a.state.users = next.state.users
	a.state.mu.Unlock()
}
