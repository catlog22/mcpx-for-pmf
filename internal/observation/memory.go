package observation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidMemoryQuery = errors.New("invalid workspace memory query")

const (
	DefaultMemoryLatest = 10
	MaxMemoryLatest     = 50
	maxMemoryRangeTerms = 128
)

// MemoryQuery contains the model-facing filters. ID and Time keep their
// compact expression form until the store validates and compiles them.
type MemoryQuery struct {
	Workspace string
	Keyword   string
	ID        string
	Time      string
	Latest    int
	Location  *time.Location
}

type MemoryPage struct {
	Items   []MemoryItem `json:"items"`
	Total   int          `json:"total"`
	HasMore bool         `json:"has_more"`
}

type MemoryItem struct {
	ID          int64              `json:"id"`
	Time        string             `json:"time"`
	Type        string             `json:"type"`
	Status      string             `json:"status,omitempty"`
	Summary     string             `json:"summary,omitempty"`
	Result      string             `json:"result,omitempty"`
	Next        string             `json:"next,omitempty"`
	RelatedTool string             `json:"related_tool,omitempty"`
	Files       []MemoryFileChange `json:"files,omitempty"`
	Truncated   bool               `json:"truncated,omitempty"`
}

type MemoryFileChange struct {
	Path      string `json:"path"`
	NewPath   string `json:"new_path,omitempty"`
	Operation string `json:"operation"`
}

type memoryRange struct {
	Start int64
	End   int64
}

type memoryEventRow struct {
	Sequence        int64
	EventType       string
	Tool            string
	ProgressSummary string
	Input           string
	Output          string
	Summary         string
	Truncated       bool
	CreatedAt       time.Time
}

func parseMemoryInteger(value string) (int64, error) {
	number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || number <= 0 {
		if err == nil {
			err = fmt.Errorf("must be positive")
		}
		return 0, err
	}
	return number, nil
}

func parseMemoryRanges(raw string, parse func(string) (int64, error), field string) ([]memoryRange, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxMemoryRangeTerms {
		return nil, fmt.Errorf("%w: %s has too many terms", ErrInvalidMemoryQuery, field)
	}
	ranges := make([]memoryRange, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.Count(part, "~") > 1 {
			return nil, fmt.Errorf("%w: invalid %s range %q", ErrInvalidMemoryQuery, field, part)
		}
		bounds := strings.Split(part, "~")
		start, err := parse(bounds[0])
		if err != nil {
			return nil, fmt.Errorf("%w: invalid %s value %q", ErrInvalidMemoryQuery, field, bounds[0])
		}
		end := start
		if len(bounds) == 2 {
			end, err = parse(bounds[1])
			if err != nil {
				return nil, fmt.Errorf("%w: invalid %s value %q", ErrInvalidMemoryQuery, field, bounds[1])
			}
		}
		if start > end {
			return nil, fmt.Errorf("%w: %s range must be ascending", ErrInvalidMemoryQuery, part)
		}
		ranges = append(ranges, memoryRange{Start: start, End: end})
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})
	merged := ranges[:0]
	for _, current := range ranges {
		if len(merged) == 0 || current.Start > merged[len(merged)-1].End+1 {
			merged = append(merged, current)
			continue
		}
		if current.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = current.End
		}
	}
	return merged, nil
}

func parseMemoryTimeRanges(raw string, location *time.Location) ([]memoryRange, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if location == nil {
		location = time.Local
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxMemoryRangeTerms {
		return nil, fmt.Errorf("%w: time has too many terms", ErrInvalidMemoryQuery)
	}
	ranges := make([]memoryRange, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.Count(part, "~") > 1 {
			return nil, fmt.Errorf("%w: invalid time range %q", ErrInvalidMemoryQuery, part)
		}
		bounds := strings.Split(part, "~")
		start, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(bounds[0]), location)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid time value %q", ErrInvalidMemoryQuery, bounds[0])
		}
		endDate := start
		if len(bounds) == 2 {
			endDate, err = time.ParseInLocation("2006-01-02", strings.TrimSpace(bounds[1]), location)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid time value %q", ErrInvalidMemoryQuery, bounds[1])
			}
		}
		if start.After(endDate) {
			return nil, fmt.Errorf("%w: time range must be ascending", ErrInvalidMemoryQuery)
		}
		ranges = append(ranges, memoryRange{
			Start: start.UTC().UnixMilli(),
			End:   endDate.AddDate(0, 0, 1).UTC().UnixMilli(),
		})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	merged := ranges[:0]
	for _, current := range ranges {
		if len(merged) == 0 || current.Start > merged[len(merged)-1].End {
			merged = append(merged, current)
			continue
		}
		if current.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = current.End
		}
	}
	return merged, nil
}

