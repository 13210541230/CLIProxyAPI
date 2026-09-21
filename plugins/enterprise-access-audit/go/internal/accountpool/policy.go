package accountpool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ProviderCodex is the only supported account pool provider scope for phase 1.
const ProviderCodex = "codex"

// Pool is one department account pool.
type Pool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Enabled     bool   `json:"enabled"`
	CreatedAtMS int64  `json:"createdAtMs"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}

// Member binds one auth credential to a pool with a selection priority.
type Member struct {
	PoolID   string `json:"poolId"`
	AuthID   string `json:"authId"`
	Priority int    `json:"priority"`
	Enabled  bool   `json:"enabled"`
}

// Binding maps one caller (API-key hash) to its primary pool.
type Binding struct {
	APIKeyHash  string `json:"apiKeyHash"`
	PoolID      string `json:"poolId"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}

// Policy is the versioned full snapshot applied by the account pool service.
type Policy struct {
	Version  int64     `json:"version"`
	Hash     string    `json:"hash"`
	Provider string    `json:"provider"`
	Pools    []Pool    `json:"pools"`
	Members  []Member  `json:"members"`
	Bindings []Binding `json:"bindings"`
}

// Status summarizes the applied policy state for management diagnostics.
type Status struct {
	Version         int64  `json:"version"`
	Hash            string `json:"hash"`
	Ready           bool   `json:"ready"`
	LastError       string `json:"lastError,omitempty"`
	ConfiguredPools int    `json:"configuredPools"`
	BoundCallers    int    `json:"boundCallers"`
}

// Envelope is the wire shape accepted by the policy management route.
type Envelope struct {
	Policy Policy `json:"policy"`
}

// Normalize trims, validates, sorts, and canonicalizes a policy snapshot.
func Normalize(p Policy) (Policy, error) {
	if p.Version <= 0 {
		return Policy{}, errors.New("account pool policy version is required")
	}
	if !strings.EqualFold(strings.TrimSpace(p.Provider), ProviderCodex) {
		return Policy{}, errors.New("account pool policy provider must be codex")
	}
	p.Provider = ProviderCodex

	poolIDs := make(map[string]struct{}, len(p.Pools))
	for i := range p.Pools {
		p.Pools[i].ID = strings.TrimSpace(p.Pools[i].ID)
		p.Pools[i].Name = strings.TrimSpace(p.Pools[i].Name)
		if p.Pools[i].ID == "" || p.Pools[i].Name == "" {
			return Policy{}, errors.New("account pool id and name are required")
		}
		if _, exists := poolIDs[p.Pools[i].ID]; exists {
			return Policy{}, fmt.Errorf("duplicate account pool %q", p.Pools[i].ID)
		}
		poolIDs[p.Pools[i].ID] = struct{}{}
	}

	memberIDs := make(map[string]map[string]struct{}, len(p.Pools))
	for i := range p.Members {
		member := &p.Members[i]
		member.PoolID = strings.TrimSpace(member.PoolID)
		member.AuthID = strings.TrimSpace(member.AuthID)
		if member.AuthID == "" {
			return Policy{}, errors.New("account pool member auth id is required")
		}
		if _, exists := poolIDs[member.PoolID]; !exists {
			return Policy{}, fmt.Errorf("account pool member references unknown pool %q", member.PoolID)
		}
		if !member.Enabled {
			continue
		}
		ids := memberIDs[member.PoolID]
		if ids == nil {
			ids = map[string]struct{}{}
			memberIDs[member.PoolID] = ids
		}
		if _, exists := ids[member.AuthID]; exists {
			return Policy{}, fmt.Errorf("duplicate account pool member %q", member.AuthID)
		}
		ids[member.AuthID] = struct{}{}
	}

	bindingIDs := make(map[string]struct{}, len(p.Bindings))
	for i := range p.Bindings {
		binding := &p.Bindings[i]
		binding.APIKeyHash = strings.ToLower(strings.TrimSpace(binding.APIKeyHash))
		binding.PoolID = strings.TrimSpace(binding.PoolID)
		if binding.APIKeyHash == "" {
			return Policy{}, errors.New("account pool binding caller hash is required")
		}
		if _, exists := poolIDs[binding.PoolID]; !exists {
			return Policy{}, fmt.Errorf("account pool binding references unknown pool %q", binding.PoolID)
		}
		if _, exists := bindingIDs[binding.APIKeyHash]; exists {
			return Policy{}, fmt.Errorf("duplicate account pool binding %q", binding.APIKeyHash)
		}
		bindingIDs[binding.APIKeyHash] = struct{}{}
	}

	sort.Slice(p.Pools, func(i, j int) bool { return p.Pools[i].ID < p.Pools[j].ID })
	sort.Slice(p.Members, func(i, j int) bool {
		if p.Members[i].PoolID != p.Members[j].PoolID {
			return p.Members[i].PoolID < p.Members[j].PoolID
		}
		if p.Members[i].Priority != p.Members[j].Priority {
			return p.Members[i].Priority < p.Members[j].Priority
		}
		return p.Members[i].AuthID < p.Members[j].AuthID
	})
	sort.Slice(p.Bindings, func(i, j int) bool { return p.Bindings[i].APIKeyHash < p.Bindings[j].APIKeyHash })
	return p, nil
}

// Hash computes the canonical SHA-256 hash of the payload without the hash field.
func Hash(p Policy) string {
	p.Hash = ""
	encoded, _ := json.Marshal(p)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// ValidateHash verifies that the policy hash matches its payload.
func ValidateHash(p Policy) error {
	if !strings.EqualFold(p.Hash, Hash(p)) {
		return errors.New("account pool policy hash does not match payload")
	}
	return nil
}

// Clone returns a deep copy suitable for storage or dismissal.
func Clone(p Policy) Policy {
	p.Pools = append([]Pool(nil), p.Pools...)
	p.Members = append([]Member(nil), p.Members...)
	p.Bindings = append([]Binding(nil), p.Bindings...)
	return p
}

// PoolByID returns the pool with the given id and whether it exists.
func PoolByID(p Policy, id string) (Pool, bool) {
	for _, pool := range p.Pools {
		if pool.ID == id {
			return pool, true
		}
	}
	return Pool{}, false
}

// EnabledMembers returns the enabled members for a pool in priority order.
func EnabledMembers(p Policy, poolID string) []Member {
	members := make([]Member, 0)
	for _, member := range p.Members {
		if member.PoolID == poolID && member.Enabled {
			members = append(members, member)
		}
	}
	return members
}

// BindingPool returns the primary pool id for a caller hash and whether bound.
func BindingPool(p Policy, callerHash string) (string, bool) {
	for _, binding := range p.Bindings {
		if strings.EqualFold(binding.APIKeyHash, callerHash) {
			return binding.PoolID, true
		}
	}
	return "", false
}
