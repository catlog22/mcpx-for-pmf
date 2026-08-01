package changeset

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mcpx/internal/file"
)

var (
	ErrNotFound      = errors.New("changeset not found")
	ErrConflict      = errors.New("changeset state conflict")
	ErrStaleRevision = errors.New("file revision is stale")
)

type Service struct {
	db          *sql.DB
	now         func() time.Time
	beforeApply func(FileChange) error
}

// partialApplyError reports that the requested filesystem mutation succeeded,
// but its durability check failed. The item must still enter the journal so a
// later rollback/recovery can restore it.
type partialApplyError struct {
	err error
}

func (e *partialApplyError) Error() string { return e.err.Error() }
func (e *partialApplyError) Unwrap() error { return e.err }

type Operation struct {
	Operation      string `json:"operation"`
	Path           string `json:"path"`
	NewPath        string `json:"new_path,omitempty"`
	BaseSHA256     string `json:"base_sha256,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	Patch          string `json:"patch,omitempty"`
	Content        string `json:"content,omitempty"`
	Match          string `json:"match,omitempty"` // for exact edit
	Occurrence     string `json:"occurrence,omitempty"`
	Replacement    string `json:"replacement,omitempty"`
	RangeStart     int    `json:"range_start,omitempty"`
	RangeEnd       int    `json:"range_end,omitempty"`
}

type FileChange struct {
	Ordinal        int    `json:"ordinal"`
	Operation      string `json:"operation"`
	Path           string `json:"path"`
	NewPath        string `json:"new_path,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	OriginalSHA256 string `json:"original_sha256,omitempty"`
	ProposedSHA256 string `json:"proposed_sha256,omitempty"`
	OriginalMode   uint32 `json:"-"`
	Original       []byte `json:"-"`
	Proposed       []byte `json:"-"`
}

type Changeset struct {
	ID                string       `json:"changeset_id"`
	RemoteSessionID   string       `json:"remote_session_id"`
	Status            string       `json:"status"`
	Summary           string       `json:"summary"`
	Digest            string       `json:"digest"`
	SourceChangesetID string       `json:"source_changeset_id,omitempty"`
	Files             []FileChange `json:"files"`
	UnifiedDiff       string       `json:"unified_diff,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	AppliedAt         *time.Time   `json:"applied_at,omitempty"`
}

type ApplyResult struct {
	ChangesetID string    `json:"changeset_id"`
	JournalID   string    `json:"journal_id"`
	Status      string    `json:"status"`
	AppliedAt   time.Time `json:"applied_at"`
	Files       int       `json:"files"`
}

func NewService(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

type PrepareOptions struct {
	// Transform runs on proposed bytes before the Changeset is persisted. It
	// must be pure with respect to the workspace; all filesystem writes remain
	// inside Apply so formatting cannot bypass the Changeset journal.
	Transform func(path string, content []byte) ([]byte, error)
}

func (s *Service) Prepare(ctx context.Context, remoteSessionID, principalID, workspaceRoot, summary string, operations []Operation) (Changeset, error) {
	return s.PrepareWithOptions(ctx, remoteSessionID, principalID, workspaceRoot, summary, operations, PrepareOptions{})
}

func (s *Service) PrepareWithOptions(ctx context.Context, remoteSessionID, principalID, workspaceRoot, summary string, operations []Operation, options PrepareOptions) (Changeset, error) {
	changeset, err := s.buildChangeset(remoteSessionID, workspaceRoot, summary, operations, options)
	if err != nil {
		return Changeset{}, err
	}
	if err := s.insert(ctx, changeset, principalID); err != nil {
		return Changeset{}, err
	}
	return changeset, nil
}

// PrepareIdempotentWithOptions atomically creates a Changeset and its request
// record. A retry therefore cannot observe a missing idempotency record after
// the Changeset has already been committed.
func (s *Service) PrepareIdempotentWithOptions(ctx context.Context, remoteSessionID, principalID, requestID, workspaceRoot, summary string, operations []Operation, options PrepareOptions) (Changeset, bool, error) {
	if requestID == "" {
		changeset, err := s.PrepareWithOptions(ctx, remoteSessionID, principalID, workspaceRoot, summary, operations, options)
		return changeset, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Changeset{}, false, err
	}
	defer tx.Rollback()

	var encoded string
	err = tx.QueryRowContext(ctx, `SELECT response_json FROM idempotency_records
        WHERE remote_session_id = ? AND principal_id = ? AND client_request_id = ?
          AND operation = 'change_execute' AND expires_at > ?`,
		remoteSessionID, principalID, requestID, s.now().UTC().UnixMilli()).Scan(&encoded)
	if err == nil {
		var reference struct {
			ChangesetID string `json:"changeset_id"`
		}
		if json.Unmarshal([]byte(encoded), &reference) != nil || reference.ChangesetID == "" {
			return Changeset{}, false, fmt.Errorf("invalid change idempotency record")
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return Changeset{}, false, err
		}
		item, getErr := s.Get(ctx, reference.ChangesetID)
		return item, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Changeset{}, false, err
	}

	changeset, err := s.buildChangeset(remoteSessionID, workspaceRoot, summary, operations, options)
	if err != nil {
		return Changeset{}, false, err
	}
	if err := s.insertTx(ctx, tx, changeset, principalID); err != nil {
		return Changeset{}, false, err
	}
	encodedBytes, err := json.Marshal(map[string]string{"changeset_id": changeset.ID})
	if err != nil {
		return Changeset{}, false, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records
        (remote_session_id, principal_id, client_request_id, operation, response_json, created_at, expires_at)
        VALUES (?, ?, ?, 'change_execute', ?, ?, ?)`,
		remoteSessionID, principalID, requestID, string(encodedBytes), now.UnixMilli(), now.Add(24*time.Hour).UnixMilli()); err != nil {
		return Changeset{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Changeset{}, false, err
	}
	return changeset, false, nil
}

