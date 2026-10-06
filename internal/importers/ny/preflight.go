package ny

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

type Options struct {
	MaxSnapshotBytes     int64
	MaxArchiveFiles      int
	MaxArchiveBytes      uint64
	MaxArchiveEntryBytes uint64
}

type Inspector struct {
	options  Options
	adapters map[string]Adapter
}

func NewInspector(options Options, adapters ...Adapter) (*Inspector, error) {
	options = normalizeOptions(options)
	inspector := &Inspector{options: options, adapters: make(map[string]Adapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			return nil, errors.New("NY schema adapter is nil")
		}
		declaration := adapter.Schema().normalized()
		if !declaration.valid() {
			return nil, fmt.Errorf("NY schema adapter %q requires an exact family and version", adapter.ID())
		}
		if strings.TrimSpace(adapter.ID()) == "" {
			return nil, errors.New("NY schema adapter ID is required")
		}
		evidence := adapter.Evidence()
		if len(evidence) == 0 {
			return nil, fmt.Errorf("NY schema adapter %q has no schema evidence metadata", adapter.ID())
		}
		for index, item := range evidence {
			if err := item.validate(); err != nil {
				return nil, fmt.Errorf("NY schema adapter %q evidence %d: %w", adapter.ID(), index, err)
			}
		}
		key := schemaKey(declaration)
		if _, exists := inspector.adapters[key]; exists {
			return nil, fmt.Errorf("duplicate NY schema adapter for %s %s", declaration.Family, declaration.Version)
		}
		inspector.adapters[key] = adapter
	}
	return inspector, nil
}

func normalizeOptions(options Options) Options {
	if options.MaxSnapshotBytes <= 0 {
		options.MaxSnapshotBytes = DefaultMaxSnapshotBytes
	}
	if options.MaxArchiveFiles <= 0 {
		options.MaxArchiveFiles = DefaultMaxArchiveFiles
	}
	if options.MaxArchiveBytes == 0 {
		options.MaxArchiveBytes = DefaultMaxArchiveBytes
	}
	if options.MaxArchiveEntryBytes == 0 {
		options.MaxArchiveEntryBytes = DefaultMaxArchiveEntry
	}
	return options
}

func schemaKey(declaration SchemaDeclaration) string {
	declaration = declaration.normalized()
	return declaration.Family + "\x00" + declaration.Version
}

