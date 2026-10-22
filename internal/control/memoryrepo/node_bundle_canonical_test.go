package memoryrepo

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// The optimized encoder mirrors only these small protocol envelopes. An added
// field must fail here until it is encoded and covered by the byte oracle below.
func TestCanonicalNodeBundleEnvelopeContract(t *testing.T) {
	type field struct {
		name   string
		typeOf reflect.Type
		tag    reflect.StructTag
	}
	for _, contract := range []struct {
		typeOf reflect.Type
		fields []field
	}{
		{reflect.TypeFor[agentv1.ConfigurationBundle](), []field{
			{"SchemaVersion", reflect.TypeFor[int](), `json:"schema_version"`},
			{"Fragments", reflect.TypeFor[[]agentv1.ConfigurationFragment](), `json:"fragments"`},
		}},
		{reflect.TypeFor[agentv1.ConfigurationFragment](), []field{
			{"Usage", reflect.TypeFor[*agentv1.UsageMetadata](), `json:"usage,omitempty"`},
			{"GroupID", reflect.TypeFor[string](), `json:"group_id"`},
			{"GroupRevision", reflect.TypeFor[agentv1.GroupRevision](), `json:"group_generation"`},
			{"Engine", reflect.TypeFor[agentv1.Engine](), `json:"engine"`},
			{"Config", reflect.TypeFor[json.RawMessage](), `json:"config"`},
		}},
	} {
		if contract.typeOf.NumField() != len(contract.fields) {
			t.Fatalf("%s changed: update canonicalNodeBundle and its wire oracle", contract.typeOf)
		}
		for i, want := range contract.fields {
			got := contract.typeOf.Field(i)
			if got.Name != want.name || got.Type != want.typeOf || got.Tag != want.tag || got.Anonymous {
				t.Fatalf("%s.%s changed: update canonicalNodeBundle and its wire oracle", contract.typeOf, got.Name)
			}
		}
	}
}

func legacyCanonicalNodeBundle(bundle agentv1.ConfigurationBundle) (json.RawMessage, error) {
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	return generations.CanonicalJSON(raw)
}

func TestCanonicalNodeBundlePreservesWireBytesAndHash(t *testing.T) {
	for _, raw := range []json.RawMessage{
		nil, {}, []byte("null"), []byte(`{"z":[3,2,1],"a":{"b":1e+09,"a":9007199254740993}}`),
		[]byte(`{"escaped":"<tag>&\u2028\n中文","a":1,"a":2}`), []byte("true"), []byte("[1,2]"),
		[]byte("{} {}"), []byte("invalid"),
	} {
		for _, usage := range []*agentv1.UsageMetadata{nil, {}, {
			RuleID: "r<&中文", CustomerID: "c", EntryGroupID: "entry", ExitGroupID: "exit", Protocol: "udp",
			ListenPort: 443, SpeedLimitMbps: 123, EntryMultiplierMicros: 9007199254740993, ExitMultiplierMicros: 1000000,
		}} {
			bundle := agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{
				GroupID: "group\n<&中文", GroupRevision: 9007199254740993, Engine: agentv1.EngineXray, Config: raw, Usage: usage,
			}}}
			want, wantErr := legacyCanonicalNodeBundle(bundle)
			got, gotErr := canonicalNodeBundle(bundle)
			if (gotErr == nil) != (wantErr == nil) || !bytes.Equal(got, want) || generations.SHA256Hex(got) != generations.SHA256Hex(want) {
				t.Fatal("canonical wire contract changed")
			}
		}
	}
	for _, fragments := range [][]agentv1.ConfigurationFragment{nil, {}} {
		bundle := agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: fragments}
		want, err := legacyCanonicalNodeBundle(bundle)
		if err != nil {
			t.Fatal(err)
		}
		got, err := canonicalNodeBundle(bundle)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("nil/empty bundle encoding changed", err)
		}
	}
}

func FuzzCanonicalNodeBundle(f *testing.F) {
	f.Add([]byte(`{"b":{"a":2},"a":[1,true,null]}`))
	f.Add([]byte(`{"x":"\u2028<>&"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		bundle := agentv1.ConfigurationBundle{SchemaVersion: 1, Fragments: []agentv1.ConfigurationFragment{{
			GroupID: string(raw), GroupRevision: 7, Engine: agentv1.EngineGOST, Config: raw,
		}}}
		want, wantErr := legacyCanonicalNodeBundle(bundle)
		got, gotErr := canonicalNodeBundle(bundle)
		if (gotErr == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
			t.Fatal("canonical wire contract changed")
		}
	})
}
