package engine

import (
	"encoding/json"
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

func TestNativeRealityBundlePreservesSourceVersionPolicy(t *testing.T) {
	for _, policy := range []string{`{}`, `{"minClientVer":""}`, `{"minClientVer":"26.3.27","maxClientVer":"30.0.0","maxTimeDiff":1000}`} {
		raw := json.RawMessage(`{"tag":"` + agentv1.NativeRealityStatsTagPrefix + `rule--metadata","protocol":"vless","streamSettings":{"security":"reality","realitySettings":` + policy + `}}`)
		got, err := compatibleRealityInbound(raw)
		if err != nil || string(got) != string(raw) {
			t.Fatal("native source version policy changed", err)
		}
	}
}
