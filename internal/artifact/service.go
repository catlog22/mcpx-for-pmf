package artifact

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"time"

	"mcpx/internal/file"
)

var (
	ErrNotFound = errors.New("artifact not found")
	ErrChanged  = errors.New("artifact content changed after registration")
)

type Service struct {
	db  *sql.DB
	now func() time.Time
}

type Artifact struct {
	ID              string    `json:"artifact_id"`
	RemoteSessionID string    `json:"remote_session_id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	Path            string    `json:"path"`
	MIMEType        string    `json:"mime_type"`
	Size            int64     `json:"size"`
	SHA256          string    `json:"sha256"`
	ResourceURI     string    `json:"resource_uri"`
	CreatedAt       time.Time `json:"created_at"`
}

type ReadResult struct {
	Artifact Artifact       `json:"artifact"`
	Offset   int64          `json:"offset"`
	Next     int64          `json:"next_offset"`
	EOF      bool           `json:"eof"`
	Encoding string         `json:"encoding"`
	Data     string         `json:"data"`
	Format   map[string]any `json:"format,omitempty"`
}

func NewService(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) Register(ctx context.Context, remoteSessionID, principalID, workspaceRoot, relativePath, name, kind, mimeType string) (Artifact, error) {
	absolute, err := file.Resolve(workspaceRoot, relativePath)
	if err != nil {
		return Artifact{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return Artifact{}, fmt.Errorf("artifact path must be a regular file")
	}
	relativePath = filepath.ToSlash(filepath.Clean(relativePath))
	if name == "" {
		name = filepath.Base(relativePath)
	}
	if kind == "" {
		kind = "other"
	}
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(relativePath))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
	}
	digest, err := fileDigest(absolute)
	if err != nil {
		return Artifact{}, err
	}
	id := randomID()
	now := s.now().UTC()
	artifact := Artifact{ID: id, RemoteSessionID: remoteSessionID, Name: name, Kind: kind, Path: relativePath,
		MIMEType: mimeType, Size: info.Size(), SHA256: digest, ResourceURI: ResourceURI(remoteSessionID, id), CreatedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO artifacts
        (id, remote_session_id, name, kind, path, mime_type, size, sha256, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, artifact.ID, artifact.RemoteSessionID, artifact.Name,
		artifact.Kind, artifact.Path, artifact.MIMEType, artifact.Size, artifact.SHA256, principalID, now.UnixMilli())
	if err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func (s *Service) Get(ctx context.Context, remoteSessionID, artifactID string) (Artifact, error) {
	var artifact Artifact
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, remote_session_id, name, kind, path, mime_type, size, sha256, created_at
        FROM artifacts WHERE id = ? AND remote_session_id = ?`, artifactID, remoteSessionID).Scan(
		&artifact.ID, &artifact.RemoteSessionID, &artifact.Name, &artifact.Kind, &artifact.Path,
		&artifact.MIMEType, &artifact.Size, &artifact.SHA256, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	if err != nil {
		return Artifact{}, err
	}
	artifact.CreatedAt = time.UnixMilli(createdAt).UTC()
	artifact.ResourceURI = ResourceURI(artifact.RemoteSessionID, artifact.ID)
	return artifact, nil
}

func (s *Service) List(ctx context.Context, remoteSessionID, kind string, limit int) ([]Artifact, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := `SELECT id, name, kind, path, mime_type, size, sha256, created_at
        FROM artifacts WHERE remote_session_id = ?`
	args := []any{remoteSessionID}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Artifact, 0, limit)
	for rows.Next() {
		var artifact Artifact
		var createdAt int64
		if err := rows.Scan(&artifact.ID, &artifact.Name, &artifact.Kind, &artifact.Path,
			&artifact.MIMEType, &artifact.Size, &artifact.SHA256, &createdAt); err != nil {
			return nil, err
		}
		artifact.RemoteSessionID = remoteSessionID
		artifact.CreatedAt = time.UnixMilli(createdAt).UTC()
		artifact.ResourceURI = ResourceURI(remoteSessionID, artifact.ID)
		result = append(result, artifact)
	}
	return result, rows.Err()
}

func (s *Service) Read(ctx context.Context, remoteSessionID, artifactID, workspaceRoot string, offset int64, limit int) (ReadResult, error) {
	artifact, err := s.Get(ctx, remoteSessionID, artifactID)
	if err != nil {
		return ReadResult{}, err
	}
	absolute, err := file.Resolve(workspaceRoot, artifact.Path)
	if err != nil {
		return ReadResult{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 1<<20 {
		limit = 256 << 10
	}
	handle, err := os.Open(absolute)
	if err != nil {
		return ReadResult{}, err
	}
	defer handle.Close()
	buffer := make([]byte, limit)
	read, readErr := handle.ReadAt(buffer, offset)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return ReadResult{}, readErr
	}
	buffer = buffer[:read]
	window := AlignReadWindow(buffer)
	pres := PresentText(artifact.Path, window, artifact.MIMEType)
	formatMap := map[string]any{
		"charset": pres.Format.Charset, "bom": pres.Format.BOM, "line_ending": pres.Format.LineEnding,
	}
	encoding, data := "base64", base64.StdEncoding.EncodeToString(buffer)
	if pres.OK {
		encoding, data = "utf-8", string(pres.UTF8)
	}
	next := offset + int64(read)
	return ReadResult{
		Artifact: artifact, Offset: offset, Next: next, EOF: next >= artifact.Size,
		Encoding: encoding, Data: data, Format: formatMap,
	}, nil
}

func (s *Service) ReadAll(ctx context.Context, remoteSessionID, artifactID, workspaceRoot string, maxBytes int64) (Artifact, []byte, error) {
	artifact, err := s.Get(ctx, remoteSessionID, artifactID)
	if err != nil {
		return Artifact{}, nil, err
	}
	if artifact.Size > maxBytes {
		return Artifact{}, nil, fmt.Errorf("artifact exceeds resource limit; use artifact_read")
	}
	absolute, err := file.Resolve(workspaceRoot, artifact.Path)
	if err != nil {
		return Artifact{}, nil, err
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return Artifact{}, nil, err
	}
	digest := sha256.Sum256(content)
	if "sha256:"+hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return Artifact{}, nil, ErrChanged
	}
	return artifact, content, nil
}

func ResourceURI(remoteSessionID, artifactID string) string {
	return fmt.Sprintf("mcpx://remote-sessions/%s/artifacts/%s", remoteSessionID, artifactID)
}

func fileDigest(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, handle); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func randomID() string {
	var value [12]byte
	_, _ = rand.Read(value[:])
	return "art_" + hex.EncodeToString(value[:])
}
