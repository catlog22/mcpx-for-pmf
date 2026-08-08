package source

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// SmartQueryOptions controls the exact/token search gateway.
type SmartQueryOptions struct {
	Query           string
	Mode            string
	Parallel        bool
	MaxResults      int
	Cursor          string
	Pattern         string
	ExcludePattern  string
	ContextBefore   int
	ContextAfter    int
	MaxBytesPerFile int64
	IncludeSHA256   bool
	ScopePaths      []string
	Allowed         func(string) bool
}

type smartDocument struct {
	Path    string
	Content string
	Title   string
	SHA256  string
}

type smartCandidate struct {
	Path    string
	Score   int
	Sources map[string]bool
	Matches map[string]bool
}

// SmartQueryPage runs exact and token recall, then merges the ranked files.
// The analyzer is local and deterministic; a model-backed QueryAnalyzer can
// be introduced later without changing the search or response contracts.
func SmartQueryPage(root string, opts SmartQueryOptions) (map[string]any, error) {
	if strings.TrimSpace(opts.Query) == "" {
		return nil, fmt.Errorf("query required")
	}
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode == "" {
		mode = "smart"
	}
	if mode != "smart" && mode != "exact" && mode != "token" {
		return nil, fmt.Errorf("unsupported query mode %q", mode)
	}
	if opts.MaxResults <= 0 || opts.MaxResults > 20 {
		opts.MaxResults = 20
	}
	if opts.MaxBytesPerFile <= 0 {
		opts.MaxBytesPerFile = 64 << 10
	}
	analysis := AnalyzeQuery(opts.Query)
	allowed := opts.Allowed
	if len(opts.ScopePaths) > 0 {
		allowed = ScopePathFilter(root, opts.ScopePaths, allowed)
	}
	paths, err := paths(root, opts.Pattern, opts.ExcludePattern, allowed)
	if err != nil {
		return nil, err
	}
	documents := loadSmartDocuments(root, paths)

	runExact := mode == "smart" || mode == "exact"
	runToken := mode == "smart" || mode == "token"
	var exactCandidates, tokenCandidates []smartCandidate
	if opts.Parallel && runExact && runToken {
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			exactCandidates = exactRecall(opts.Query, documents)
		}()
		go func() {
			defer wait.Done()
			tokenCandidates = tokenRecall(analysis, documents)
		}()
		wait.Wait()
	} else {
		if runExact {
			exactCandidates = exactRecall(opts.Query, documents)
		}
		if runToken {
			tokenCandidates = tokenRecall(analysis, documents)
		}
	}

	merged := make(map[string]*smartCandidate, len(exactCandidates)+len(tokenCandidates))
	for _, candidate := range append(exactCandidates, tokenCandidates...) {
		mergeSmartCandidate(merged, candidate)
	}
	ordered := make([]*smartCandidate, 0, len(merged))
	for _, candidate := range merged {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		if ordered[i].Sources["exact"] != ordered[j].Sources["exact"] {
			return ordered[i].Sources["exact"]
		}
		return ordered[i].Path < ordered[j].Path
	})

	start := decodeCursor(opts.Cursor)
	if start > len(ordered) {
		start = len(ordered)
	}
	end := start + opts.MaxResults
	if end > len(ordered) {
		end = len(ordered)
	}
	files := make([]map[string]any, 0, end-start)
	totalBytes := 0
	for _, candidate := range ordered[start:end] {
		read, readErr := Read(root, candidate.Path, 0, 120, opts.MaxBytesPerFile)
		entry := map[string]any{
			"path": candidate.Path, "score": candidate.Score,
			"source": smartSources(candidate.Sources), "matches": smartMatches(candidate.Matches),
			"metadata": smartMetadata(candidate.Path),
		}
		if readErr != nil {
			entry["ok"] = false
			entry["error"] = readErr.Error()
		} else {
			entry["ok"] = true
			entry["content"] = read.Content
			entry["offset"] = read.Offset
			entry["limit"] = read.Limit
			entry["total_lines"] = read.TotalLines
			entry["truncated"] = read.Truncated
			totalBytes += len(read.Content)
			if opts.IncludeSHA256 {
				entry["sha256"] = read.SHA256
			}
		}
		files = append(files, entry)
	}
	result := map[string]any{
		"query": opts.Query, "mode": mode, "parallel": opts.Parallel,
		"analysis": analysis, "files": files, "total_bytes": totalBytes,
		"truncated": end < len(ordered), "max_results": opts.MaxResults,
	}
	if end < len(ordered) {
		result["next_cursor"] = encodeCursor(end)
	}
	return result, nil
}

// ScopePathFilter turns user-provided paths into a hard file boundary. A
// directory seed includes only that directory's descendants; a file seed
// includes only that file. Invalid or missing seeds match nothing instead of
// silently widening the query back to the workspace root.
func ScopePathFilter(root string, seeds []string, allowed func(string) bool) func(string) bool {
	scopes := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			continue
		}
		normalized := filepath.ToSlash(filepath.Clean(seed))
		if normalized == "." {
			scopes = append(scopes, "")
			continue
		}
		absolute, err := resolvePath(root, normalized)
		if err != nil {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil {
			continue
		}
		if info.IsDir() {
			scopes = append(scopes, strings.TrimSuffix(normalized, "/"))
			continue
		}
		scopes = append(scopes, normalized)
	}
	return func(path string) bool {
		if allowed != nil && !allowed(path) {
			return false
		}
		path = filepath.ToSlash(filepath.Clean(path))
		for _, scope := range scopes {
			if scope == "" || path == scope || strings.HasPrefix(path, scope+"/") {
				return true
			}
		}
		return false
	}
}