func (s *Service) buildChangeset(remoteSessionID, workspaceRoot, summary string, operations []Operation, options PrepareOptions) (Changeset, error) {
	if remoteSessionID == "" || len(operations) == 0 {
		return Changeset{}, fmt.Errorf("remote_session_id and operations are required")
	}
	files := make([]FileChange, 0, len(operations))
	seen := map[string]bool{}
	for index, operation := range operations {
		prepared, err := prepareOperation(workspaceRoot, index, operation)
		if err != nil {
			return Changeset{}, fmt.Errorf("operation %d: %w", index, err)
		}
		if options.Transform != nil && (prepared.Operation == "update" || prepared.Operation == "create") {
			path := prepared.Path
			if prepared.NewPath != "" {
				path = prepared.NewPath
			}
			transformed, transformErr := options.Transform(path, append([]byte(nil), prepared.Proposed...))
			if transformErr != nil {
				return Changeset{}, fmt.Errorf("operation %d: transform %s: %w", index, path, transformErr)
			}
			prepared.Proposed = transformed
			prepared.ProposedSHA256 = hashBytes(transformed)
		}
		for _, path := range []string{prepared.Path, prepared.NewPath} {
			if path != "" {
				if seen[path] {
					return Changeset{}, fmt.Errorf("path %q appears more than once", path)
				}
				seen[path] = true
			}
		}
		files = append(files, prepared)
	}
	id, err := randomID("chg_", 16)
	if err != nil {
		return Changeset{}, err
	}
	digest := digestFiles(files)
	now := s.now().UTC()
	changeset := Changeset{ID: id, RemoteSessionID: remoteSessionID, Status: "draft", Summary: summary, Digest: digest, Files: files, CreatedAt: now}
	changeset.UnifiedDiff = unifiedDiff(files)
	return changeset, nil
}

func prepareOperation(workspaceRoot string, ordinal int, operation Operation) (FileChange, error) {
	op := strings.ToLower(strings.TrimSpace(operation.Operation))
	path := filepath.ToSlash(filepath.Clean(operation.Path))
	if path == "." || filepath.IsAbs(operation.Path) {
		return FileChange{}, fmt.Errorf("workspace-relative path required")
	}
	if _, err := file.Resolve(workspaceRoot, path); err != nil {
		return FileChange{}, err
	}
	expectedSHA256, err := expectedHash(operation)
	if err != nil {
		return FileChange{}, err
	}
	prepared := FileChange{Ordinal: ordinal, Operation: op, Path: path, ExpectedSHA256: expectedSHA256}
	exactOps := op == "replace_exact" || op == "insert_before" || op == "insert_after" || op == "delete_exact" || op == "replace_range"
	switch {
	case exactOps:
		if prepared.ExpectedSHA256 == "" {
			return FileChange{}, fmt.Errorf("base_sha256 required for %s", op)
		}
		content, mode, err := readExisting(workspaceRoot, path)
		if err != nil {
			return FileChange{}, err
		}
		prepared.Original, prepared.OriginalMode = content, mode
		prepared.OriginalSHA256 = hashBytes(content)
		if prepared.OriginalSHA256 != prepared.ExpectedSHA256 {
			return FileChange{}, fmt.Errorf("%w: %s", ErrStaleRevision, path)
		}
		proposed, err := applyExactEdit(content, operation)
		if err != nil {
			return FileChange{}, err
		}
		// Store as update so apply/diff pipelines treat it as content change.
		prepared.Operation = "update"
		prepared.Proposed = proposed
		prepared.ProposedSHA256 = hashBytes(proposed)
	case op == "update" || op == "rename" || op == "delete":
		if prepared.ExpectedSHA256 == "" {
			return FileChange{}, fmt.Errorf("expected_sha256 required for %s", op)
		}
		content, mode, err := readExisting(workspaceRoot, path)
		if err != nil {
			return FileChange{}, err
		}
		prepared.Original, prepared.OriginalMode = content, mode
		prepared.OriginalSHA256 = hashBytes(content)
		if prepared.OriginalSHA256 != prepared.ExpectedSHA256 {
			return FileChange{}, fmt.Errorf("%w: %s", ErrStaleRevision, path)
		}
	case op == "create":
		absolute, _ := file.Resolve(workspaceRoot, path)
		if _, err := os.Stat(absolute); !os.IsNotExist(err) {
			return FileChange{}, fmt.Errorf("create target already exists: %s", path)
		}
	default:
		return FileChange{}, fmt.Errorf("unsupported operation %q", op)
	}
	if op == "update" || op == "create" {
		if operation.Patch != "" {
			if op != "update" {
				return FileChange{}, fmt.Errorf("patch is only supported for update")
			}
			if operation.Content != "" {
				return FileChange{}, fmt.Errorf("content and patch cannot both be provided")
			}
			if !utf8.ValidString(operation.Patch) {
				return FileChange{}, fmt.Errorf("patch must be UTF-8")
			}
			proposed, err := applyUnifiedPatch(prepared.Original, operation.Patch)
			if err != nil {
				return FileChange{}, fmt.Errorf("%w: %s: %v", ErrStaleRevision, path, err)
			}
			prepared.Proposed = proposed
		} else {
			if !utf8.ValidString(operation.Content) {
				return FileChange{}, fmt.Errorf("content must be UTF-8")
			}
			// Normalize line endings to the target file's convention so a
			// small edit never rewrites every line ending of a CRLF file.
			prepared.Proposed = []byte(normalizeContentLineEndings(operation.Content, prepared.Original))
		}
		prepared.ProposedSHA256 = hashBytes(prepared.Proposed)
	}
	if op == "rename" {
		prepared.NewPath = filepath.ToSlash(filepath.Clean(operation.NewPath))
		if prepared.NewPath == "." || filepath.IsAbs(operation.NewPath) {
			return FileChange{}, fmt.Errorf("new_path required for rename")
		}
		absolute, err := file.Resolve(workspaceRoot, prepared.NewPath)
		if err != nil {
			return FileChange{}, err
		}
		if _, err := os.Stat(absolute); !os.IsNotExist(err) {
			return FileChange{}, fmt.Errorf("rename target already exists: %s", prepared.NewPath)
		}
		prepared.ProposedSHA256 = prepared.OriginalSHA256
	}
	return prepared, nil
}

