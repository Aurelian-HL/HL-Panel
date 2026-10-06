package ny

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPreflightRejectsDuplicateJSONFieldsWithoutLeakingSource(t *testing.T) {
	inspector := newTestInspector(t)
	for _, raw := range []string{
		`{"password=secret":1,"password=secret":2}`,
		`{"groups":[{"token":"secret","token":"other"}]}`,
		`{"groups":1,"grou\u0070s":2}`,
	} {
		t.Run(raw, func(t *testing.T) {
			snapshot, err := inspector.Capture([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			report := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{})
			assertReportIssue(t, report, StatusInvalid, "DUPLICATE_JSON_FIELD")
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte("secret")) || bytes.Contains(encoded, []byte("password")) {
				t.Fatalf("report disclosed source content: %s", encoded)
			}
		})
	}
}

func TestUniqueJSONFieldsAllowsSameNameInDifferentObjects(t *testing.T) {
	if err := validateUniqueJSONFields([]byte(`[{"id":1},{"id":2}]`)); err != nil {
		t.Fatalf("independent object fields rejected: %v", err)
	}
}

func TestPreflightRejectsExcessiveJSONNesting(t *testing.T) {
	inspector := newTestInspector(t)
	raw := []byte(strings.Repeat("[", maxJSONNesting+1) + "0" + strings.Repeat("]", maxJSONNesting+1))
	snapshot, err := inspector.Capture(raw)
	if err != nil {
		t.Fatal(err)
	}
	assertReportIssue(t, inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{}), StatusInvalid, "JSON_NESTING_LIMIT")
}

func TestDuplicateJSONFieldNeverReachesRegisteredAdapter(t *testing.T) {
	declaration := SchemaDeclaration{Family: "fixture-ny", Version: "one"}
	adapter := &countingAdapter{schema: declaration}
	inspector, err := NewInspector(Options{}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := inspector.Capture([]byte(`{"rules":[{"id":1,"id":2}]}`))
	if err != nil {
		t.Fatal(err)
	}
	assertReportIssue(t, inspector.Preflight(context.Background(), snapshot, declaration), StatusInvalid, "DUPLICATE_JSON_FIELD")
	if adapter.calls != 0 {
		t.Fatalf("adapter called %d times for ambiguous JSON", adapter.calls)
	}
}

type countingAdapter struct {
	schema SchemaDeclaration
	calls  int
}

func (a *countingAdapter) ID() string { return "counting-fixture-adapter" }

func (a *countingAdapter) Schema() SchemaDeclaration { return a.schema }

func (a *countingAdapter) Evidence() []Evidence {
	return []Evidence{{Reference: "fixture://schema", SchemaSHA256: digestText("schema")}}
}

func (a *countingAdapter) Preview(context.Context, json.RawMessage) (AdapterPreview, error) {
	a.calls++
	return AdapterPreview{}, nil
}
