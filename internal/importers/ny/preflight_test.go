package ny

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestCaptureIsDeterministicAndOwnsRawSnapshot(t *testing.T) {
	raw := []byte(`{"groups":[],"credential":"must-not-leak"}`)
	snapshot, err := Capture(raw, 0)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	wantHash := sha256.Sum256(raw)
	if snapshot.Metadata().SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("snapshot hash = %q, want %q", snapshot.Metadata().SHA256, hex.EncodeToString(wantHash[:]))
	}

	raw[0] = '['
	copyOne := snapshot.RawCopy()
	if copyOne[0] != '{' {
		t.Fatal("snapshot changed when caller mutated the input slice")
	}
	copyOne[0] = '['
	if snapshot.RawCopy()[0] != '{' {
		t.Fatal("snapshot changed when caller mutated RawCopy output")
	}

	again, err := Capture(snapshot.RawCopy(), 0)
	if err != nil {
		t.Fatalf("Capture(second) error = %v", err)
	}
	if again.Metadata().SHA256 != snapshot.Metadata().SHA256 {
		t.Fatalf("same bytes produced different hashes: %q != %q", again.Metadata().SHA256, snapshot.Metadata().SHA256)
	}
}

func TestCaptureRejectsOversizeInput(t *testing.T) {
	_, err := Capture([]byte("12345"), 4)
	if !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("Capture() error = %v, want ErrSnapshotTooLarge", err)
	}
}

func TestPreflightReportsEmptyAndInvalidJSON(t *testing.T) {
	inspector := newTestInspector(t)

	empty, err := inspector.Capture(nil)
	if err != nil {
		t.Fatalf("Capture(empty) error = %v", err)
	}
	emptyReport := inspector.Preflight(context.Background(), empty, SchemaDeclaration{})
	assertReportIssue(t, emptyReport, StatusInvalid, "EMPTY_SNAPSHOT")

	invalid, err := inspector.Capture([]byte(`{"groups":`))
	if err != nil {
		t.Fatalf("Capture(invalid) error = %v", err)
	}
	invalidReport := inspector.Preflight(context.Background(), invalid, SchemaDeclaration{})
	assertReportIssue(t, invalidReport, StatusInvalid, "INVALID_JSON")

	multiple, err := inspector.Capture([]byte(`{} {}`))
	if err != nil {
		t.Fatalf("Capture(multiple) error = %v", err)
	}
	multipleReport := inspector.Preflight(context.Background(), multiple, SchemaDeclaration{})
	assertReportIssue(t, multipleReport, StatusInvalid, "MULTIPLE_JSON_VALUES")
}

func TestPreflightWithoutSchemaDoesNotGuessFieldsOrLeakValues(t *testing.T) {
	inspector := newTestInspector(t)
	raw := []byte(`{"rules":[{"target":"203.0.113.9:443","password":"secret-value"}],"groups":[{"name":"edge"}],"version":"unknown","token=top-secret@198.51.100.7":true}`)
	snapshot, err := inspector.Capture(raw)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	report := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{})
	assertReportIssue(t, report, StatusSchemaRequired, "NY_SCHEMA_REQUIRED")
	if report.ReportVersion != 3 || report.Mappings == nil || len(report.Mappings) != 0 {
		t.Fatalf("schema-less report version/mappings = %d / %#v", report.ReportVersion, report.Mappings)
	}
	if report.Groups.Count != 0 || report.Rules.Count != 0 {
		t.Fatalf("schema-less preview interpreted entities: groups=%d rules=%d", report.Groups.Count, report.Rules.Count)
	}
	wantFields := []string{
		fingerprintJSONKey("groups"), fingerprintJSONKey("rules"),
		fingerprintJSONKey("token=top-secret@198.51.100.7"), fingerprintJSONKey("version"),
	}
	sort.Strings(wantFields)
	if strings.Join(report.TopLevelFieldFingerprints, ",") != strings.Join(wantFields, ",") {
		t.Fatalf("top-level field fingerprints = %#v, want %#v", report.TopLevelFieldFingerprints, wantFields)
	}
	if len(report.UnsupportedFields) != len(wantFields) {
		t.Fatalf("unsupported fields = %#v, want %d entries", report.UnsupportedFields, len(wantFields))
	}
	for _, field := range report.UnsupportedFields {
		if field.Path != "" || !validSHA256(field.PathSHA256) {
			t.Fatalf("untrusted field was not reduced to a SHA-256 fingerprint: %#v", field)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
	for _, secret := range []string{"203.0.113.9", "secret-value", "edge", "token=top-secret", "198.51.100.7"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("redacted report leaked source value %q: %s", secret, encoded)
		}
	}
}