func applyExactEdit(original []byte, operation Operation) ([]byte, error) {
	op := strings.ToLower(strings.TrimSpace(operation.Operation))
	if !utf8.Valid(original) {
		return nil, fmt.Errorf("target file must be UTF-8")
	}
	text := string(original)
	if op == "replace_range" {
		if operation.RangeStart < 0 || operation.RangeEnd < operation.RangeStart {
			return nil, fmt.Errorf("invalid range_start/range_end")
		}
		lineEnding := "\n"
		if strings.Contains(text, "\r\n") {
			lineEnding = "\r\n"
			text = strings.ReplaceAll(text, "\r\n", "\n")
		}
		lines := splitPatchContent(text)
		if operation.RangeEnd > len(lines) {
			return nil, fmt.Errorf("range_end exceeds file lines")
		}
		replacement := operation.Content
		if operation.Replacement != "" {
			replacement = operation.Replacement
		}
		if !utf8.ValidString(replacement) {
			return nil, fmt.Errorf("replacement must be UTF-8")
		}
		replLines := strings.Split(strings.ReplaceAll(replacement, "\r\n", "\n"), "\n")
		if len(replLines) > 0 && replLines[len(replLines)-1] == "" {
			replLines = replLines[:len(replLines)-1]
		}
		out := append([]string{}, lines[:operation.RangeStart]...)
		out = append(out, replLines...)
		out = append(out, lines[operation.RangeEnd:]...)
		joined := strings.Join(out, "\n")
		if strings.HasSuffix(text, "\n") && len(out) > 0 {
			joined += "\n"
		}
		if lineEnding != "\n" {
			joined = strings.ReplaceAll(joined, "\n", lineEnding)
		}
		return []byte(joined), nil
	}
	match := operation.Match
	if match == "" {
		return nil, fmt.Errorf("match is required for %s", op)
	}
	// Normalize the file and the caller-supplied match/replacement to \n so
	// exact edits work on CRLF files with LF-shaped arguments, then restore
	// the file's own line ending before writing.
	lineEnding := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnding = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	match = strings.ReplaceAll(match, "\r\n", "\n")
	count := strings.Count(text, match)
	if count == 0 {
		return nil, fmt.Errorf("no match for %q", match)
	}
	occurrence := strings.ToLower(strings.TrimSpace(operation.Occurrence))
	if occurrence == "" || occurrence == "one" {
		if count != 1 {
			return nil, fmt.Errorf("ambiguous_match: %q occurs %d times", match, count)
		}
	} else {
		return nil, fmt.Errorf("unsupported occurrence %q", occurrence)
	}
	replacement := operation.Content
	if operation.Replacement != "" {
		replacement = operation.Replacement
	}
	replacement = strings.ReplaceAll(replacement, "\r\n", "\n")
	var proposed string
	switch op {
	case "replace_exact":
		if !utf8.ValidString(replacement) {
			return nil, fmt.Errorf("replacement must be UTF-8")
		}
		proposed = strings.Replace(text, match, replacement, 1)
	case "insert_before":
		if !utf8.ValidString(replacement) {
			return nil, fmt.Errorf("content must be UTF-8")
		}
		proposed = strings.Replace(text, match, replacement+match, 1)
	case "insert_after":
		if !utf8.ValidString(replacement) {
			return nil, fmt.Errorf("content must be UTF-8")
		}
		proposed = strings.Replace(text, match, match+replacement, 1)
	case "delete_exact":
		proposed = strings.Replace(text, match, "", 1)
	default:
		return nil, fmt.Errorf("unsupported exact operation %q", op)
	}
	if lineEnding != "\n" {
		proposed = strings.ReplaceAll(proposed, "\n", lineEnding)
	}
	return []byte(proposed), nil
}

// normalizeContentLineEndings rewrites content to match the original file's
// line-ending convention (\n or \r\n). Mixed input is first reduced to \n so
// a full-file update never flips every line ending of a CRLF file.
func normalizeContentLineEndings(content string, original []byte) string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if strings.Contains(string(original), "\r\n") {
		return strings.ReplaceAll(normalized, "\n", "\r\n")
	}
	return normalized
}