// QueryMemory returns the bounded, deterministic projection of project facts.
func (s *Store) QueryMemory(ctx context.Context, query MemoryQuery) (MemoryPage, error) {
	if s == nil || s.db == nil {
		return MemoryPage{}, fmt.Errorf("observation store database is required")
	}
	query.Workspace = strings.TrimSpace(query.Workspace)
	if query.Workspace == "" {
		return MemoryPage{}, fmt.Errorf("%w: workspace is required", ErrInvalidMemoryQuery)
	}
	if query.Latest < 0 || query.Latest > MaxMemoryLatest {
		return MemoryPage{}, fmt.Errorf("%w: latest must be between 1 and %d", ErrInvalidMemoryQuery, MaxMemoryLatest)
	}
	if query.Latest == 0 {
		query.Latest = DefaultMemoryLatest
	}
	idRanges, err := parseMemoryRanges(query.ID, parseMemoryInteger, "id")
	if err != nil {
		return MemoryPage{}, err
	}
	timeRanges, err := parseMemoryTimeRanges(query.Time, query.Location)
	if err != nil {
		return MemoryPage{}, err
	}

	where := []string{
		"workspace_name = ?",
		`(event_type IN ('file.changed', 'session.lifecycle') OR (event_type = 'tool.completed' AND tool_name = 'progress_report'))`,
	}
	args := []any{query.Workspace}
	if len(idRanges) > 0 {
		parts := make([]string, 0, len(idRanges))
		for _, item := range idRanges {
			parts = append(parts, "sequence BETWEEN ? AND ?")
			args = append(args, item.Start, item.End)
		}
		where = append(where, "("+strings.Join(parts, " OR ")+")")
	}
	if len(timeRanges) > 0 {
		parts := make([]string, 0, len(timeRanges))
		for _, item := range timeRanges {
			parts = append(parts, "(created_at >= ? AND created_at < ?)")
			args = append(args, item.Start, item.End)
		}
		where = append(where, "("+strings.Join(parts, " OR ")+")")
	}
	statement := `SELECT sequence, event_type, tool_name, progress_summary, input_json, output_json,
        summary, truncated, created_at
        FROM observation_events WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY sequence DESC`
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return MemoryPage{}, err
	}
	defer rows.Close()

	keyword := strings.ToLower(strings.TrimSpace(query.Keyword))
	page := MemoryPage{Items: make([]MemoryItem, 0, query.Latest)}
	for rows.Next() {
		var row memoryEventRow
		var truncated int
		var createdAt int64
		if err := rows.Scan(&row.Sequence, &row.EventType, &row.Tool, &row.ProgressSummary, &row.Input, &row.Output, &row.Summary, &truncated, &createdAt); err != nil {
			return MemoryPage{}, err
		}
		row.Truncated = truncated != 0
		row.CreatedAt = time.UnixMilli(createdAt).UTC()
		item, ok := projectMemoryEvent(row, query.Location)
		if !ok || (keyword != "" && !strings.Contains(strings.ToLower(memorySearchText(item)), keyword)) {
			continue
		}
		page.Total++
		if len(page.Items) < query.Latest {
			page.Items = append(page.Items, item)
		}
	}
	if err := rows.Err(); err != nil {
		return MemoryPage{}, err
	}
	page.HasMore = page.Total > len(page.Items)
	return page, nil
}

func projectMemoryEvent(row memoryEventRow, location *time.Location) (MemoryItem, bool) {
	if location == nil {
		location = time.Local
	}
	item := MemoryItem{ID: row.Sequence, Time: row.CreatedAt.In(location).Format(time.RFC3339), Truncated: row.Truncated}
	input := decodeObject(row.Input)
	output := decodeObject(row.Output)
	result := objectValue(output, "result")
	if result == nil {
		result = output
	}
	if nested := objectValue(result, "data"); nested != nil {
		result = nested
	}
	switch {
	case row.EventType == TypeToolCompleted && row.Tool == "progress_report":
		item.Type = "progress"
		item.Summary = firstMemoryString(input, result, "summary")
		item.Result = firstMemoryString(input, result, "result_summary")
		item.Status = firstMemoryString(input, result, "status")
		item.Next = firstMemoryString(input, result, "next_step")
		item.RelatedTool = firstMemoryString(input, result, "related_tool")
		if item.Summary == "" {
			item.Summary = strings.TrimSpace(row.ProgressSummary)
		}
		if item.Summary == "" {
			item.Summary = strings.TrimSpace(row.Summary)
		}
	case row.EventType == TypeFileChanged:
		item.Type = "file_changed"
		item.Summary = strings.TrimSpace(row.Summary)
		if item.Summary == "" {
			item.Summary = "文件变更"
		}
		item.Status = firstMemoryString(nil, result, "status")
		item.Files = memoryFileChanges(result)
	case row.EventType == TypeSessionLifecycle:
		item.Type = "session_lifecycle"
		item.Summary = strings.TrimSpace(row.Summary)
		if item.Summary == "" {
			item.Summary = "会话生命周期事件"
		}
	default:
		return MemoryItem{}, false
	}
	return item, true
}

func decodeObject(raw string) map[string]any {
	var value map[string]any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return nil
	}
	return value
}

func objectValue(object map[string]any, key string) map[string]any {
	if object == nil {
		return nil
	}
	value, _ := object[key].(map[string]any)
	return value
}

func firstMemoryString(first, second map[string]any, key string) string {
	for _, object := range []map[string]any{first, second} {
		if object == nil {
			continue
		}
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func memoryFileChanges(result map[string]any) []MemoryFileChange {
	if result == nil {
		return nil
	}
	values, _ := result["files"].([]any)
	files := make([]MemoryFileChange, 0, len(values))
	for _, value := range values {
		object, _ := value.(map[string]any)
		if object == nil {
			continue
		}
		path, _ := object["path"].(string)
		operation, _ := object["operation"].(string)
		if strings.TrimSpace(path) == "" || strings.TrimSpace(operation) == "" {
			continue
		}
		newPath, _ := object["new_path"].(string)
		files = append(files, MemoryFileChange{Path: path, NewPath: newPath, Operation: operation})
	}
	return files
}

func memorySearchText(item MemoryItem) string {
	parts := []string{item.Type, item.Status, item.Summary, item.Result, item.Next, item.RelatedTool}
	for _, file := range item.Files {
		parts = append(parts, file.Path, file.NewPath, file.Operation)
	}
	return strings.Join(parts, " ")
}