func TestPreflightRejectsUnknownAndIncompleteSchemaDeclarations(t *testing.T) {
	inspector := newTestInspector(t)
	snapshot, err := inspector.Capture([]byte(`{"schema_version":"v9","groups":[]}`))
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}

	unknown := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{Family: "ny-json", Version: "v9"})
	assertReportIssue(t, unknown, StatusUnsupported, "NY_SCHEMA_UNSUPPORTED")
	if unknown.Schema.Supported {
		t.Fatal("unknown schema was marked supported")
	}

	incomplete := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{Family: "ny-json"})
	assertReportIssue(t, incomplete, StatusSchemaRequired, "NY_SCHEMA_DECLARATION_INCOMPLETE")
}

func TestInspectorRequiresEvidenceMetadataBeforeRegisteringAdapter(t *testing.T) {
	adapter := fakeAdapter{schema: SchemaDeclaration{Family: "ny-json", Version: "v1"}}
	if _, err := NewInspector(Options{}, adapter); err == nil || !strings.Contains(err.Error(), "no schema evidence metadata") {
		t.Fatalf("NewInspector() error = %v, want missing evidence metadata error", err)
	}
}

func TestRegisteredAdapterReturnsOnlyRedactedPreview(t *testing.T) {
	groupFingerprint := digestText("group-42")
	ruleFingerprint := digestText("rule-99")
	adapter := fakeAdapter{
		schema: SchemaDeclaration{Family: "fixture-ny", Version: "2026.10"},
		evidence: []Evidence{{
			Reference:    "fixture://verified-schema.json",
			SchemaSHA256: digestText("verified schema bytes"),
		}},
		preview: AdapterPreview{
			Groups: EntitySummary{State: EntityPreviewed, Count: 1, Fingerprints: []string{groupFingerprint}},
			Rules:  EntitySummary{State: EntityPreviewed, Count: 1, Fingerprints: []string{ruleFingerprint}},
			Mappings: []MappingProposal{
				{SourceFingerprint: groupFingerprint, TargetKind: TargetDeviceGroup, GroupKind: GroupKindEntry, Disposition: DispositionProposed},
				{SourceFingerprint: ruleFingerprint, TargetKind: TargetForwardingRule, Disposition: DispositionBlocked, ReasonCodes: []string{ReasonMissingGroup}},
			},
			ReferenceConflicts: []ReferenceConflict{{
				Code: "MISSING_GROUP_REFERENCE", FromType: "rule", FromFingerprint: ruleFingerprint,
				Field: "group_id", ToType: "group",
			}},
			UnsupportedFields: []UnsupportedField{{Path: "$.rules[*].private_extension", Code: "FIELD_UNSUPPORTED"}},
			Issues:            []Issue{{Code: ReasonUnknownField, Severity: SeverityWarning, Message: "password=hidden"}},
		},
	}
	inspector, err := NewInspector(Options{}, adapter)
	if err != nil {
		t.Fatalf("NewInspector() error = %v", err)
	}
	snapshot, err := inspector.Capture([]byte(`{"groups":[{"id":42}],"rules":[{"id":99,"password":"hidden"}]}`))
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	report := inspector.Preflight(context.Background(), snapshot, adapter.schema)
	if report.Status != StatusPreviewReady || !report.Schema.Supported || report.Groups.Count != 1 || report.Rules.Count != 1 {
		t.Fatalf("preview report = %#v", report)
	}
	if len(report.ReferenceConflicts) != 1 || len(report.UnsupportedFields) != 1 || len(report.Mappings) != 2 {
		t.Fatalf("preview conflicts/unsupported = %#v / %#v", report.ReferenceConflicts, report.UnsupportedFields)
	}
	if report.UnsupportedFields[0].Path != "" || !validSHA256(report.UnsupportedFields[0].PathSHA256) {
		t.Fatalf("adapter path was not redacted: %#v", report.UnsupportedFields[0])
	}
	encoded, _ := json.Marshal(report)
	if bytes.Contains(encoded, []byte("hidden")) {
		t.Fatalf("adapter report leaked source credential: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte("source contains a field not mapped by the adapter")) {
		t.Fatalf("adapter warning was not rendered from a controlled message: %s", encoded)
	}
}

func TestAdapterCannotProposeRuleWithMissingEntryGroup(t *testing.T) {
	groupFingerprint := digestText("entry-group")
	ruleFingerprint := digestText("rule")
	preview := AdapterPreview{
		Groups: EntitySummary{State: EntityPreviewed, Count: 1, Fingerprints: []string{groupFingerprint}},
		Rules:  EntitySummary{State: EntityPreviewed, Count: 1, Fingerprints: []string{ruleFingerprint}},
		Mappings: []MappingProposal{
			{SourceFingerprint: groupFingerprint, TargetKind: TargetDeviceGroup, GroupKind: GroupKindEntry, Disposition: DispositionProposed},
			{SourceFingerprint: ruleFingerprint, TargetKind: TargetForwardingRule, Disposition: DispositionProposed},
		},
	}
	if err := validateAdapterPreview(preview); err == nil {
		t.Fatal("rule without a mapped entry-group dependency was accepted")
	}
	preview.Mappings[1].EntryGroup = digestText("unknown-group")
	preview.Mappings[1].EgressMode = EgressDirect
	if err := validateAdapterPreview(preview); err == nil {
		t.Fatal("rule referencing an absent group was accepted")
	}
	preview.Mappings[1].EntryGroup = groupFingerprint
	if err := validateAdapterPreview(preview); err != nil {
		t.Fatalf("valid mapping was rejected: %v", err)
	}
	preview.Mappings[1].EgressMode = EgressExitGroup
	preview.Mappings[1].ExitGroup = groupFingerprint
	if err := validateAdapterPreview(preview); err == nil {
		t.Fatal("entry group used as an exit group was accepted")
	}
	exitFingerprint := digestText("exit-group")
	preview.Groups = EntitySummary{State: EntityPreviewed, Count: 2, Fingerprints: []string{groupFingerprint, exitFingerprint}}
	preview.Mappings = append(preview.Mappings, MappingProposal{
		SourceFingerprint: exitFingerprint, TargetKind: TargetDeviceGroup,
		GroupKind: GroupKindExit, Disposition: DispositionProposed,
	})
	preview.Mappings[1].ExitGroup = exitFingerprint
	if err := validateAdapterPreview(preview); err != nil {
		t.Fatalf("valid entry-to-exit mapping was rejected: %v", err)
	}
	preview.Mappings[1].Disposition = DispositionBlocked
	preview.Mappings[1].EntryGroup = ""
	preview.Mappings[1].ExitGroup = ""
	preview.Mappings[1].EgressMode = ""
	preview.Mappings[1].ReasonCodes = []string{ReasonUnsupported}
	preview.ReferenceConflicts = []ReferenceConflict{{
		Code: ReasonMissingGroup, FromType: "rule", FromFingerprint: ruleFingerprint,
		Field: "entry_group_id", ToType: "group",
	}}
	if err := validateAdapterPreview(preview); err == nil {
		t.Fatal("missing-group conflict with unrelated blocking reason was accepted")
	}
}

func TestAdapterDiagnosticsCannotLeakSourceValues(t *testing.T) {
	groupFingerprint := digestText("group")
	base := AdapterPreview{
		Groups:   EntitySummary{State: EntityPreviewed, Count: 1, Fingerprints: []string{groupFingerprint}},
		Rules:    EntitySummary{State: EntityPreviewed, Fingerprints: []string{}},
		Mappings: []MappingProposal{{SourceFingerprint: groupFingerprint, TargetKind: TargetDeviceGroup, GroupKind: GroupKindEntry, Disposition: DispositionProposed}},
	}
	malicious := base
	malicious.ReferenceConflicts = []ReferenceConflict{{Code: "password=secret", FromType: "rule"}}
	if err := validateAdapterPreview(malicious); err == nil {
		t.Fatal("untrusted conflict diagnostic was accepted")
	}
	malicious = base
	malicious.UnsupportedFields = []UnsupportedField{{Path: "$.password=secret", Code: "password=secret"}}
	if err := validateAdapterPreview(malicious); err == nil {
		t.Fatal("untrusted field code was accepted")
	}
	malicious = base
	malicious.Issues = []Issue{{Code: "password=secret", Severity: SeverityWarning, Message: "password=secret"}}
	if err := validateAdapterPreview(malicious); err == nil {
		t.Fatal("untrusted issue code was accepted")
	}
}

func TestAdapterReportOrderIsDeterministic(t *testing.T) {
	firstGroup, secondGroup := digestText("first"), digestText("second")
	base := fakeAdapter{
		schema:   SchemaDeclaration{Family: "fixture-ny", Version: "ordered"},
		evidence: []Evidence{{Reference: "fixture://schema", SchemaSHA256: digestText("schema")}},
		preview: AdapterPreview{
			Groups: EntitySummary{State: EntityPreviewed, Count: 2, Fingerprints: []string{secondGroup, firstGroup}},
			Rules:  EntitySummary{State: EntityPreviewed, Fingerprints: []string{}},
			Mappings: []MappingProposal{
				{SourceFingerprint: secondGroup, TargetKind: TargetDeviceGroup, GroupKind: GroupKindExit, Disposition: DispositionProposed},
				{SourceFingerprint: firstGroup, TargetKind: TargetDeviceGroup, GroupKind: GroupKindEntry, Disposition: DispositionProposed},
			},
			UnsupportedFields: []UnsupportedField{
				{Path: "$.z", Code: "FIELD_UNSUPPORTED"},
				{Path: "$.a", Code: "FIELD_UNSUPPORTED"},
			},
			Issues: []Issue{
				{Code: ReasonUnsupported, Severity: SeverityWarning},
				{Code: ReasonUnknownField, Severity: SeverityWarning},
			},
		},
	}
	inspector, err := NewInspector(Options{}, base)
	if err != nil {
		t.Fatalf("NewInspector() error = %v", err)
	}
	snapshot, err := inspector.Capture([]byte(`{}`))
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	first := inspector.Preflight(context.Background(), snapshot, base.schema)
	base.preview.Groups.Fingerprints[0], base.preview.Groups.Fingerprints[1] = base.preview.Groups.Fingerprints[1], base.preview.Groups.Fingerprints[0]
	base.preview.Mappings[0], base.preview.Mappings[1] = base.preview.Mappings[1], base.preview.Mappings[0]
	base.preview.UnsupportedFields[0], base.preview.UnsupportedFields[1] = base.preview.UnsupportedFields[1], base.preview.UnsupportedFields[0]
	base.preview.Issues[0], base.preview.Issues[1] = base.preview.Issues[1], base.preview.Issues[0]
	secondInspector, err := NewInspector(Options{}, base)
	if err != nil {
		t.Fatalf("NewInspector(reordered) error = %v", err)
	}
	second := secondInspector.Preflight(context.Background(), snapshot, base.schema)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("equivalent previews emitted different reports: %s != %s", firstJSON, secondJSON)
	}
}

