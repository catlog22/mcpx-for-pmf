package file

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ChangeType for file.changes.
type ChangeType string

const (
	ChangeCreate ChangeType = "create"
	ChangeModify ChangeType = "modify"
	ChangeDelete ChangeType = "delete"
)

// Snapshot is a cheap content fingerprint map.
type Snapshot struct {
	ID            string            `json:"snapshot_id"`
	At            time.Time         `json:"at"`
	WorkspaceRoot string            `json:"-"`
	Hash          map[string]string `json:"-"` // rel path -> sha256 of size+mtime+name
}

// Change is one path delta.
type Change struct {
	Path string     `json:"path"`
	Type ChangeType `json:"type"`
}

var defaultIgnore = []string{".git", "node_modules", "bin", "vendor", ".mcpx"}

// TakeSnapshot walks workspace and fingerprints files.
func TakeSnapshot(workspaceRoot string) (Snapshot, error) {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	hashes := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		base := d.Name()
		for _, ig := range defaultIgnore {
			if base == ig || strings.HasPrefix(rel, ig+string(filepath.Separator)) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		sum := sha256.Sum256([]byte(fmtSizeMtime(rel, info.Size(), info.ModTime())))
		hashes[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:8])
		return nil
	})
	idSum := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	return Snapshot{
		ID:            "snap_" + hex.EncodeToString(idSum[:8]),
		At:            time.Now().UTC(),
		WorkspaceRoot: root,
		Hash:          hashes,
	}, nil
}

func fmtSizeMtime(rel string, size int64, m time.Time) string {
	return rel + "|" + strconv.FormatInt(size, 10) + "|" + m.UTC().Format(time.RFC3339Nano)
}

// DiffSnapshots compares two snapshots.
func DiffSnapshots(old, neu Snapshot) []Change {
	var out []Change
	for p, h := range neu.Hash {
		oh, ok := old.Hash[p]
		if !ok {
			out = append(out, Change{Path: p, Type: ChangeCreate})
			continue
		}
		if oh != h {
			out = append(out, Change{Path: p, Type: ChangeModify})
		}
	}
	for p := range old.Hash {
		if _, ok := neu.Hash[p]; !ok {
			out = append(out, Change{Path: p, Type: ChangeDelete})
		}
	}
	return out
}