func expectedHash(operation Operation) (string, error) {
	expected := normalizeHash(operation.ExpectedSHA256)
	base := normalizeHash(operation.BaseSHA256)
	if expected != "" && base != "" && expected != base {
		return "", fmt.Errorf("base_sha256 and expected_sha256 must match when both are provided")
	}
	if base != "" {
		return base, nil
	}
	return expected, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type patchLine struct {
	kind      byte
	text      string
	noNewline bool
}

type patchHunk struct {
	oldStart int
	oldCount int
	newCount int
	lines    []patchLine
}

// applyUnifiedPatch applies a textual Unified Diff body to a UTF-8 file. File
// headers are optional because the operation already names the target path.
func applyUnifiedPatch(original []byte, patch string) ([]byte, error) {
	if !utf8.Valid(original) {
		return nil, fmt.Errorf("target file must be UTF-8 for patch updates")
	}
	hunks, err := parsePatchHunks(patch)
	if err != nil {
		return nil, err
	}
	originalText := string(original)
	lineEnding := "\n"
	if strings.Contains(originalText, "\r\n") {
		lineEnding = "\r\n"
		originalText = strings.ReplaceAll(originalText, "\r\n", "\n")
	}
	originalEndsNewline := strings.HasSuffix(originalText, "\n")
	originalLines := splitPatchContent(originalText)
	result := make([]string, 0, len(originalLines))
	cursor := 0
	resultEndsNewline := originalEndsNewline
	for _, hunk := range hunks {
		start := hunk.oldStart - 1
		if hunk.oldStart == 0 {
			start = 0
		}
		if start < cursor || start > len(originalLines) {
			return nil, fmt.Errorf("hunk source range is outside the target file")
		}
		result = append(result, originalLines[cursor:start]...)
		cursor = start
		oldLines, newLines := 0, 0
		lastOutputFromHunk := false
		lastOutputNoNewline := false
		for _, line := range hunk.lines {
			switch line.kind {
			case ' ':
				if cursor >= len(originalLines) || originalLines[cursor] != line.text {
					return nil, fmt.Errorf("context does not match at line %d", cursor+1)
				}
				result = append(result, line.text)
				cursor++
				oldLines++
				newLines++
				lastOutputFromHunk, lastOutputNoNewline = true, line.noNewline
			case '-':
				if cursor >= len(originalLines) || originalLines[cursor] != line.text {
					return nil, fmt.Errorf("removed line does not match at line %d", cursor+1)
				}
				cursor++
				oldLines++
			case '+':
				result = append(result, line.text)
				newLines++
				lastOutputFromHunk, lastOutputNoNewline = true, line.noNewline
			}
		}
		if oldLines != hunk.oldCount || newLines != hunk.newCount {
			return nil, fmt.Errorf("hunk line counts do not match its header")
		}
		if cursor == len(originalLines) && lastOutputFromHunk {
			resultEndsNewline = !lastOutputNoNewline
		}
	}
	result = append(result, originalLines[cursor:]...)
	updated := strings.Join(result, "\n")
	if resultEndsNewline && len(result) > 0 {
		updated += "\n"
	}
	if lineEnding != "\n" {
		updated = strings.ReplaceAll(updated, "\n", lineEnding)
	}
	return []byte(updated), nil
}

func parsePatchHunks(patch string) ([]patchHunk, error) {
	if strings.TrimSpace(patch) == "" {
		return nil, fmt.Errorf("patch is required")
	}
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var hunks []patchHunk
	for index := 0; index < len(lines); {
		matches := hunkHeader.FindStringSubmatch(lines[index])
		if matches == nil {
			if len(hunks) == 0 && (strings.HasPrefix(lines[index], "diff ") || strings.HasPrefix(lines[index], "index ") || strings.HasPrefix(lines[index], "--- ") || strings.HasPrefix(lines[index], "+++ ")) {
				index++
				continue
			}
			return nil, fmt.Errorf("expected Unified Diff hunk header")
		}
		oldStart, oldCount, err := parseHunkRange(matches[1], matches[2])
		if err != nil {
			return nil, err
		}
		_, newCount, err := parseHunkRange(matches[3], matches[4])
		if err != nil {
			return nil, err
		}
		hunk := patchHunk{oldStart: oldStart, oldCount: oldCount, newCount: newCount}
		index++
		for index < len(lines) && !strings.HasPrefix(lines[index], "@@ ") {
			line := lines[index]
			if line == `\ No newline at end of file` {
				if len(hunk.lines) == 0 {
					return nil, fmt.Errorf("newline marker has no preceding patch line")
				}
				hunk.lines[len(hunk.lines)-1].noNewline = true
				index++
				continue
			}
			if len(line) == 0 || (line[0] != ' ' && line[0] != '+' && line[0] != '-') {
				return nil, fmt.Errorf("invalid Unified Diff line")
			}
			hunk.lines = append(hunk.lines, patchLine{kind: line[0], text: line[1:]})
			index++
		}
		hunks = append(hunks, hunk)
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("patch contains no hunks")
	}
	return hunks, nil
}

func parseHunkRange(startText, countText string) (int, int, error) {
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid Unified Diff hunk range")
	}
	count := 1
	if countText != "" {
		count, err = strconv.Atoi(countText)
		if err != nil || count < 0 {
			return 0, 0, fmt.Errorf("invalid Unified Diff hunk range")
		}
	}
	return start, count, nil
}

