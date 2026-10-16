package importedconfig_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/importedconfig"
	"github.com/hongle/hl-panel/internal/control/importedconfig/testfixture"
)

func nativeFixture() map[string]any {
	a := testfixture.Document()
	c := a["config"].(map[string]any)
	c["inbounds"] = append(c["inbounds"].([]any), map[string]any{"tag": "management", "protocol": "tunnel", "port": 62789})
	c["api"] = map[string]any{"tag": "api"}
	c["metrics"] = map[string]any{"listen": "0.0.0.0:11111"}
	c["log"] = map[string]any{"access": "/private/access.log"}
	c["outbounds"] = append(c["outbounds"].([]any), map[string]any{"tag": "blocked", "protocol": "blackhole"})
	c["routing"].(map[string]any)["rules"] = []any{
		map[string]any{"type": "field", "inboundTag": []string{"management"}, "outboundTag": "api"},
		map[string]any{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "blocked"},
		map[string]any{"type": "field", "protocol": []string{"bittorrent"}, "outboundTag": "blocked"},
		map[string]any{"type": "field", "inboundTag": []string{"inbound-25510"}, "outboundTag": "direct"},
	}
	return a
}

func TestLoopbackValidationPreservesCredentialsAndRouteOrder(t *testing.T) {
	a := nativeFixture()
	raw, _ := json.Marshal(a)
	before := string(raw)
	compiled, plan, err := importedconfig.CompileLoopbackValidation(raw)
	if err != nil || len(plan.Lines) != 1 {
		t.Fatal("native validation compilation failed", err)
	}
	var result map[string]any
	if json.Unmarshal(compiled, &result) != nil {
		t.Fatal("invalid compiled JSON")
	}
	c := a["config"].(map[string]any)
	var original map[string]any
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, &original)
	inbound := result["inbounds"].([]any)[0].(map[string]any)
	sourceInbound := original["inbounds"].([]any)[0].(map[string]any)
	if inbound["listen"] != "127.0.0.1" || len(result["inbounds"].([]any)) != 1 {
		t.Fatal("management or nonloopback listener survived")
	}
	for _, field := range []string{"settings", "streamSettings"} {
		if !reflect.DeepEqual(inbound[field], sourceInbound[field]) {
			t.Fatal("connection material changed", field)
		}
	}
	if !reflect.DeepEqual(result["outbounds"], original["outbounds"]) {
		t.Fatal("outbound semantics changed")
	}
	gotRules := result["routing"].(map[string]any)["rules"]
	wantRules := original["routing"].(map[string]any)["rules"].([]any)[1:]
	if !reflect.DeepEqual(gotRules, wantRules) {
		t.Fatal("business route ordering changed")
	}
	if result["api"] != nil || result["metrics"] != nil || strings.Contains(string(compiled), "/private/access.log") {
		t.Fatal("management side effect survived")
	}
	if strings.Contains(string(compiled), "xtls-rprx-vision") || string(raw) != before {
		t.Fatal("empty flow defaulted or input mutated")
	}
}

func TestLoopbackValidationRejectsUnknownRuntimeSemantics(t *testing.T) {
	for _, scenario := range []string{"env", "dns", "outbound_proxy", "global_rule", "mixed_selector", "unknown_tag"} {
		t.Run(scenario, func(t *testing.T) {
			a := nativeFixture()
			c := a["config"].(map[string]any)
			rules := c["routing"].(map[string]any)["rules"].([]any)
			switch scenario {
			case "env":
				c["env"] = map[string]any{"XRAY_LOCATION_ASSET": "/unexpected"}
			case "dns":
				c["dns"] = map[string]any{"servers": []string{"8.8.8.8"}}
			case "outbound_proxy":
				c["outbounds"].([]any)[0].(map[string]any)["proxySettings"] = map[string]any{"tag": "other"}
			case "global_rule":
				rules[1].(map[string]any)["ip"] = []string{"0.0.0.0/0"}
			case "mixed_selector":
				rules[0].(map[string]any)["inboundTag"] = []string{"management", "inbound-25510"}
			case "unknown_tag":
				rules[0].(map[string]any)["inboundTag"] = []string{"unknown"}
			}
			raw, _ := json.Marshal(a)
			if _, _, err := importedconfig.CompileLoopbackValidation(raw); err == nil {
				t.Fatal("unsupported semantics accepted")
			}
		})
	}
}
