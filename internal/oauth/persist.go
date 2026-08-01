package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// fileStore is the on-disk shape of the client registry.
type fileStore struct {
	Version int       `json:"version"`
	Clients []*Client `json:"clients"`
}

// SetPersistPath enables atomic JSON persistence after mutations.
// path empty disables disk I/O. Load is attempted immediately when path is set.
func (r *Registry) SetPersistPath(path string) error {
	r.mu.Lock()
	r.persistPath = path
	r.mu.Unlock()
	if path == "" {
		return nil
	}
	return r.Load()
}

// Load replaces in-memory clients from disk (if file exists).
func (r *Registry) Load() error {
	r.mu.Lock()
	path := r.persistPath
	r.mu.Unlock()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var store fileStore
	if err := json.Unmarshal(b, &store); err != nil {
		return fmt.Errorf("oauth clients file: %w", err)
	}
	m := make(map[string]*Client, len(store.Clients))
	for _, c := range store.Clients {
		if c == nil || c.ClientID == "" {
			continue
		}
		// copy
		cp := *c
		if cp.RedirectURIs != nil {
			cp.RedirectURIs = append([]string{}, c.RedirectURIs...)
		}
		m[cp.ClientID] = &cp
	}
	r.mu.Lock()
	r.clients = m
	r.mu.Unlock()
	return nil
}

func (r *Registry) saveLocked() error {
	if r.persistPath == "" {
		return nil
	}
	list := make([]*Client, 0, len(r.clients))
	for _, c := range r.clients {
		cp := *c
		if c.RedirectURIs != nil {
			cp.RedirectURIs = append([]string{}, c.RedirectURIs...)
		}
		list = append(list, &cp)
	}
	store := fileStore{Version: 1, Clients: list}
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.persistPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := r.persistPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.persistPath)
}