func splitPatchContent(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func (s *Service) FindIdempotent(ctx context.Context, remoteSessionID, principalID, requestID string) (Changeset, bool, error) {
	if remoteSessionID == "" || principalID == "" || requestID == "" {
		return Changeset{}, false, nil
	}
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT response_json FROM idempotency_records
        WHERE remote_session_id = ? AND principal_id = ? AND client_request_id = ?
          AND operation = 'change_execute' AND expires_at > ?`,
		remoteSessionID, principalID, requestID, s.now().UTC().UnixMilli()).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return Changeset{}, false, nil
	}
	if err != nil {
		return Changeset{}, false, err
	}
	var reference struct {
		ChangesetID string `json:"changeset_id"`
	}
	if err := json.Unmarshal([]byte(encoded), &reference); err != nil || reference.ChangesetID == "" {
		return Changeset{}, false, fmt.Errorf("invalid change idempotency record")
	}
	item, err := s.Get(ctx, reference.ChangesetID)
	if err != nil {
		return Changeset{}, false, err
	}
	return item, true, nil
}

func (s *Service) Get(ctx context.Context, changesetID string) (Changeset, error) {
	var result Changeset
	var createdAt int64
	var appliedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, remote_session_id, status, summary, digest,
        COALESCE(source_changeset_id,''), created_at, applied_at FROM changesets WHERE id = ?`, changesetID).Scan(
		&result.ID, &result.RemoteSessionID, &result.Status, &result.Summary, &result.Digest,
		&result.SourceChangesetID, &createdAt, &appliedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Changeset{}, ErrNotFound
	}
	if err != nil {
		return Changeset{}, err
	}
	result.CreatedAt = time.UnixMilli(createdAt).UTC()
	if appliedAt.Valid {
		value := time.UnixMilli(appliedAt.Int64).UTC()
		result.AppliedAt = &value
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ordinal, operation, path, new_path, expected_sha256,
        original_sha256, proposed_sha256, original_mode, original_content, proposed_content
        FROM changeset_files WHERE changeset_id = ? ORDER BY ordinal`, changesetID)
	if err != nil {
		return Changeset{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item FileChange
		if err := rows.Scan(&item.Ordinal, &item.Operation, &item.Path, &item.NewPath, &item.ExpectedSHA256,
			&item.OriginalSHA256, &item.ProposedSHA256, &item.OriginalMode, &item.Original, &item.Proposed); err != nil {
			return Changeset{}, err
		}
		result.Files = append(result.Files, item)
	}
	result.UnifiedDiff = unifiedDiff(result.Files)
	return result, rows.Err()
}

type applyJournal struct {
	ChangesetID     string `json:"changeset_id"`
	AppliedOrdinals []int  `json:"applied_ordinals"`
}

func marshalApplyJournal(changesetID string, applied []FileChange) ([]byte, error) {
	ordinals := make([]int, 0, len(applied))
	for _, item := range applied {
		ordinals = append(ordinals, item.Ordinal)
	}
	return json.Marshal(applyJournal{ChangesetID: changesetID, AppliedOrdinals: ordinals})
}

func (s *Service) updateJournalProgress(ctx context.Context, journalID, changesetID string, applied []FileChange) error {
	journal, err := marshalApplyJournal(changesetID, applied)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE change_journals SET journal_json = ?, updated_at = ? WHERE id = ? AND status = 'applying'`,
		string(journal), s.now().UTC().UnixMilli(), journalID)
	return err
}

func (s *Service) Apply(ctx context.Context, changesetID, workspaceRoot string) (ApplyResult, error) {
	changeset, err := s.Get(ctx, changesetID)
	if err != nil {
		return ApplyResult{}, err
	}
	if changeset.Status != "draft" {
		return ApplyResult{}, ErrConflict
	}
	if err := preflight(workspaceRoot, changeset.Files); err != nil {
		return ApplyResult{}, err
	}
	journalID, err := randomID("jrnl_", 12)
	if err != nil {
		return ApplyResult{}, err
	}
	now := s.now().UTC()
	journal, _ := marshalApplyJournal(changeset.ID, nil)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO change_journals(id, changeset_id, status, journal_json, created_at, updated_at)
		VALUES (?, ?, 'applying', ?, ?, ?)`, journalID, changeset.ID, string(journal), now.UnixMilli(), now.UnixMilli()); err != nil {
		return ApplyResult{}, fmt.Errorf("%w: Changeset application already claimed", ErrConflict)
	}
	applied := make([]FileChange, 0, len(changeset.Files))
	for _, item := range changeset.Files {
		if s.beforeApply != nil {
			if err := s.beforeApply(item); err != nil {
				return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied,
					fmt.Errorf("before apply %s: %w", item.Path, err))
			}
		}
		if err := applyOne(workspaceRoot, item); err != nil {
			var partialErr *partialApplyError
			if errors.As(err, &partialErr) {
				applied = append(applied, item)
				if progressErr := s.updateJournalProgress(ctx, journalID, changeset.ID, applied); progressErr != nil {
					return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied,
						fmt.Errorf("record partially applied %s: %w", item.Path, progressErr))
				}
			}
			return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied,
				fmt.Errorf("apply %s: %w", item.Path, err))
		}
		applied = append(applied, item)
		if err := s.updateJournalProgress(ctx, journalID, changeset.ID, applied); err != nil {
			return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied,
				fmt.Errorf("record apply progress: %w", err))
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied, err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE changesets SET status = 'applied', applied_at = ? WHERE id = ? AND status = 'draft'`, now.UnixMilli(), changeset.ID)
	if err != nil {
		_ = tx.Rollback()
		return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied, err)
	}
	rows, _ := res.RowsAffected()
	if rows != 1 {
		_ = tx.Rollback()
		return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied, ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE change_journals SET status = 'completed', updated_at = ? WHERE id = ?`, now.UnixMilli(), journalID); err != nil {
		_ = tx.Rollback()
		return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied, err)
	}
	if changeset.SourceChangesetID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE changesets SET status = 'reverted' WHERE id = ? AND status = 'applied'`, changeset.SourceChangesetID); err != nil {
			_ = tx.Rollback()
			return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ApplyResult{}, s.abortApply(ctx, journalID, changeset.ID, workspaceRoot, applied, err)
	}
	return ApplyResult{ChangesetID: changeset.ID, JournalID: journalID, Status: "applied", AppliedAt: now, Files: len(applied)}, nil
}

