package file

import (
	"fmt"
	"os"
	"strings"
)

// PatchSearchReplace applies unique (or all) string replacement.
type PatchSearchReplace struct {
	WorkspaceRoot string
	Path          string
	OldString     string
	NewString     string
	ReplaceAll    bool
}

// PatchResult is file.patch data.
type PatchResult struct {
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
	Count   int    `json:"count,omitempty"`
}

// SearchReplace edits a file under workspace.
func SearchReplace(p PatchSearchReplace) (PatchResult, error) {
	if p.OldString == "" {
		return PatchResult{}, fmt.Errorf("old_string required")
	}
	if _, err := Resolve(p.WorkspaceRoot, p.Path); err != nil {
		return PatchResult{}, err
	}
	root, err := os.OpenRoot(p.WorkspaceRoot)
	if err != nil {
		return PatchResult{}, err
	}
	defer root.Close()
	data, err := root.ReadFile(p.Path)
	if err != nil {
		return PatchResult{}, err
	}
	info, err := root.Stat(p.Path)
	if err != nil {
		return PatchResult{}, err
	}
	content := string(data)
	count := strings.Count(content, p.OldString)
	if count == 0 {
		return PatchResult{}, fmt.Errorf("old_string not found")
	}
	if !p.ReplaceAll && count != 1 {
		return PatchResult{}, fmt.Errorf("old_string found %d times; set replace_all or make unique", count)
	}
	var next string
	if p.ReplaceAll {
		next = strings.ReplaceAll(content, p.OldString, p.NewString)
	} else {
		next = strings.Replace(content, p.OldString, p.NewString, 1)
		count = 1
	}
	if next == content {
		return PatchResult{Path: p.Path, Changed: false, Count: 0}, nil
	}
	if err := root.WriteFile(p.Path, []byte(next), info.Mode().Perm()); err != nil {
		return PatchResult{}, err
	}
	return PatchResult{Path: p.Path, Changed: true, Count: count}, nil
}
