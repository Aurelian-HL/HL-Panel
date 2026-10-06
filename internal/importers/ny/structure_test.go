package ny

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestJSONStructureInventoryRedactsKeysAndValues(t *testing.T) {
	inspector := newTestInspector(t)
	raw := []byte(`{"token-secret":[{"address":"203.0.113.7","password":"private-value"},{"address":null,"password":"other-value"}]}`)
	snapshot, err := inspector.Capture(raw)
	if err != nil {
		t.Fatal(err)
	}
	report := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{})
	if report.JSONStructure == nil || report.JSONStructure.Truncated || len(report.JSONStructure.Entries) != 6 {
		t.Fatalf("JSON structure = %#v", report.JSONStructure)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"token-secret", "address", "password", "203.0.113.7", "private-value", "other-value"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("report disclosed %q: %s", secret, encoded)
		}
	}
	for _, entry := range report.JSONStructure.Entries {
		if !validSHA256(entry.PathSHA256) || entry.Occurrences < 1 {
			t.Fatalf("invalid structure entry: %#v", entry)
		}
	}
}

func TestJSONStructureInventoryCountsArrayShapesAndMixedTypes(t *testing.T) {
	structure := inspectJSONStructure(map[string]any{
		"items": []any{
			map[string]any{"state": "ready"},
			map[string]any{"state": nil},
			map[string]any{"state": "active"},
		},
	}, 20)
	if structure.Truncated || len(structure.Entries) != 5 {
		t.Fatalf("structure = %#v", structure)
	}
	childPath := "$\x00k5:items\x00a"
	statePath := childPath + "\x00k5:state"
	for _, expected := range []JSONStructureEntry{
		{PathSHA256: digestString("json-structure-path/v1\x00" + childPath), Kind: "object", Occurrences: 3},
		{PathSHA256: digestString("json-structure-path/v1\x00" + statePath), Kind: "string", Occurrences: 2},
		{PathSHA256: digestString("json-structure-path/v1\x00" + statePath), Kind: "null", Occurrences: 1},
	} {
		if !containsStructureEntry(structure.Entries, expected) {
			t.Fatalf("missing %#v in %#v", expected, structure.Entries)
		}
	}
}

func TestJSONStructureInventoryIsDeterministicAndBounded(t *testing.T) {
	first := map[string]any{"b": true, "a": json.Number("1"), "c": []any{false}}
	second := map[string]any{"c": []any{false}, "a": json.Number("1"), "b": true}
	left := inspectJSONStructure(first, 3)
	right := inspectJSONStructure(second, 3)
	if !reflect.DeepEqual(left, right) || !left.Truncated || len(left.Entries) != 3 {
		t.Fatalf("bounded structure differs: %#v / %#v", left, right)
	}
	if !sort.SliceIsSorted(left.Entries, func(i, j int) bool {
		return left.Entries[i].PathSHA256 < left.Entries[j].PathSHA256
	}) {
		t.Fatalf("entries not sorted: %#v", left.Entries)
	}
	if !containsStructureEntry(left.Entries, JSONStructureEntry{
		PathSHA256: digestString("json-structure-path/v1\x00$\x00k1:a"),
		Kind:       "number", Occurrences: 1,
	}) {
		t.Fatalf("sorted first object child missing: %#v", left.Entries)
	}
}

func TestJSONStructureTruncationIsExplicitAndInvalidJSONHasNoInventory(t *testing.T) {
	inspector := newTestInspector(t)
	parts := make([]string, defaultMaxJSONStructureEntries)
	for index := range parts {
		parts[index] = `"` + strings.Repeat("k", index+1) + `":null`
	}
	snapshot, err := inspector.Capture([]byte(`{` + strings.Join(parts, ",") + `}`))
	if err != nil {
		t.Fatal(err)
	}
	report := inspector.Preflight(context.Background(), snapshot, SchemaDeclaration{})
	if report.JSONStructure == nil || !report.JSONStructure.Truncated || len(report.JSONStructure.Entries) != defaultMaxJSONStructureEntries {
		t.Fatalf("truncated structure = %#v", report.JSONStructure)
	}
	assertHasIssue(t, report, "JSON_STRUCTURE_TRUNCATED")

	invalid, err := inspector.Capture([]byte(`{"x":1,"x":2}`))
	if err != nil {
		t.Fatal(err)
	}
	invalidReport := inspector.Preflight(context.Background(), invalid, SchemaDeclaration{})
	if invalidReport.JSONStructure != nil {
		t.Fatalf("invalid JSON has partial structure: %#v", invalidReport.JSONStructure)
	}
}

func containsStructureEntry(entries []JSONStructureEntry, expected JSONStructureEntry) bool {
	for _, entry := range entries {
		if entry == expected {
			return true
		}
	}
	return false
}