// ReadSnapshot opens a local regular file without following a final symlink.
// It never contacts a panel, mutates the source, or writes a database.
func (i *Inspector) ReadSnapshot(ctx context.Context, inputPath string) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	inputPath, err := validateLocalSnapshotPath(strings.TrimSpace(inputPath))
	if err != nil {
		return Snapshot{}, err
	}
	info, err := os.Lstat(inputPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect NY snapshot: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Snapshot{}, errors.New("NY snapshot must be a regular file, not a symlink")
	}
	if info.Size() > i.options.MaxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("%w: got %d bytes, limit %d", ErrSnapshotTooLarge, info.Size(), i.options.MaxSnapshotBytes)
	}
	file, err := os.Open(inputPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open NY snapshot: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect opened NY snapshot: %w", err)
	}
	if !opened.Mode().IsRegular() || opened.Size() > i.options.MaxSnapshotBytes {
		return Snapshot{}, errors.New("opened NY snapshot is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, i.options.MaxSnapshotBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read NY snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return Capture(raw, i.options.MaxSnapshotBytes)
}

func (i *Inspector) Capture(raw []byte) (Snapshot, error) {
	return Capture(raw, i.options.MaxSnapshotBytes)
}

func (i *Inspector) Preflight(ctx context.Context, snapshot Snapshot, declaration SchemaDeclaration) Report {
	if ctx == nil {
		ctx = context.Background()
	}
	declaration = declaration.normalized()
	report := emptyReport(snapshot.Metadata(), declaration)
	if err := ctx.Err(); err != nil {
		report.Status = StatusInvalid
		report.Groups.State = EntityInvalid
		report.Rules.State = EntityInvalid
		report.Issues = append(report.Issues, issue("CONTEXT_CANCELED", SeverityError, "", "preflight context was canceled"))
		return report
	}
	if snapshot.Metadata().Size == 0 {
		report.Status = StatusInvalid
		report.Groups.State = EntityInvalid
		report.Rules.State = EntityInvalid
		report.Issues = append(report.Issues, issue("EMPTY_SNAPSHOT", SeverityError, "", "snapshot is empty"))
		return report
	}

	switch snapshot.Metadata().Format {
	case FormatJSON:
		i.inspectJSON(ctx, snapshot, declaration, &report)
	case FormatZIP:
		i.inspectZIP(snapshot, declaration, &report)
	default:
		report.Status = StatusUnsupported
		report.Groups.State = EntityUnsupported
		report.Rules.State = EntityUnsupported
		report.Issues = append(report.Issues, issue("UNSUPPORTED_SNAPSHOT_FORMAT", SeverityError, "", "only JSON snapshots and ZIP archive manifests are supported"))
	}
	return report
}

func emptyReport(metadata SnapshotMetadata, declaration SchemaDeclaration) Report {
	return Report{
		ReportVersion:             ReportVersion,
		Status:                    StatusSchemaRequired,
		Snapshot:                  metadata,
		Schema:                    SchemaStatus{Declared: declaration},
		TopLevelFieldFingerprints: []string{},
		Groups:                    EntitySummary{State: EntitySchemaRequired, Fingerprints: []string{}},
		Rules:                     EntitySummary{State: EntitySchemaRequired, Fingerprints: []string{}},
		Mappings:                  []MappingProposal{},
		ReferenceConflicts:        []ReferenceConflict{},
		UnsupportedFields:         []UnsupportedField{},
		Issues:                    []Issue{},
	}
}

func (i *Inspector) inspectJSON(ctx context.Context, snapshot Snapshot, declaration SchemaDeclaration, report *Report) {
	var document any
	decoder := json.NewDecoder(bytes.NewReader(snapshot.raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		markInvalid(report, "INVALID_JSON", "JSON snapshot could not be parsed")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		markInvalid(report, "MULTIPLE_JSON_VALUES", "JSON snapshot contains more than one top-level value")
		return
	}
	if err := validateUniqueJSONFields(snapshot.raw); err != nil {
		code, message := "INVALID_JSON", "JSON snapshot could not be parsed"
		if errors.Is(err, errDuplicateJSONField) {
			code, message = "DUPLICATE_JSON_FIELD", "JSON snapshot contains a repeated object field"
		} else if errors.Is(err, errJSONNestingLimit) {
			code, message = "JSON_NESTING_LIMIT", "JSON snapshot exceeds the supported nesting depth"
		}
		markInvalid(report, code, message)
		return
	}

	switch value := document.(type) {
	case map[string]any:
		report.TopLevelKind = "object"
		report.TopLevelFieldFingerprints = make([]string, 0, len(value))
		for key := range value {
			report.TopLevelFieldFingerprints = append(report.TopLevelFieldFingerprints, fingerprintJSONKey(key))
		}
		sort.Strings(report.TopLevelFieldFingerprints)
	case []any:
		report.TopLevelKind = "array"
	default:
		report.TopLevelKind = "scalar"
	}
	structure := inspectJSONStructure(document, defaultMaxJSONStructureEntries)
	report.JSONStructure = &structure
	if structure.Truncated {
		report.Issues = append(report.Issues, issue("JSON_STRUCTURE_TRUNCATED", SeverityWarning, "", "JSON structure inventory reached its entry limit"))
	}

	if declaration.empty() {
		markSchemaRequired(report, "NY_SCHEMA_REQUIRED", "an exact NY export family and version backed by schema evidence is required")
		addUnknownFields(report)
		return
	}
	if !declaration.valid() {
		markSchemaRequired(report, "NY_SCHEMA_DECLARATION_INCOMPLETE", "schema family and version must be supplied together")
		addUnknownFields(report)
		return
	}
	adapter, exists := i.adapters[schemaKey(declaration)]
	if !exists {
		report.Status = StatusUnsupported
		report.Groups.State = EntityUnsupported
		report.Rules.State = EntityUnsupported
		report.Issues = append(report.Issues, issue("NY_SCHEMA_UNSUPPORTED", SeverityWarning, "", "no reviewed exact-version adapter is registered for the declared NY schema"))
		addUnknownFields(report)
		return
	}

	preview, err := adapter.Preview(ctx, json.RawMessage(snapshot.RawCopy()))
	if err != nil {
		markInvalid(report, "ADAPTER_PREVIEW_FAILED", "the registered exact-version adapter rejected the snapshot")
		return
	}
	if err := validateAdapterPreview(preview); err != nil {
		markInvalid(report, "ADAPTER_OUTPUT_INVALID", "the registered exact-version adapter returned an invalid redacted preview")
		return
	}
	report.Status = StatusPreviewReady
	report.Schema.Supported = true
	report.Schema.AdapterID = adapter.ID()
	report.Schema.EvidenceCount = len(adapter.Evidence())
	report.Groups = cloneEntitySummary(preview.Groups)
	report.Rules = cloneEntitySummary(preview.Rules)
	report.Mappings = cloneMappings(preview.Mappings)
	report.ReferenceConflicts = append([]ReferenceConflict(nil), preview.ReferenceConflicts...)
	for _, field := range preview.UnsupportedFields {
		if field.Path != "" {
			field.PathSHA256 = digestString("schema-path\x00" + field.Path)
			field.Path = ""
		}
		report.UnsupportedFields = append(report.UnsupportedFields, field)
	}
	for _, item := range preview.Issues {
		report.Issues = append(report.Issues, issue(item.Code, item.Severity, "", adapterIssueMessage(item.Code)))
	}
	canonicalizeAdapterReport(report)
}

func (i *Inspector) inspectZIP(snapshot Snapshot, declaration SchemaDeclaration, report *Report) {
	archive, issues, invalid := preflightZIP(snapshot.raw, i.options)
	report.Archive = &archive
	report.Issues = append(report.Issues, issues...)
	if invalid {
		report.Status = StatusInvalid
		report.Groups.State = EntityInvalid
		report.Rules.State = EntityInvalid
		return
	}
	if declaration.empty() || !declaration.valid() {
		markSchemaRequired(report, "NY_ARCHIVE_SCHEMA_REQUIRED", "archive manifest is valid, but an exact NY archive schema and member selection are required")
		return
	}
	report.Status = StatusUnsupported
	report.Groups.State = EntityUnsupported
	report.Rules.State = EntityUnsupported
	report.Issues = append(report.Issues, issue("NY_ARCHIVE_ADAPTER_UNSUPPORTED", SeverityWarning, "", "no reviewed exact-version NY archive adapter is registered"))
}

func preflightZIP(raw []byte, options Options) (ArchiveReport, []Issue, bool) {
	report := ArchiveReport{Entries: []ArchiveEntry{}}
	issues := []Issue{}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return report, append(issues, issue("INVALID_ZIP", SeverityError, "", "ZIP archive central directory could not be parsed")), true
	}
	if len(reader.File) > options.MaxArchiveFiles {
		return report, append(issues, issue("ARCHIVE_FILE_LIMIT", SeverityError, "", "ZIP archive contains too many entries")), true
	}
	report.FileCount = len(reader.File)
	for _, file := range reader.File {
		nameDigest := sha256.Sum256([]byte(file.Name))
		entry := ArchiveEntry{
			NameSHA256:      hex.EncodeToString(nameDigest[:]),
			CompressedBytes: file.CompressedSize64,
			ExpandedBytes:   file.UncompressedSize64,
			Directory:       file.FileInfo().IsDir(),
			PotentialJSON:   strings.EqualFold(path.Ext(strings.ReplaceAll(file.Name, "\\", "/")), ".json"),
		}
		report.Entries = append(report.Entries, entry)
		if entry.PotentialJSON && !entry.Directory {
			report.PotentialJSONFiles++
		}
		if unsafeArchivePath(file.Name) {
			issues = append(issues, issue("ARCHIVE_UNSAFE_PATH", SeverityError, "archive.entries", "ZIP entry path is absolute or escapes the archive root"))
		}
		if file.Mode()&os.ModeSymlink != 0 {
			issues = append(issues, issue("ARCHIVE_SYMLINK", SeverityError, "archive.entries", "ZIP symlink entries are not accepted"))
		}
		if file.Flags&0x1 != 0 {
			issues = append(issues, issue("ARCHIVE_ENCRYPTED_ENTRY", SeverityError, "archive.entries", "encrypted ZIP entries cannot be inspected"))
		}
		if file.UncompressedSize64 > options.MaxArchiveEntryBytes {
			issues = append(issues, issue("ARCHIVE_ENTRY_SIZE_LIMIT", SeverityError, "archive.entries", "ZIP entry exceeds the expanded-size limit"))
		}
		if ^uint64(0)-report.ExpandedBytes < file.UncompressedSize64 {
			issues = append(issues, issue("ARCHIVE_SIZE_OVERFLOW", SeverityError, "archive", "ZIP expanded-size total overflowed"))
			return report, issues, true
		}
		report.ExpandedBytes += file.UncompressedSize64
	}
	if report.ExpandedBytes > options.MaxArchiveBytes {
		issues = append(issues, issue("ARCHIVE_EXPANDED_SIZE_LIMIT", SeverityError, "archive", "ZIP expanded-size total exceeds the configured limit"))
	}
	invalid := false
	for _, item := range issues {
		if item.Severity == SeverityError {
			invalid = true
			break
		}
	}
	return report, issues, invalid
}

func unsafeArchivePath(name string) bool {
	normalized := strings.ReplaceAll(name, "\\", "/")
	cleaned := path.Clean(normalized)
	windowsDrive := len(normalized) >= 2 && normalized[1] == ':' &&
		((normalized[0] >= 'a' && normalized[0] <= 'z') || (normalized[0] >= 'A' && normalized[0] <= 'Z'))
	return path.IsAbs(normalized) || windowsDrive || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.ContainsRune(normalized, '\x00')
}

func addUnknownFields(report *Report) {
	if report.TopLevelKind != "object" {
		report.UnsupportedFields = append(report.UnsupportedFields, UnsupportedField{PathSHA256: digestString("$"), Code: "SCHEMA_NOT_INTERPRETED"})
		return
	}
	for _, fingerprint := range report.TopLevelFieldFingerprints {
		report.UnsupportedFields = append(report.UnsupportedFields, UnsupportedField{PathSHA256: fingerprint, Code: "SCHEMA_NOT_INTERPRETED"})
	}
}

func fingerprintJSONKey(value string) string {
	return digestString("json-key\x00" + value)
}

func digestString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func markSchemaRequired(report *Report, code, message string) {
	report.Status = StatusSchemaRequired
	report.Groups.State = EntitySchemaRequired
	report.Rules.State = EntitySchemaRequired
	report.Issues = append(report.Issues, issue(code, SeverityWarning, "", message))
}

func markInvalid(report *Report, code, message string) {
	report.Status = StatusInvalid
	report.Groups.State = EntityInvalid
	report.Rules.State = EntityInvalid
	report.Issues = append(report.Issues, issue(code, SeverityError, "", message))
}

func issue(code string, severity Severity, path, message string) Issue {
	return Issue{Code: code, Severity: severity, Path: path, Message: message}
}

func cloneEntitySummary(summary EntitySummary) EntitySummary {
	result := summary
	result.Fingerprints = append([]string(nil), summary.Fingerprints...)
	if result.Fingerprints == nil {
		result.Fingerprints = []string{}
	}
	sort.Strings(result.Fingerprints)
	return result
}

func validateAdapterPreview(preview AdapterPreview) error {
	for name, summary := range map[string]EntitySummary{"groups": preview.Groups, "rules": preview.Rules} {
		if summary.State != EntityPreviewed || summary.Count < 0 || summary.Count != len(summary.Fingerprints) {
			return fmt.Errorf("%s summary is inconsistent", name)
		}
		for _, fingerprint := range summary.Fingerprints {
			if !validSHA256(fingerprint) {
				return fmt.Errorf("%s fingerprint is not SHA-256", name)
			}
		}
		if len(fingerprintSet(summary.Fingerprints)) != summary.Count {
			return fmt.Errorf("%s fingerprints are repeated", name)
		}
	}
	if err := validateMappings(preview); err != nil {
		return err
	}
	return validateAdapterDiagnostics(preview)
}

func validSHA256(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