func TestZIPPreflightReportsManifestWithoutNamesOrContent(t *testing.T) {
	archive := zipBytes(t, map[string]string{
		"private/customer-export.json": `{"password":"zip-secret"}`,
		"manifest.txt":                 "metadata",
	})
	inspector := newTestInspector(t)
	snapshot, err := inspector.Capture(archive)
	if err != nil {
		t.Fatalf("Capture(zip) error = %v", err)
	}
	report := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{})
	assertReportIssue(t, report, StatusSchemaRequired, "NY_ARCHIVE_SCHEMA_REQUIRED")
	if report.Archive == nil || report.Archive.FileCount != 2 || report.Archive.PotentialJSONFiles != 1 {
		t.Fatalf("archive report = %#v", report.Archive)
	}
	encoded, _ := json.Marshal(report)
	for _, secret := range []string{"customer-export.json", "zip-secret", "manifest.txt"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("archive report leaked %q: %s", secret, encoded)
		}
	}
}

func TestZIPPreflightRejectsTraversalAndExpandedSizeLimit(t *testing.T) {
	archive := zipBytes(t, map[string]string{"../escape.json": strings.Repeat("x", 32)})
	inspector, err := NewInspector(Options{MaxArchiveEntryBytes: 16})
	if err != nil {
		t.Fatalf("NewInspector() error = %v", err)
	}
	snapshot, err := inspector.Capture(archive)
	if err != nil {
		t.Fatalf("Capture(zip) error = %v", err)
	}
	report := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{})
	if report.Status != StatusInvalid {
		t.Fatalf("zip report status = %q, want %q", report.Status, StatusInvalid)
	}
	assertHasIssue(t, report, "ARCHIVE_UNSAFE_PATH")
	assertHasIssue(t, report, "ARCHIVE_ENTRY_SIZE_LIMIT")
}

