package importedconfig_test

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/importedconfig"
	"github.com/hongle/hl-panel/internal/control/importedconfig/testfixture"
)

func nativeOptions(p importedconfig.NativeProfile) importedconfig.NativeOptions {
	return importedconfig.NativeOptions{RuleID: "rule-one", CustomerID: "customer-one", EntryGroupID: "group-one", UUID: p.UUID, PrivateKey: p.PrivateKey, ListenAddress: "127.0.0.1", ListenPort: p.Port}
}

func TestNativeFragmentPreservesCredentialsAndScopesRoutes(t *testing.T) {
	a := nativeFixture()
	c := a["config"].(map[string]any)
	sniff := map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic", "fakedns"}, "metadataOnly": false, "routeOnly": false}
	c["inbounds"].([]any)[0].(map[string]any)["sniffing"] = sniff
	a["inbounds"].([]any)[0].(map[string]any)["raw"].(map[string]any)["sniffing"] = sniff
	raw, _ := json.Marshal(a)
	p, err := importedconfig.PrepareNative(raw, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Flow != "" || len(p.ServerNames) != 2 || len(p.ShortIDs) != 2 {
		t.Fatal("native handshake shape changed")
	}
	fragment, err := importedconfig.CompileNativeFragment(p.Config, nativeOptions(p))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if json.Unmarshal(fragment, &got) != nil {
		t.Fatal("invalid JSON")
	}
	in := got["inbounds"].([]any)[0].(map[string]any)
	for _, field := range []string{"settings", "streamSettings", "sniffing"} {
		want, _ := json.Marshal(c["inbounds"].([]any)[0].(map[string]any)[field])
		actual, _ := json.Marshal(in[field])
		if string(want) != string(actual) {
			t.Fatalf("native %s changed", field)
		}
	}
	tag := in["tag"].(string)
	metadata, err := base64.RawURLEncoding.DecodeString(strings.SplitN(tag, "--", 2)[1])
	var meta map[string]string
	if err != nil || json.Unmarshal(metadata, &meta) != nil || meta["rule_id"] != "rule-one" || meta["customer_id"] != "customer-one" || meta["protocol"] != "tcp" {
		t.Fatal("usage attribution lost")
	}
	routes := got["routing"].(map[string]any)["rules"].([]any)
	if len(routes) != 4 {
		t.Fatal("route order or fallback lost")
	}
	for _, r := range routes {
		if !reflect.DeepEqual(r.(map[string]any)["inboundTag"], []any{tag}) {
			t.Fatal("cross-rule selector survived")
		}
	}
	if !reflect.DeepEqual(routes[0].(map[string]any)["ip"], []any{"geoip:private"}) || !reflect.DeepEqual(routes[1].(map[string]any)["protocol"], []any{"bittorrent"}) {
		t.Fatal("global blocks reordered")
	}
	if got["log"] != nil || got["api"] != nil || got["metrics"] != nil {
		t.Fatal("management configuration survived")
	}
	other := nativeOptions(p)
	other.RuleID = "rule-two"
	second, err := importedconfig.CompileNativeFragment(p.Config, other)
	if err != nil {
		t.Fatal(err)
	}
	var got2 map[string]any
	_ = json.Unmarshal(second, &got2)
	tags := map[string]bool{}
	for _, o := range got["outbounds"].([]any) {
		tags[o.(map[string]any)["tag"].(string)] = true
	}
	for _, o := range got2["outbounds"].([]any) {
		if tags[o.(map[string]any)["tag"].(string)] {
			t.Fatal("outbound tags collide across customers")
		}
	}
}

func TestNativeUDPKeepsRealityTCPIngressAndOriginalOutbound(t *testing.T) {
	p, err := importedconfig.PrepareNative(testfixture.Raw(), 1)
	if err != nil {
		t.Fatal(err)
	}
	options := nativeOptions(p)
	options.Protocol = "udp"
	fragment, err := importedconfig.CompileNativeFragment(p.Config, options)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(fragment, &config); err != nil {
		t.Fatal(err)
	}
	in := config["inbounds"].([]any)[0].(map[string]any)
	if in["protocol"] != "vless" {
		t.Fatalf("native UDP changed ingress protocol: %#v", in["protocol"])
	}
	stream := in["streamSettings"].(map[string]any)
	if stream["network"] != "tcp" || stream["security"] != "reality" {
		t.Fatalf("native UDP changed Reality/TCP transport: %#v", stream)
	}
	if _, ok := stream["realitySettings"].(map[string]any); !ok {
		t.Fatal("native UDP lost Reality settings")
	}
	outs := config["outbounds"].([]any)
	if len(outs) != 1 || outs[0].(map[string]any)["protocol"] != "freedom" {
		t.Fatalf("native UDP changed original outbound: %#v", outs)
	}
	outSettings := outs[0].(map[string]any)["settings"].(map[string]any)
	if _, redirected := outSettings["redirect"]; redirected {
		t.Fatal("native UDP introduced a fixed redirect")
	}
	routes := config["routing"].(map[string]any)["rules"].([]any)
	if len(routes) != 1 {
		t.Fatalf("native UDP changed original route count: %#v", routes)
	}
	tag := in["tag"].(string)
	metadata, err := base64.RawURLEncoding.DecodeString(strings.SplitN(tag, "--", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]string
	if err := json.Unmarshal(metadata, &meta); err != nil || meta["protocol"] != "udp" {
		t.Fatalf("native UDP usage metadata = %#v", meta)
	}
}

func TestNativeRejectsUnknownTargetProtocol(t *testing.T) {
	p, err := importedconfig.PrepareNative(testfixture.Raw(), 1)
	if err != nil {
		t.Fatal(err)
	}
	options := nativeOptions(p)
	options.Protocol = "quic"
	if _, err := importedconfig.CompileNativeFragment(p.Config, options); err == nil {
		t.Fatal("unknown native target protocol accepted")
	}
}

func TestNativeRejectsUDPWhenImportedClientUsesVisionFlow(t *testing.T) {
	document := testfixture.Document()
	config := document["config"].(map[string]any)
	inbound := config["inbounds"].([]any)[0].(map[string]any)
	settings := inbound["settings"].(map[string]any)
	settings["clients"].([]any)[0].(map[string]any)["flow"] = "xtls-rprx-vision"
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	p, err := importedconfig.PrepareNative(raw, 1)
	if err != nil {
		t.Fatal(err)
	}
	options := nativeOptions(p)
	options.Protocol = "udp"
	if _, err := importedconfig.CompileNativeFragment(p.Config, options); err == nil {
		t.Fatal("native UDP relabeled a TCP Vision flow")
	}
}

func TestNativeProbeIsRestrictedAndInvalidOptionsRejected(t *testing.T) {
	p, err := importedconfig.PrepareNative(testfixture.Raw(), 1)
	if err != nil {
		t.Fatal(err)
	}
	options := nativeOptions(p)
	options.ProbeUUID = "22222222-2222-4222-8222-222222222222"
	options.EchoPort = 34567
	raw, err := importedconfig.CompileNativeFragment(p.Config, options)
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	routes := c["routing"].(map[string]any)["rules"].([]any)
	probeRoute := routes[0].(map[string]any)
	if !reflect.DeepEqual(probeRoute["user"], []any{"hl-probe-rule-one"}) {
		t.Fatal("probe did not take precedence")
	}
	for _, out := range c["outbounds"].([]any) {
		o := out.(map[string]any)
		if o["tag"] == probeRoute["outboundTag"] {
			settings := o["settings"].(map[string]any)
			if settings["redirect"] != "127.0.0.1:34567" || len(settings["finalRules"].([]any)) != 1 {
				t.Fatal("probe not loopback restricted")
			}
		}
	}
	for _, mutate := range []func(*importedconfig.NativeOptions){
		func(o *importedconfig.NativeOptions) { o.UUID = "22222222-2222-4222-8222-222222222222" },
		func(o *importedconfig.NativeOptions) { o.PrivateKey = "wrong" },
		func(o *importedconfig.NativeOptions) { o.RuleID = "" },
		func(o *importedconfig.NativeOptions) { o.ListenAddress = "example.org" },
		func(o *importedconfig.NativeOptions) { o.ListenPort = 0 },
		func(o *importedconfig.NativeOptions) { o.ProbeUUID = p.UUID; o.EchoPort = 34567 },
		func(o *importedconfig.NativeOptions) { o.ProbeUUID = "22222222-2222-4222-8222-222222222222" },
	} {
		o := nativeOptions(p)
		mutate(&o)
		if _, err := importedconfig.CompileNativeFragment(p.Config, o); err == nil {
			t.Fatal("invalid material association accepted")
		}
	}
	public, _ := json.Marshal(p)
	if strings.Contains(string(public), p.UUID) || strings.Contains(string(public), p.PrivateKey) || strings.Contains(string(public), "mldsa65Seed") {
		t.Fatal("confidential material leaked")
	}
}
