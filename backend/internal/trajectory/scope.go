package trajectory

import (
	"context"
	"time"
)

// ScopeType 开关作用域。
type ScopeType string

const (
	ScopeGlobal ScopeType = "global"
	ScopeGroup  ScopeType = "group"
	ScopeAPIKey ScopeType = "api_key"
)

// Scope 一条开关记录。global 作用域的 ID 固定为 0。
type Scope struct {
	Type      ScopeType `json:"scope_type"`
	ID        int64     `json:"scope_id"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ScopeRepository 开关持久化。
type ScopeRepository interface {
	ListScopes(ctx context.Context) ([]Scope, error)
	UpsertScope(ctx context.Context, scope Scope) error
	DeleteScope(ctx context.Context, scopeType ScopeType, id int64) error
}

// scopeSnapshot 进程内只读快照，热路径只做 map 查找。
type scopeSnapshot struct {
	global bool
	groups map[int64]bool
	keys   map[int64]bool
}

func newScopeSnapshot(scopes []Scope) *scopeSnapshot {
	s := &scopeSnapshot{groups: map[int64]bool{}, keys: map[int64]bool{}}
	for _, sc := range scopes {
		switch sc.Type {
		case ScopeGlobal:
			s.global = sc.Enabled
		case ScopeGroup:
			s.groups[sc.ID] = sc.Enabled
		case ScopeAPIKey:
			s.keys[sc.ID] = sc.Enabled
		}
	}
	return s
}

// decide 判定规则：global ∧（API key 覆盖值，若存在；否则 group 开关）。全部默认关闭。
func (s *scopeSnapshot) decide(groupID, apiKeyID int64) bool {
	if s == nil || !s.global {
		return false
	}
	if apiKeyID > 0 {
		if v, ok := s.keys[apiKeyID]; ok {
			return v
		}
	}
	if groupID > 0 {
		return s.groups[groupID]
	}
	return false
}
