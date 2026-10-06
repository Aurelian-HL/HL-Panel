// Package membership loads versioned, complete gateway candidate snapshots.
// Invalid or stale snapshots are rejected so a runtime can retain its last
// known good membership.
package membership

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hongle/hl-panel/internal/routing/endpointrouter"
)

const MaxSnapshotBytes int64 = 2 << 20

type Snapshot struct {
	Revision  uint64                    `json:"revision"`
	Endpoints []endpointrouter.Endpoint `json:"endpoints"`
	SHA256    string                    `json:"-"`
}

type FileSource struct {
	path string
}

func NewFileSource(path string) (*FileSource, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("membership file path must be absolute")
	}
	return &FileSource{path: filepath.Clean(path)}, nil
}

func (s *FileSource) Load(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	info, err := os.Lstat(s.path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect membership snapshot: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Snapshot{}, errors.New("membership snapshot must be a regular file")
	}
	if info.Size() > MaxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("membership snapshot exceeds %d bytes", MaxSnapshotBytes)
	}
	file, err := os.Open(s.path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open membership snapshot: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect opened membership snapshot: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() > MaxSnapshotBytes {
		return Snapshot{}, errors.New("opened membership snapshot is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxSnapshotBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read membership snapshot: %w", err)
	}
	if int64(len(raw)) > MaxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("membership snapshot exceeds %d bytes", MaxSnapshotBytes)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return DecodeSnapshot(raw)
}

// DecodeSnapshot applies the same strict contract to file and HTTP sources.
func DecodeSnapshot(raw []byte) (Snapshot, error) {
	if int64(len(raw)) > MaxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("membership snapshot exceeds %d bytes", MaxSnapshotBytes)
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode membership snapshot: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("contains multiple JSON values")
		}
		return Snapshot{}, fmt.Errorf("decode membership snapshot: %w", err)
	}
	if snapshot.Revision == 0 {
		return Snapshot{}, errors.New("membership revision must be greater than zero")
	}
	if snapshot.Endpoints == nil {
		return Snapshot{}, errors.New("membership endpoints must be an array")
	}
	canonical, err := json.Marshal(struct {
		Revision  uint64                    `json:"revision"`
		Endpoints []endpointrouter.Endpoint `json:"endpoints"`
	}{Revision: snapshot.Revision, Endpoints: snapshot.Endpoints})
	if err != nil {
		return Snapshot{}, fmt.Errorf("canonicalize membership snapshot: %w", err)
	}
	digest := sha256.Sum256(canonical)
	snapshot.SHA256 = hex.EncodeToString(digest[:])
	return snapshot, nil
}
