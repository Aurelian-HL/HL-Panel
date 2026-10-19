package engine

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestLegacyRealityBundleCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, field, expected string }{
		{"omitted", "", "0.0.0"},
		{"empty", `,"minClientVer":""`, "0.0.0"},
		{"explicit_restriction", `,"minClientVer":"26.3.27"`, "26.3.27"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"inbounds":[{"tag":"legacy","protocol":"vless","streamSettings":{"security":"reality","realitySettings":{"privateKey":"fixture-key","shortIds":["01234567"]` + tc.field + `}}}],"outbounds":[]}`)
			original := string(raw)
			bundle := agentv1.ConfigurationBundle{SchemaVersion: BundleSchemaVersion, Fragments: []agentv1.ConfigurationFragment{{Engine: agentv1.EngineXray, Config: raw}}}
			compiled, err := CompileXrayBundle(bundle)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Inbounds []struct {
					Stream struct {
						Reality struct {
							Minimum string   `json:"minClientVer"`
							Key     string   `json:"privateKey"`
							IDs     []string `json:"shortIds"`
						} `json:"realitySettings"`
					} `json:"streamSettings"`
				} `json:"inbounds"`
			}
			if err := json.Unmarshal(compiled, &doc); err != nil {
				t.Fatal(err)
			}
			reality := doc.Inbounds[0].Stream.Reality
			if reality.Minimum != tc.expected || reality.Key != "fixture-key" || len(reality.IDs) != 1 || reality.IDs[0] != "01234567" || string(raw) != original {
				t.Fatal("compatibility changed credentials, explicit restriction, or source bundle")
			}
		})
	}
}

func TestNativeRealityBundleCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, policy, minimum string }{
		{"omitted", `{}`, "0.0.0"},
		{"empty", `{"minClientVer":""}`, "0.0.0"},
		{"explicit_restriction", `{"minClientVer":"26.3.27","maxClientVer":"30.0.0","maxTimeDiff":1000}`, "26.3.27"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"tag":"` + agentv1.NativeRealityStatsTagPrefix + `rule--metadata","protocol":"vless","settings":{"clients":[{"id":"fixture-identity","flow":""}]},"streamSettings":{"security":"reality","realitySettings":` + tc.policy + `}}`)
			original := string(raw)
			got, err := compatibleRealityInbound(raw)
			if err != nil {
				t.Fatal(err)
			}
			var before, after map[string]any
			if err := json.Unmarshal(raw, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(got, &after); err != nil {
				t.Fatal(err)
			}
			reality := after["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
			if reality["minClientVer"] != tc.minimum || string(raw) != original {
				t.Fatal("native listener minimum compatibility or immutable source changed")
			}
			// Every other source field, including explicit maximum/time policies
			// and the original VLESS identity/flow, must survive the projection.
			realityBefore := before["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
			realityBefore["minClientVer"] = tc.minimum
			if !reflect.DeepEqual(before, after) {
				t.Fatal("native compatibility changed unrelated source fields")
			}
			if tc.name == "explicit_restriction" && string(got) != original {
				t.Fatal("native explicit version restriction changed")
			}
		})
	}
}
