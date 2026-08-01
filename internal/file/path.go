package file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve ensures an existing path is under workspace root after resolving
// symbolic links. Checking only the lexical path would allow a workspace link
// such as outside -> /etc to escape the boundary.
func Resolve(workspaceRoot, rel string) (abs string, err error) {
	if rel == "" {
		return "", fmt.Errorf("path required")
	}
	rootLexical, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", err
	}
	rootLexical = filepath.Clean(rootLexical)
	root, err := filepath.EvalSymlinks(rootLexical)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	joined := filepath.Clean(filepath.Join(rootLexical, rel))
	if !withinRoot(rootLexical, joined) {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if os.IsNotExist(err) {
			// Resolve the nearest existing ancestor. This preserves support for
			// future files while still detecting a symlinked parent escape.
			ancestor := filepath.Dir(joined)
			for {
				realParent, parentErr := filepath.EvalSymlinks(ancestor)
				if parentErr == nil {
					if !withinRoot(root, realParent) {
						return "", fmt.Errorf("path escapes workspace: %s", rel)
					}
					return joined, nil
				}
				parent := filepath.Dir(ancestor)
				if parent == ancestor {
					return "", parentErr
				}
				ancestor = parent
			}
		}
		return "", err
	}
	if !withinRoot(root, resolved) {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	return resolved, nil
}

func withinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Rel returns path relative to workspace for display.
func Rel(workspaceRoot, abs string) (string, error) {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", err
	}
	return filepath.Rel(root, abs)
}
