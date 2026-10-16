package importedconfig_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/control/importedconfig"
)

func multiLineFixture() map[string]any {
	a := nativeFixture()
	c := a["config"].(map[string]any)
	// Deep-copy source and native records to avoid sharing nested credentials.
	clone := func(v any) map[string]any {
		raw, _ := json.Marshal(v)
		var copy map[string]any
		_ = json.Unmarshal(raw, &copy)
		return copy
	}
	row := clone(a["inbounds"].([]any)[0])
	row["id"], row["port"], row["owner_id"] = 2, 25511, ""
	raw := row["raw"].(map[string]any)
	raw["id"], raw["port"] = 2, 25511
	raw["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)["id"] = "22222222-2222-4222-8222-222222222222"
	a["inbounds"] = append(a["inbounds"].([]any), row)
	inbound := clone(c["inbounds"].([]any)[0])
	inbound["port"], inbound["tag"] = 25511, "inbound-25511"
	inbound["settings"] = raw["settings"]
	inbound["sniffing"] = map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic", "fakedns"}, "routeOnly": false}
	inbound["streamSettings"].(map[string]any)["tcpSettings"] = map[string]any{"header": map[string]any{"type": "none"}}
	inbound["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)["maxTimediff"] = 60000
	raw["sniffing"], raw["streamSettings"] = inbound["sniffing"], inbound["streamSettings"]
	c["inbounds"] = append(c["inbounds"].([]any), inbound)
	rules := c["routing"].(map[string]any)["rules"].([]any)
	rules[len(rules)-1].(map[string]any)["inboundTag"] = []string{"inbound-25510", "inbound-25511"}
	return a
}

func TestSelectionPreservesNativeMaterialAndRemovesExcludedListeners(t *testing.T) {
	a := multiLineFixture()
	raw, _ := json.Marshal(a)
	compiled, plan, err := importedconfig.CompileSelectionValidation(raw, []int64{2})
	if err != nil || len(plan.Lines) != 1 || plan.Lines[0].SourceID != 2 || plan.Unassigned != 1 {
		t.Fatal("explicit selection failed", err)
	}
	var got map[string]any
	_ = json.Unmarshal(compiled, &got)
	ins := got["inbounds"].([]any)
	if len(ins) != 1 || ins[0].(map[string]any)["port"] != float64(25511) || ins[0].(map[string]any)["listen"] != "127.0.0.1" {
		t.Fatal("excluded listener survived or loopback was lost")
	}
	original := a["config"].(map[string]any)["inbounds"].([]any)[2].(map[string]any)
	for _, key := range []string{"settings", "streamSettings", "sniffing"} {
		wantRaw, _ := json.Marshal(original[key])
		var want any
		_ = json.Unmarshal(wantRaw, &want)
		if !reflect.DeepEqual(ins[0].(map[string]any)[key], want) {
			t.Fatal("native material was changed", key)
		}
	}
	rules := got["routing"].(map[string]any)["rules"].([]any)
	if len(rules) != 3 || rules[0].(map[string]any)["ip"] == nil || rules[1].(map[string]any)["protocol"] == nil ||
		!reflect.DeepEqual(rules[2].(map[string]any)["inboundTag"], []any{"inbound-25511"}) {
		t.Fatal("route order or selection changed")
	}
	after, _ := json.Marshal(a)
	if string(after) != string(raw) {
		t.Fatal("input mutated")
	}
}

func TestSelectionRejectsImplicitAllDuplicateAndUnknownIDs(t *testing.T) {
	raw, _ := json.Marshal(multiLineFixture())
	for _, ids := range [][]int64{nil, {}, {1, 1}, {0}, {-1}, {3}, {1, 3}} {
		if _, _, err := importedconfig.CompileSelectionValidation(raw, ids); err == nil {
			t.Fatal("invalid selection was accepted")
		}
	}
	all, fullPlan, err := importedconfig.CompileLoopbackValidation(raw)
	selected, selectedPlan, err2 := importedconfig.CompileSelectionValidation(raw, []int64{2, 1})
	if err != nil || err2 != nil || !reflect.DeepEqual(fullPlan, selectedPlan) {
		t.Fatal("all selection changed plan")
	}
	var fullConfig, selectedConfig any
	_ = json.Unmarshal(all, &fullConfig)
	_ = json.Unmarshal(selected, &selectedConfig)
	if !reflect.DeepEqual(fullConfig, selectedConfig) {
		t.Fatal("explicit all selection changed native config")
	}
}
