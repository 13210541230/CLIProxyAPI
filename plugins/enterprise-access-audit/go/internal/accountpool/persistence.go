package accountpool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	policyFileName = "account-pool-policy.json"
	limitsFileName = "account-pool-limits.json"
)

// AccountLimit configures per-account concurrency admission. A zero Limit and
// Limit15s mean the account is not concurrency-managed (routed but only guarded
// by reservations, never gated).
type AccountLimit struct {
	AuthID        string `json:"authId"`
	Limit         int    `json:"limit"`
	Limit15s      int    `json:"limit15s"`
	WindowSeconds int    `json:"windowSeconds"`
}

// persistence stores the versioned policy and per-account concurrency limits
// under the plugin account-pool data directory.
type persistence struct {
	mu      sync.Mutex
	dataDir string
}

func newPersistence(dataDir string) *persistence {
	return &persistence{dataDir: dataDir}
}

func (s *persistence) loadPolicy() (Policy, error) {
	if s == nil {
		return Policy{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.dataDir, policyFileName))
	if errors.Is(err, os.ErrNotExist) {
		return Policy{}, nil
	}
	if err != nil {
		return Policy{}, err
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, err
	}
	p, err = Normalize(p)
	if err != nil {
		return Policy{}, err
	}
	if err := ValidateHash(p); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func (s *persistence) savePolicy(p Policy) error {
	if s == nil {
		return errors.New("account pool persistence is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.dataDir, policyFileName, p)
}

func (s *persistence) loadLimits() (map[string]AccountLimit, error) {
	if s == nil {
		return map[string]AccountLimit{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.dataDir, limitsFileName))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]AccountLimit{}, nil
	}
	if err != nil {
		return nil, err
	}
	var limits []AccountLimit
	if err := json.Unmarshal(data, &limits); err != nil {
		return nil, err
	}
	out := make(map[string]AccountLimit, len(limits))
	for _, limit := range limits {
		if limit.AuthID == "" {
			continue
		}
		out[limit.AuthID] = limit
	}
	return out, nil
}

func (s *persistence) saveLimits(limits map[string]AccountLimit) error {
	if s == nil {
		return errors.New("account pool persistence is unavailable")
	}
	items := make([]AccountLimit, 0, len(limits))
	for _, limit := range limits {
		if limit.AuthID == "" {
			continue
		}
		items = append(items, limit)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.dataDir, limitsFileName, items)
}

func writeJSONAtomic(dataDir, name string, value any) error {
	if dataDir == "" {
		return errors.New("account pool data directory is not configured")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dataDir, name))
}
