// Package ny provides read-only, offline preflight inspection for NY export
// snapshots. It deliberately has no persistence or production-panel client.
package ny

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	ReportVersion           = 3
	DefaultMaxSnapshotBytes = int64(16 << 20)
	DefaultMaxArchiveFiles  = 512
	DefaultMaxArchiveBytes  = uint64(128 << 20)
	DefaultMaxArchiveEntry  = uint64(32 << 20)
)

var ErrSnapshotTooLarge = errors.New("NY snapshot exceeds the configured size limit")

type Format string

const (
	FormatJSON    Format = "json"
	FormatZIP     Format = "zip"
	FormatUnknown Format = "unknown"
)

type Status string

const (
	StatusPreviewReady   Status = "preview_ready"
	StatusSchemaRequired Status = "schema_required"
	StatusUnsupported    Status = "unsupported"
	StatusInvalid        Status = "invalid_snapshot"
)

type EntityState string

const (
	EntityPreviewed      EntityState = "previewed"
	EntitySchemaRequired EntityState = "schema_required"
	EntityUnsupported    EntityState = "unsupported"
	EntityInvalid        EntityState = "invalid_snapshot"
)

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Snapshot owns an immutable copy of the supplied bytes. RawCopy returns a
// separate copy so adapters cannot mutate the evidence captured by the hash.
type Snapshot struct {
	raw  []byte
	meta SnapshotMetadata
}

type SnapshotMetadata struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size_bytes"`
	Format Format `json:"format"`
}

func Capture(raw []byte, maximum int64) (Snapshot, error) {
	if maximum <= 0 {
		maximum = DefaultMaxSnapshotBytes
	}
	if int64(len(raw)) > maximum {
		return Snapshot{}, fmt.Errorf("%w: got %d bytes, limit %d", ErrSnapshotTooLarge, len(raw), maximum)
	}
	owned := bytes.Clone(raw)
	digest := sha256.Sum256(owned)
	return Snapshot{
		raw: owned,
		meta: SnapshotMetadata{
			SHA256: hex.EncodeToString(digest[:]),
			Size:   int64(len(owned)),
			Format: detectFormat(owned),
		},
	}, nil
}

func (s Snapshot) Metadata() SnapshotMetadata { return s.meta }

func (s Snapshot) RawCopy() []byte { return bytes.Clone(s.raw) }

type SchemaDeclaration struct {
	Family  string `json:"family,omitempty"`
	Version string `json:"version,omitempty"`
}

func (d SchemaDeclaration) normalized() SchemaDeclaration {
	return SchemaDeclaration{
		Family:  strings.ToLower(strings.TrimSpace(d.Family)),
		Version: strings.TrimSpace(d.Version),
	}
}

func (d SchemaDeclaration) empty() bool {
	d = d.normalized()
	return d.Family == "" && d.Version == ""
}

func (d SchemaDeclaration) valid() bool {
	d = d.normalized()
	return d.Family != "" && d.Version != ""
}

type Evidence struct {
	Reference    string `json:"reference"`
	SchemaSHA256 string `json:"schema_sha256"`
	Description  string `json:"description,omitempty"`
}

func (e Evidence) validate() error {
	if strings.TrimSpace(e.Reference) == "" {
		return errors.New("schema evidence reference is required")
	}
	digest := strings.ToLower(strings.TrimSpace(e.SchemaSHA256))
	if len(digest) != sha256.Size*2 {
		return errors.New("schema evidence SHA-256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("schema evidence SHA-256 must contain 64 hexadecimal characters")
	}
	return nil
}

type SchemaStatus struct {
	Declared      SchemaDeclaration `json:"declared"`
	Supported     bool              `json:"supported"`
	EvidenceCount int               `json:"evidence_count"`
	AdapterID     string            `json:"adapter_id,omitempty"`
}

type EntitySummary struct {
	State        EntityState `json:"state"`
	Count        int         `json:"count"`
	Fingerprints []string    `json:"fingerprints"`
}

type ReferenceConflict struct {
	Code            string `json:"code"`
	FromType        string `json:"from_type"`
	FromFingerprint string `json:"from_fingerprint,omitempty"`
	Field           string `json:"field"`
	ToType          string `json:"to_type"`
	ToFingerprint   string `json:"to_fingerprint,omitempty"`
}

type UnsupportedField struct {
	Path       string `json:"path,omitempty"`
	PathSHA256 string `json:"path_sha256,omitempty"`
	Code       string `json:"code"`
}

type Issue struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Path     string   `json:"path,omitempty"`
	Message  string   `json:"message"`
}

type ArchiveEntry struct {
	NameSHA256      string `json:"name_sha256"`
	CompressedBytes uint64 `json:"compressed_bytes"`
	ExpandedBytes   uint64 `json:"expanded_bytes"`
	Directory       bool   `json:"directory"`
	PotentialJSON   bool   `json:"potential_json"`
}

type ArchiveReport struct {
	FileCount          int            `json:"file_count"`
	ExpandedBytes      uint64         `json:"expanded_bytes"`
	PotentialJSONFiles int            `json:"potential_json_files"`
	Entries            []ArchiveEntry `json:"entries"`
}

type JSONStructureEntry struct {
	PathSHA256  string `json:"path_sha256"`
	Kind        string `json:"kind"`
	Occurrences int    `json:"occurrences"`
}

type JSONStructureReport struct {
	Entries   []JSONStructureEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
}

type Report struct {
	ReportVersion             int                  `json:"report_version"`
	Status                    Status               `json:"status"`
	Snapshot                  SnapshotMetadata     `json:"snapshot"`
	Schema                    SchemaStatus         `json:"schema"`
	TopLevelKind              string               `json:"top_level_kind,omitempty"`
	TopLevelFieldFingerprints []string             `json:"top_level_field_fingerprints"`
	JSONStructure             *JSONStructureReport `json:"json_structure,omitempty"`
	Groups                    EntitySummary        `json:"groups"`
	Rules                     EntitySummary        `json:"rules"`
	Mappings                  []MappingProposal    `json:"mappings"`
	ReferenceConflicts        []ReferenceConflict  `json:"reference_conflicts"`
	UnsupportedFields         []UnsupportedField   `json:"unsupported_fields"`
	Issues                    []Issue              `json:"issues"`
	Archive                   *ArchiveReport       `json:"archive,omitempty"`
}

// Adapter is registered only for an exact, externally declared schema. The
// registry validates evidence metadata, not the authenticity of an external
// artifact; that verification belongs to repository review and CI. Match by
// field shape is intentionally absent.
type Adapter interface {
	ID() string
	Schema() SchemaDeclaration
	Evidence() []Evidence
	Preview(context.Context, json.RawMessage) (AdapterPreview, error)
}

type AdapterPreview struct {
	Groups             EntitySummary
	Rules              EntitySummary
	Mappings           []MappingProposal
	ReferenceConflicts []ReferenceConflict
	UnsupportedFields  []UnsupportedField
	Issues             []Issue
}

func detectFormat(raw []byte) Format {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return FormatUnknown
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return FormatJSON
	}
	if len(trimmed) >= 4 && trimmed[0] == 'P' && trimmed[1] == 'K' {
		switch string(trimmed[2:4]) {
		case "\x03\x04", "\x05\x06", "\x07\x08":
			return FormatZIP
		}
	}
	return FormatUnknown
}
