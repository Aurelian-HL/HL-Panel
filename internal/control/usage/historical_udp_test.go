package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func historicalUDPFixture(t *testing.T, engine agentv1.Engine) historicalUsageRepository {
	t.Helper()
	repo := historicalUsageFixture(t)
	var bundle agentv1.ConfigurationBundle
	if err := json.Unmarshal(repo.historical.Config.Config, &bundle); err != nil {
		t.Fatal(err)
	}
	f := &bundle.Fragments[0]
	f.Engine = engine
	f.GroupID = generations.ForwardingFragmentID(engine, "rule-1")
	f.Usage.Protocol = "udp"
	if engine == agentv1.EngineGOST {
		rule := forwarding.Rule{ID: "rule-1", Name: "historical UDP", CustomerID: "customer-1", EntryGroupID: "entry-1",
			IngressProtocol: forwarding.IngressUDP, Protocol: forwarding.ProtocolUDP, EgressMode: forwarding.EgressDirect,
			ListenPort: 23001, Targets: []forwarding.Target{{Host: "127.0.0.1", Port: 23002}}, SelectionPolicy: forwarding.SelectionRoundRobin}
		var err error
		f.Config, err = gostconfig.CompileDirectUDP([]forwarding.Rule{rule}, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
	} else {
		encoded, err := json.Marshal(f.Usage)
		if err != nil {
			t.Fatal(err)
		}
		f.Config, err = json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": "vless-reality-kl-native-rule-1--" + base64.RawURLEncoding.EncodeToString(encoded)}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	sealHistoricalUDP(t, &repo.historical, bundle)
	return repo
}

func sealHistoricalUDP(t *testing.T, h *deploymentreceipts.HistoricalInput, bundle agentv1.ConfigurationBundle) {
	t.Helper()
	var err error
	h.Config.Config, err = json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	h.Config.ConfigSHA256 = generations.SHA256Hex(h.Config.Config)
	for phase, r := range h.ApplyResults {
		r.ConfigSHA256, r.EngineMode = h.Config.ConfigSHA256, string(bundle.Fragments[0].Engine)
		h.ApplyResults[phase] = r
	}
}

func TestHistoricalUDPUsageAcceptsVerifiedNativeCounters(t *testing.T) {
	for _, engine := range []agentv1.Engine{agentv1.EngineGOST, agentv1.EngineXray} {
		t.Run(string(engine), func(t *testing.T) {
			repo := historicalUDPFixture(t, engine)
			p := NewLegacyRuleMetadataProvider(repo).(GenerationRuleMetadataProvider)
			got, err := p.ResolveRuleUsageMetadataAt(context.Background(), "node-1", "rule-1", agentv1.NodeConfigGeneration(repo.historical.Config.Generation), time.Now().UTC())
			if err != nil {
				t.Fatal("verified historical UDP counter rejected", err)
			}
			if got.Protocol != "udp" || got.CustomerID != "customer-1" || got.EntryMultiplierMicros != 2_000_000 {
				t.Fatal("historical metadata changed")
			}
		})
	}
}

func TestHistoricalUDPUsageRetainsEvidenceAndNativeValidation(t *testing.T) {
	for _, engine := range []agentv1.Engine{agentv1.EngineGOST, agentv1.EngineXray} {
		for _, invalid := range []string{"node", "generation", "hash", "future", "schema", "invalidated", "attempt", "commit", "verify", "dry_run", "engine", "duplicate", "missing_usage", "protocol", "customer", "multiplier", "counter", "native_protocol"} {
			t.Run(string(engine)+"/"+invalid, func(t *testing.T) {
				repo := historicalUDPFixture(t, engine)
				h := &repo.historical
				generation := agentv1.NodeConfigGeneration(h.Config.Generation)
				var bundle agentv1.ConfigurationBundle
				if err := json.Unmarshal(h.Config.Config, &bundle); err != nil {
					t.Fatal(err)
				}
				f := &bundle.Fragments[0]
				switch invalid {
				case "schema":
					bundle.SchemaVersion++
				case "duplicate":
					bundle.Fragments = append(bundle.Fragments, *f)
				case "missing_usage":
					f.Usage = nil
				case "protocol":
					f.Usage.Protocol = "quic"
				case "customer":
					f.Usage.CustomerID = ""
				case "multiplier":
					f.Usage.EntryMultiplierMicros = -1
				case "counter":
					f.Config = json.RawMessage(`{}`)
				case "native_protocol":
					if engine == agentv1.EngineGOST {
						f.Config = json.RawMessage(`{"services":[{"name":"forward-rule-1","handler":{"type":"tcp"},"listener":{"type":"tcp"},"metadata":{"enableStats":true}}]}`)
					} else {
						m := *f.Usage
						m.Protocol = "tcp"
						encoded, _ := json.Marshal(m)
						f.Config, _ = json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": "vless-reality-kl-native-rule-1--" + base64.RawURLEncoding.EncodeToString(encoded)}}})
					}
				}
				sealHistoricalUDP(t, h, bundle)
				switch invalid {
				case "node":
					h.Config.NodeID = "other-node"
				case "generation":
					h.Config.Generation++
				case "hash":
					h.Config.ConfigSHA256 = "bad-hash"
				case "future":
					h.Config.CreatedAt = time.Now().Add(time.Hour)
				case "invalidated":
					h.Invalidated = true
				case "attempt":
					h.AttemptID = "other-attempt"
				case "commit":
					delete(h.ApplyResults, agentv1.ApplyPhaseCommit)
				case "verify":
					delete(h.ApplyResults, agentv1.ApplyPhaseVerify)
				case "dry_run", "engine":
					r := h.ApplyResults[agentv1.ApplyPhaseVerify]
					r.EngineMode = "dry_run"
					if invalid == "engine" {
						r.EngineMode = "unsupported-engine"
					}
					h.ApplyResults[r.Phase] = r
				}
				p := NewLegacyRuleMetadataProvider(repo).(GenerationRuleMetadataProvider)
				if _, err := p.ResolveRuleUsageMetadataAt(context.Background(), "node-1", "rule-1", generation, time.Now().UTC()); !errors.Is(err, faults.ErrValidation) {
					t.Fatal("invalid UDP history accepted", err)
				}
			})
		}
	}
}

func TestHistoricalUDPDoesNotAuthorizeUnversionedLegacyReports(t *testing.T) {
	repo := historicalUDPFixture(t, agentv1.EngineGOST)
	p := NewLegacyRuleMetadataProvider(repo).(GenerationRuleMetadataProvider)
	if _, err := p.ResolveRuleUsageMetadataAt(context.Background(), "node-1", "rule-1", 0, time.Now().UTC()); err == nil {
		t.Fatal("UDP history bypassed current legacy eligibility")
	}
}

func TestGOSTHistoricalCounterRequiresUnambiguousProtocolAndStats(t *testing.T) {
	for _, raw := range []string{
		`{"services":[{"name":"forward-rule-1","handler":{"type":"udp"},"listener":{"type":"tcp"},"metadata":{"enableStats":true}}]}`,
		`{"services":[{"name":"forward-rule-1","handler":{"type":"tcp"},"listener":{"type":"udp"},"metadata":{"enableStats":true}}]}`,
		`{"services":[{"name":"forward-rule-1","handler":{"type":"udp"},"listener":{"type":"udp"},"metadata":{"enableStats":false}}]}`,
		`{"services":[{"name":"forward-rule-1","handler":{"type":"udp"},"listener":{"type":"udp"},"metadata":{"enableStats":"true"}}]}`,
		`{"services":[{"name":"forward-rule-1","handler":{"type":"udp"},"listener":{"type":"udp"},"metadata":{"enableStats":true}},{"name":"forward-rule-1"}]}`,
	} {
		if hasGOSTService(json.RawMessage(raw), "forward-rule-1", "udp") {
			t.Fatal("ambiguous UDP counter accepted")
		}
	}
}
