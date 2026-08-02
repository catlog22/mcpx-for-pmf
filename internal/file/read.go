package file

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ReadOptions controls file.read.
type ReadOptions struct {
	WorkspaceRoot string
	Path          string // relative
	Offset        int    // 0-based line start
	Limit         int    // max lines; 0 => default
	MaxBytes      int64
}

// ReadResult is file.read data.
type ReadResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	Truncated  bool   `json:"truncated"`
	SHA256     string `json:"-"`
}

// FullReadOptions controls an explicit whole-file read. Unlike ReadOptions,
// FullReadOptions permits binary content but always enforces MaxBytes.
type FullReadOptions struct {
	WorkspaceRoot string
	Path          string
	MaxBytes      int64
}

// FullReadResult contains the complete file bytes and their display metadata.
// Content is intentionally excluded from JSON serialization so callers choose
// the appropriate MCP content type instead of duplicating large payloads.
type FullReadResult struct {
	Path     string `json:"path"`
	Content  []byte `json:"-"`
	Size     int64  `json:"size_bytes"`
	MIMEType string `json:"mime_type"`
	SHA256   string `json:"sha256"`
}

// Read loads a bounded text window while hashing the same stream. It keeps
// only the requested lines instead of materializing the complete file.
func Read(opts ReadOptions) (ReadResult, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 1 << 20
	}
	if opts.Limit <= 0 {
		opts.Limit = 500
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	if _, err := Resolve(opts.WorkspaceRoot, opts.Path); err != nil {
		return ReadResult{}, err
	}
	root, err := os.OpenRoot(opts.WorkspaceRoot)
	if err != nil {
		return ReadResult{}, err
	}
	defer root.Close()
	st, err := root.Stat(opts.Path)
	if err != nil {
		return ReadResult{}, err
	}
	if st.IsDir() {
		return ReadResult{}, fmt.Errorf("is a directory")
	}
	if st.Size() > opts.MaxBytes*4 {
		return ReadResult{}, fmt.Errorf("file too large")
	}

	f, err := root.Open(opts.Path)
	if err != nil {
		return ReadResult{}, err
	}
	defer f.Close()
	endsWithNewline := false
	if st.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], st.Size()-1); err == nil {
			endsWithNewline = last[0] == '\n'
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return ReadResult{}, err
		}
	}

	hasher := sha256.New()
	reader := bufio.NewReaderSize(f, 64*1024)
	selected := make([]string, 0, opts.Limit)
	lineNumber := 0
	for {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			_, _ = hasher.Write(raw)
			line := raw
			if line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
			}
			if !utf8.Valid(line) {
				return ReadResult{}, fmt.Errorf("binary or non-utf8 content")
			}
			if lineNumber >= opts.Offset && lineNumber < opts.Offset+opts.Limit {
				selected = append(selected, string(line))
			}
			lineNumber++
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return ReadResult{}, readErr
		}
	}

	start := opts.Offset
	if start > lineNumber {
		start = lineNumber
	}
	chunk := strings.Join(selected, "\n")
	if len(selected) > 0 && (endsWithNewline || start+len(selected) < lineNumber) {
		chunk += "\n"
	}
	truncated := start+len(selected) < lineNumber
	if int64(len(chunk)) > opts.MaxBytes {
		chunk = truncateUTF8(chunk, int(opts.MaxBytes))
		truncated = true
	}
	digest := hasher.Sum(nil)
	return ReadResult{
		Path:       opts.Path,
		Content:    chunk,
		TotalLines: lineNumber,
		Offset:     start,
		Limit:      opts.Limit,
		Truncated:  truncated,
		SHA256:     "sha256:" + hex.EncodeToString(digest),
	}, nil
}

// ReadFull reads a complete regular file for an explicit client-preview
// request. It preserves binary bytes and rejects files larger than MaxBytes.
func ReadFull(opts FullReadOptions) (FullReadResult, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 1 << 20
	}
	if _, err := Resolve(opts.WorkspaceRoot, opts.Path); err != nil {
		return FullReadResult{}, err
	}
	root, err := os.OpenRoot(opts.WorkspaceRoot)
	if err != nil {
		return FullReadResult{}, err
	}
	defer root.Close()
	info, err := root.Stat(opts.Path)
	if err != nil {
		return FullReadResult{}, err
	}
	if info.IsDir() {
		return FullReadResult{}, fmt.Errorf("is a directory")
	}
	if info.Size() > opts.MaxBytes {
		return FullReadResult{}, fmt.Errorf("file too large for full read")
	}

	handle, err := root.Open(opts.Path)
	if err != nil {
		return FullReadResult{}, err
	}
	defer handle.Close()
	content, err := io.ReadAll(io.LimitReader(handle, opts.MaxBytes+1))
	if err != nil {
		return FullReadResult{}, err
	}
	if int64(len(content)) > opts.MaxBytes {
		return FullReadResult{}, fmt.Errorf("file too large for full read")
	}
	digest := sha256.Sum256(content)
	return FullReadResult{
		Path: opts.Path, Content: content, Size: int64(len(content)),
		MIMEType: detectMIME(opts.Path, content), SHA256: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func detectMIME(path string, content []byte) string {
	if detected := mime.TypeByExtension(filepath.Ext(path)); detected != "" {
		return normalizeMIME(detected)
	}
	return normalizeMIME(http.DetectContentType(content))
}

func normalizeMIME(detected string) string {
	if separator := strings.IndexByte(detected, ';'); separator >= 0 {
		detected = detected[:separator]
	}
	return strings.TrimSpace(detected)
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}
