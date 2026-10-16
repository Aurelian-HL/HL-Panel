package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func TestKLNativeUsageTagRequiresExactRuleAndOwnership(t *testing.T) {
	rule := forwarding.Rule{ID: "klrule_one", CustomerID: "customer-1", EntryGroupID: "entry-1", Protocol: forwarding.ProtocolTCP}
	valid := map[string]string{"rule_id": rule.ID, "customer_id": rule.CustomerID, "entry_group_id": rule.EntryGroupID, "exit_group_id": "", "protocol": "tcp"}
	for _, tt := range []struct {
		name, prefix, field, value string
		want                       bool
	}{
		{name: "imported tag", prefix: "vless-reality-kl-native-" + rule.ID + "--", want: true},
		{name: "standard tag", prefix: "vless-reality-" + rule.ID + "--", want: true},
		{name: "different rule in prefix", prefix: "vless-reality-kl-native-klrule_other--"},
		{name: "rule prefix collision", prefix: "vless-reality-kl-native-" + rule.ID + "-extra--"},
		{name: "forged rule", field: "rule_id", value: "klrule_other"},
		{name: "forged customer", field: "customer_id", value: "other"},
		{name: "forged entry", field: "entry_group_id", value: "other"},
		{name: "forged exit", field: "exit_group_id", value: "other"},
		{name: "forged protocol", field: "protocol", value: "udp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := make(map[string]string)
			for k, v := range valid {
				m[k] = v
			}
			if tt.field != "" {
				m[tt.field] = tt.value
			}
			prefix := tt.prefix
			if prefix == "" {
				prefix = "vless-reality-kl-native-" + rule.ID + "--"
			}
			metadata, _ := json.Marshal(m)
			raw, _ := json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": prefix + base64.RawURLEncoding.EncodeToString(metadata)}}})
			if got := hasXrayInboundTag(raw, "vless-reality-fwd_"+rule.ID, rule); got != tt.want {
				t.Fatalf("accepted=%v want=%v", got, tt.want)
			}
		})
	}
	for _, tag := range []string{"vless-reality-kl-native-" + rule.ID + "--invalid*", "vless-reality-kl-native-" + rule.ID + "--"} {
		raw, _ := json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": tag}}})
		if hasXrayInboundTag(raw, "unused", rule) {
			t.Fatal("malformed metadata accepted")
		}
	}
}

func TestKLNativeDelayedUsageKeepsHistoricalVerification(t *testing.T) {
	for _, invalid := range []string{"", "hash", "node", "generation", "receipt", "attempt", "future", "ownership"} {
		t.Run(invalid, func(t *testing.T) {
			repo := historicalUsageFixture(t)
			h := &repo.historical
			var bundle agentv1.ConfigurationBundle
			if err := json.Unmarshal(h.Config.Config, &bundle); err != nil {
				t.Fatal(err)
			}
			f := &bundle.Fragments[0]
			f.Engine = agentv1.EngineXray
			f.GroupID = generations.ForwardingFragmentID(f.Engine, "rule-1")
			m := map[string]string{"rule_id": "rule-1", "customer_id": "customer-1", "entry_group_id": "entry-1", "exit_group_id": "exit-1", "protocol": "tcp"}
			if invalid == "ownership" {
				m["customer_id"] = "forged"
			}
			encoded, _ := json.Marshal(m)
			f.Config, _ = json.Marshal(map[string]any{"inbounds": []map[string]string{{"tag": "vless-reality-kl-native-rule-1--" + base64.RawURLEncoding.EncodeToString(encoded)}}})
			h.Config.Config, _ = json.Marshal(bundle)
			h.Config.ConfigSHA256 = generations.SHA256Hex(h.Config.Config)
			for phase, r := range h.ApplyResults {
				r.EngineMode = string(agentv1.EngineXray)
				r.ConfigSHA256 = h.Config.ConfigSHA256
				h.ApplyResults[phase] = r
			}
			generation := agentv1.NodeConfigGeneration(h.Config.Generation)
			switch invalid {
			case "hash":
				h.Config.ConfigSHA256 = "invalid"
			case "node":
				h.Config.NodeID = "other"
			case "generation":
				h.Config.Generation++
			case "receipt":
				delete(h.ApplyResults, agentv1.ApplyPhaseVerify)
			case "attempt":
				h.AttemptID = "other"
			case "future":
				h.Config.CreatedAt = time.Now().Add(time.Hour)
			}
			p := NewLegacyRuleMetadataProvider(repo).(GenerationRuleMetadataProvider)
			got, err := p.ResolveRuleUsageMetadataAt(context.Background(), "node-1", "rule-1", generation, time.Now())
			if invalid != "" {
				if err == nil {
					t.Fatal("invalid historical evidence accepted")
				}
				return
			}
			if err != nil || got.CustomerID != "customer-1" || got.EntryMultiplierMicros != 2_000_000 {
				t.Fatalf("historical result=%+v err=%v", got, err)
			}
		})
	}
}