func TestZIPPreflightRejectsWindowsAbsolutePath(t *testing.T) {
	archive := zipBytes(t, map[string]string{`C:\\private\\export.json`: `{}`})
	inspector := newTestInspector(t)
	snapshot, err := inspector.Capture(archive)
	if err != nil {
		t.Fatalf("Capture(zip) error = %v", err)
	}
	report := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{})
	if report.Status != StatusInvalid {
		t.Fatalf("zip report status = %q, want %q", report.Status, StatusInvalid)
	}
	assertHasIssue(t, report, "ARCHIVE_UNSAFE_PATH")
}

type fakeAdapter struct {
	schema   SchemaDeclaration
	evidence []Evidence
	preview  AdapterPreview
}

func (a fakeAdapter) ID() string { return "test-fixture-adapter" }

func (a fakeAdapter) Schema() SchemaDeclaration { return a.schema }

func (a fakeAdapter) Evidence() []Evidence { return append([]Evidence(nil), a.evidence...) }

func (a fakeAdapter) Preview(context.Context, json.RawMessage) (AdapterPreview, error) {
	return a.preview, nil
}

func newTestInspector(t *testing.T) *Inspector {
	t.Helper()
	inspector, err := NewInspector(Options{})
	if err != nil {
		t.Fatalf("NewInspector() error = %v", err)
	}
	return inspector
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%q) error = %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("zip.Write(%q) error = %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip.Close() error = %v", err)
	}
	return buffer.Bytes()
}

func assertReportIssue(t *testing.T, report Report, status Status, code string) {
	t.Helper()
	if report.Status != status {
		t.Fatalf("report status = %q, want %q; report=%#v", report.Status, status, report)
	}
	assertHasIssue(t, report, code)
}

func assertHasIssue(t *testing.T, report Report, code string) {
	t.Helper()
	for _, item := range report.Issues {
		if item.Code == code {
			return
		}
	}
	t.Fatalf("report issues = %#v, want code %q", report.Issues, code)
}