func (s *Service) abortApply(ctx context.Context, journalID, changesetID, workspaceRoot string, applied []FileChange, cause error) error {
	rollbackErr := rollback(workspaceRoot, applied)
	status := "rolled_back"
	if rollbackErr != nil {
		// Keep the journal recoverable. Recover only scans applying journals,
		// so marking this failed would strand any successfully applied files.
		status = "applying"
	}
	stateErr := s.markApplyFailure(ctx, journalID, changesetID, status)
	if rollbackErr == nil && stateErr == nil {
		return cause
	}
	return fmt.Errorf("%w (rollback: %v; state: %v)", cause, rollbackErr, stateErr)
}

func (s *Service) markApplyFailure(ctx context.Context, journalID, changesetID, journalStatus string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE change_journals SET status = ?, updated_at = ? WHERE id = ?`,
		journalStatus, s.now().UTC().UnixMilli(), journalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE changesets SET status = 'failed' WHERE id = ? AND status = 'draft'`, changesetID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) Recover(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT j.id, j.changeset_id, j.journal_json, rs.workspace_path
        FROM change_journals j
        JOIN changesets c ON c.id = j.changeset_id
        JOIN remote_sessions rs ON rs.id = c.remote_session_id
        WHERE j.status = 'applying' ORDER BY j.created_at, j.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pendingJournal struct {
		id, changesetID, journalJSON, workspaceRoot string
	}
	pending := make([]pendingJournal, 0)
	for rows.Next() {
		var item pendingJournal
		if err := rows.Scan(&item.id, &item.changesetID, &item.journalJSON, &item.workspaceRoot); err != nil {
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	var firstErr error
	for _, item := range pending {
		if err := s.recoverJournal(ctx, item.id, item.changesetID, item.journalJSON, item.workspaceRoot); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) recoverJournal(ctx context.Context, journalID, changesetID, journalJSON, workspaceRoot string) error {
	var journal applyJournal
	if err := json.Unmarshal([]byte(journalJSON), &journal); err != nil {
		return s.markRecoveryFailed(ctx, journalID, changesetID, fmt.Errorf("decode journal: %w", err))
	}
	changeset, err := s.Get(ctx, changesetID)
	if err != nil {
		return s.markRecoveryFailed(ctx, journalID, changesetID, err)
	}
	applied := make([]FileChange, 0, len(changeset.Files))
	for _, item := range changeset.Files {
		wasApplied, inferErr := inferApplied(workspaceRoot, item)
		if inferErr != nil {
			return s.markRecoveryFailed(ctx, journalID, changesetID, inferErr)
		}
		if wasApplied {
			applied = append(applied, item)
		}
	}
	if err := rollback(workspaceRoot, applied); err != nil {
		return s.markRecoveryFailed(ctx, journalID, changesetID, fmt.Errorf("rollback journal %s: %w", journalID, err))
	}
	return s.markRecoveryFailed(ctx, journalID, changesetID, nil)
}

func inferApplied(workspaceRoot string, item FileChange) (bool, error) {
	path, err := file.Resolve(workspaceRoot, item.Path)
	if err != nil {
		return false, err
	}
	switch item.Operation {
	case "update":
		content, _, err := readExisting(workspaceRoot, item.Path)
		if err != nil {
			return false, err
		}
		switch hashBytes(content) {
		case item.ProposedSHA256:
			return true, nil
		case item.OriginalSHA256:
			return false, nil
		default:
			return false, fmt.Errorf("external modification detected for %s", item.Path)
		}
	case "create":
		content, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if hashBytes(content) != item.ProposedSHA256 {
			return false, fmt.Errorf("external modification detected for %s", item.Path)
		}
		return true, nil
	case "delete":
		content, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if hashBytes(content) == item.OriginalSHA256 {
			return false, nil
		}
		return false, fmt.Errorf("external modification detected for %s", item.Path)
	case "rename":
		target, err := file.Resolve(workspaceRoot, item.NewPath)
		if err != nil {
			return false, err
		}
		sourceInfo, sourceErr := os.Stat(path)
		targetContent, targetErr := os.ReadFile(target)
		if os.IsNotExist(sourceErr) && targetErr == nil && hashBytes(targetContent) == item.ProposedSHA256 {
			return true, nil
		}
		if sourceErr == nil && os.IsNotExist(targetErr) && sourceInfo.Mode().IsRegular() {
			content, readErr := os.ReadFile(path)
			if readErr == nil && hashBytes(content) == item.OriginalSHA256 {
				return false, nil
			}
		}
		return false, fmt.Errorf("cannot determine recovery state for %s", item.Path)
	default:
		return false, fmt.Errorf("unsupported recovery operation %q", item.Operation)
	}
}

func (s *Service) markRecoveryFailed(ctx context.Context, journalID, changesetID string, recoveryErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE change_journals SET status = ?, updated_at = ? WHERE id = ?`,
		map[bool]string{true: "failed", false: "rolled_back"}[recoveryErr != nil], s.now().UTC().UnixMilli(), journalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE changesets SET status = 'failed' WHERE id = ? AND status = 'draft'`, changesetID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return recoveryErr
}

func (s *Service) PrepareRevert(ctx context.Context, changesetID, principalID, workspaceRoot string) (Changeset, error) {
	source, err := s.Get(ctx, changesetID)
	if err != nil {
		return Changeset{}, err
	}
	if source.Status != "applied" {
		return Changeset{}, ErrConflict
	}
	operations := make([]Operation, 0, len(source.Files))
	for index := len(source.Files) - 1; index >= 0; index-- {
		item := source.Files[index]
		switch item.Operation {
		case "update":
			operations = append(operations, Operation{Operation: "update", Path: item.Path, ExpectedSHA256: item.ProposedSHA256, Content: string(item.Original)})
		case "create":
			operations = append(operations, Operation{Operation: "delete", Path: item.Path, ExpectedSHA256: item.ProposedSHA256})
		case "delete":
			operations = append(operations, Operation{Operation: "create", Path: item.Path, Content: string(item.Original)})
		case "rename":
			operations = append(operations, Operation{Operation: "rename", Path: item.NewPath, NewPath: item.Path, ExpectedSHA256: item.ProposedSHA256})
		}
	}
	revert, err := s.Prepare(ctx, source.RemoteSessionID, principalID, workspaceRoot, "Revert "+source.ID, operations)
	if err != nil {
		return Changeset{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE changesets SET source_changeset_id = ? WHERE id = ?`, source.ID, revert.ID)
	if err != nil {
		return Changeset{}, err
	}
	revert.SourceChangesetID = source.ID
	return revert, nil
}

func (s *Service) History(ctx context.Context, remoteSessionID string, limit int) ([]Changeset, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, status, summary, digest,
        COALESCE(source_changeset_id,''), created_at, applied_at
        FROM changesets WHERE remote_session_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, remoteSessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Changeset, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var item Changeset
		var createdAt int64
		var appliedAt sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Status, &item.Summary, &item.Digest, &item.SourceChangesetID, &createdAt, &appliedAt); err != nil {
			return nil, err
		}
		item.RemoteSessionID = remoteSessionID
		item.CreatedAt = time.UnixMilli(createdAt).UTC()
		if appliedAt.Valid {
			value := time.UnixMilli(appliedAt.Int64).UTC()
			item.AppliedAt = &value
		}
		result = append(result, item)
		ids = append(ids, item.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return result, nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	fileRows, err := s.db.QueryContext(ctx, `SELECT changeset_id, ordinal, operation, path, new_path,
        expected_sha256, original_sha256, proposed_sha256, original_mode
        FROM changeset_files WHERE changeset_id IN (`+placeholders+`) ORDER BY changeset_id, ordinal`, args...)
	if err != nil {
		return nil, err
	}
	defer fileRows.Close()
	byID := make(map[string][]FileChange, len(ids))
	for fileRows.Next() {
		var changesetID string
		var item FileChange
		if err := fileRows.Scan(&changesetID, &item.Ordinal, &item.Operation, &item.Path, &item.NewPath,
			&item.ExpectedSHA256, &item.OriginalSHA256, &item.ProposedSHA256, &item.OriginalMode); err != nil {
			return nil, err
		}
		byID[changesetID] = append(byID[changesetID], item)
	}
	if err := fileRows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Files = byID[result[index].ID]
	}
	return result, nil
}

func (s *Service) insert(ctx context.Context, changeset Changeset, principalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.insertTx(ctx, tx, changeset, principalID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) insertTx(ctx context.Context, tx *sql.Tx, changeset Changeset, principalID string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO changesets
        (id, remote_session_id, created_by, status, summary, digest, source_changeset_id, created_at)
        VALUES (?, ?, ?, 'draft', ?, ?, ?, ?)`, changeset.ID, changeset.RemoteSessionID, principalID,
		changeset.Summary, changeset.Digest, nullable(changeset.SourceChangesetID), changeset.CreatedAt.UnixMilli()); err != nil {
		return err
	}
	for _, item := range changeset.Files {
		if _, err := tx.ExecContext(ctx, `INSERT INTO changeset_files
            (changeset_id, ordinal, operation, path, new_path, expected_sha256, original_sha256,
             proposed_sha256, original_mode, original_content, proposed_content)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, changeset.ID, item.Ordinal, item.Operation,
			item.Path, item.NewPath, item.ExpectedSHA256, item.OriginalSHA256, item.ProposedSHA256,
			item.OriginalMode, nullableBytes(item.Original), nullableBytes(item.Proposed)); err != nil {
			return err
		}
	}
	return nil
}

func preflight(workspaceRoot string, files []FileChange) error {
	for _, item := range files {
		absolute, err := file.Resolve(workspaceRoot, item.Path)
		if err != nil {
			return err
		}
		if item.Operation == "create" {
			if _, err := os.Stat(absolute); !os.IsNotExist(err) {
				return fmt.Errorf("%w: create target %s", ErrStaleRevision, item.Path)
			}
			continue
		}
		content, _, err := readExisting(workspaceRoot, item.Path)
		if err != nil || hashBytes(content) != item.ExpectedSHA256 {
			return fmt.Errorf("%w: %s", ErrStaleRevision, item.Path)
		}
		if item.Operation == "rename" {
			target, err := file.Resolve(workspaceRoot, item.NewPath)
			if err != nil {
				return err
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				return fmt.Errorf("%w: rename target %s", ErrStaleRevision, item.NewPath)
			}
		}
	}
	return nil
}

func applyOne(workspaceRoot string, item FileChange) error {
	path, err := file.Resolve(workspaceRoot, item.Path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", item.Path, err)
	}
	switch item.Operation {
	case "update":
		return atomicWrite(path, item.Proposed, os.FileMode(item.OriginalMode))
	case "create":
		return atomicWrite(path, item.Proposed, 0o644)
	case "delete":
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return &partialApplyError{err: err}
		}
		return nil
	case "rename":
		target, err := file.Resolve(workspaceRoot, item.NewPath)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", item.NewPath, err)
		}
		if err := os.Rename(path, target); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return &partialApplyError{err: err}
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return &partialApplyError{err: err}
		}
		return nil
	default:
		return fmt.Errorf("unsupported operation %q", item.Operation)
	}
}

func rollback(workspaceRoot string, applied []FileChange) error {
	var failures []string
	for index := len(applied) - 1; index >= 0; index-- {
		item := applied[index]
		path, resolveErr := file.Resolve(workspaceRoot, item.Path)
		if resolveErr != nil {
			failures = append(failures, item.Path+": "+resolveErr.Error())
			continue
		}
		var err error
		switch item.Operation {
		case "update":
			if err = ensureCurrentHash(workspaceRoot, item.Path, item.ProposedSHA256); err == nil {
				err = atomicWrite(path, item.Original, os.FileMode(item.OriginalMode))
			}
		case "delete":
			if _, statErr := os.Stat(path); statErr == nil {
				err = fmt.Errorf("external file appeared after delete")
			} else if !os.IsNotExist(statErr) {
				err = statErr
			} else {
				err = atomicWrite(path, item.Original, os.FileMode(item.OriginalMode))
			}
		case "create":
			if err = ensureCurrentHash(workspaceRoot, item.Path, item.ProposedSHA256); err == nil {
				err = os.Remove(path)
			}
		case "rename":
			target, resolveErr := file.Resolve(workspaceRoot, item.NewPath)
			if resolveErr != nil {
				err = resolveErr
				break
			}
			if err = ensureCurrentHash(workspaceRoot, item.NewPath, item.ProposedSHA256); err == nil {
				if _, statErr := os.Stat(path); statErr == nil {
					err = fmt.Errorf("original path recreated externally")
				} else if !os.IsNotExist(statErr) {
					err = statErr
				} else {
					err = os.Rename(target, path)
				}
			}
		}
		if err != nil {
			failures = append(failures, item.Path+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func ensureCurrentHash(workspaceRoot, path, expected string) error {
	content, _, err := readExisting(workspaceRoot, path)
	if err != nil {
		return err
	}
	if hashBytes(content) != expected {
		return fmt.Errorf("external modification detected")
	}
	return nil
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mcpx-change-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if mode == 0 {
		mode = 0o644
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func readExisting(workspaceRoot, path string) ([]byte, uint32, error) {
	absolute, err := file.Resolve(workspaceRoot, path)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, 0, fmt.Errorf("file must be regular and at most 4 MiB")
	}
	content, err := os.ReadFile(absolute)
	return content, uint32(info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)), err
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeHash(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "" && !strings.HasPrefix(value, "sha256:") {
		value = "sha256:" + value
	}
	return value
}

func digestFiles(files []FileChange) string {
	items := make([]string, 0, len(files))
	for _, item := range files {
		items = append(items, fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%s", item.Ordinal, item.Operation, item.Path, item.NewPath, item.OriginalSHA256, item.ProposedSHA256))
	}
	sort.Strings(items)
	return hashBytes([]byte(strings.Join(items, "\n")))
}

func unifiedDiff(files []FileChange) string {
	var builder strings.Builder
	for _, item := range files {
		oldPath, newPath := "a/"+item.Path, "b/"+item.Path
		oldContent, newContent := item.Original, item.Proposed
		switch item.Operation {
		case "create":
			oldPath = "/dev/null"
		case "delete":
			newPath = "/dev/null"
		case "rename":
			newPath, newContent = "b/"+item.NewPath, item.Original
		}
		builder.WriteString(diffFile(oldPath, newPath, oldContent, newContent))
	}
	return builder.String()
}

func diffFile(oldPath, newPath string, oldContent, newContent []byte) string {
	oldLines, newLines := splitLines(string(oldContent)), splitLines(string(newContent))
	if oldPath == newPath && string(oldContent) == string(newContent) {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- %s\n+++ %s\n", oldPath, newPath)
	fmt.Fprintf(&builder, "@@ -%d,%d +%d,%d @@\n", firstLine(len(oldLines)), len(oldLines), firstLine(len(newLines)), len(newLines))
	for _, line := range oldLines {
		builder.WriteString("-" + line + "\n")
	}
	for _, line := range newLines {
		builder.WriteString("+" + line + "\n")
	}
	return builder.String()
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

func firstLine(count int) int {
	if count == 0 {
		return 0
	}
	return 1
}

func randomID(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if value == nil {
		return nil
	}
	return value
}