func loadSmartDocuments(root string, filePaths []string) []smartDocument {
	documents := make([]smartDocument, 0, len(filePaths))
	for _, path := range filePaths {
		absolute, err := resolvePath(root, path)
		if err != nil {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil || info.IsDir() || info.Size() > 2<<20 {
			continue
		}
		content, err := os.ReadFile(absolute)
		if err != nil || !utf8.Valid(content) {
			continue
		}
		text := string(content)
		documents = append(documents, smartDocument{Path: path, Content: text, Title: documentTitle(path, text), SHA256: digest(content)})
	}
	return documents
}

func resolvePath(root, path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if absolute != rootAbs && !strings.HasPrefix(absolute, rootAbs+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return absolute, nil
}

func exactRecall(query string, documents []smartDocument) []smartCandidate {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	normalizedQuery := normalizeIdentifier(query)
	result := make([]smartCandidate, 0)
	for _, document := range documents {
		score := 0
		matches := map[string]bool{}
		pathLower := strings.ToLower(document.Path)
		pathNormalized := normalizeIdentifier(document.Path)
		contentLower := strings.ToLower(document.Content)
		contentNormalized := normalizeIdentifier(document.Content)
		if queryLower != "" && strings.Contains(pathLower, queryLower) {
			score = maxInt(score, 100)
			matches[query] = true
		}
		if normalizedQuery != "" && strings.Contains(pathNormalized, normalizedQuery) {
			score = maxInt(score, 100)
			matches[query] = true
		}
		if queryLower != "" && strings.Contains(contentLower, queryLower) {
			score = maxInt(score, 100)
			matches[query] = true
		}
		if normalizedQuery != "" && strings.Contains(contentNormalized, normalizedQuery) {
			score = maxInt(score, 100)
			matches[query] = true
		}
		if score > 0 {
			result = append(result, smartCandidate{Path: document.Path, Score: score, Sources: map[string]bool{"exact": true}, Matches: matches})
		}
	}
	return result
}

func tokenRecall(analysis QueryAnalysis, documents []smartDocument) []smartCandidate {
	result := make([]smartCandidate, 0)
	for _, document := range documents {
		score := 0
		matches := map[string]bool{}
		title := strings.ToLower(document.Title)
		body := strings.ToLower(document.Content)
		path := normalizeIdentifier(document.Path)
		for _, phrase := range analysis.Phrases {
			if strings.Contains(title, strings.ToLower(phrase)) || strings.Contains(path, normalizeIdentifier(phrase)) {
				score += 80
				matches[phrase] = true
			} else if strings.Contains(body, strings.ToLower(phrase)) {
				score += 50
				matches[phrase] = true
			}
		}
		for _, token := range append(analysis.Tokens, analysis.TechnicalTerms...) {
			if strings.Contains(title, strings.ToLower(token)) || strings.Contains(path, normalizeIdentifier(token)) {
				score += 20
				matches[token] = true
			} else if strings.Contains(body, strings.ToLower(token)) || strings.Contains(normalizeIdentifier(body), normalizeIdentifier(token)) {
				score += 5
				matches[token] = true
			}
		}
		if score > 0 {
			result = append(result, smartCandidate{Path: document.Path, Score: score, Sources: map[string]bool{"token": true}, Matches: matches})
		}
	}
	return result
}

func mergeSmartCandidate(merged map[string]*smartCandidate, candidate smartCandidate) {
	current, ok := merged[candidate.Path]
	if !ok {
		copyCandidate := candidate
		copyCandidate.Sources = map[string]bool{}
		copyCandidate.Matches = map[string]bool{}
		merged[candidate.Path] = &copyCandidate
		current = &copyCandidate
	}
	current.Score += candidate.Score
	for source := range candidate.Sources {
		current.Sources[source] = true
	}
	for match := range candidate.Matches {
		current.Matches[match] = true
	}
}

func smartSources(sources map[string]bool) []string {
	result := make([]string, 0, 2)
	for _, source := range []string{"exact", "token"} {
		if sources[source] {
			result = append(result, source)
		}
	}
	return result
}

func smartMatches(matches map[string]bool) []string {
	result := make([]string, 0, len(matches))
	for match := range matches {
		result = append(result, match)
	}
	sort.Strings(result)
	return result
}

func smartMetadata(path string) map[string]any {
	ext := strings.ToLower(filepath.Ext(path))
	language := strings.TrimPrefix(ext, ".")
	typ := "source"
	if ext == ".md" || ext == ".markdown" {
		typ = "markdown"
		if strings.Contains(strings.ToLower(path), "/prd/") {
			typ = "prd"
		}
	}
	return map[string]any{"type": typ, "language": language}
}

func documentTitle(path, content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func normalizeIdentifier(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
