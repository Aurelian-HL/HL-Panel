package vlessconfig

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func TestEgressEditsPreserveCustomerAndProbeListeners(t *testing.T) {
	for _, mode := range []string{"direct", "socks-address", "socks-password"} {
		t.Run(mode, func(t *testing.T) {
			rule := validRule()
			upstream := SOCKS5Upstream{Hostname: "landing.example.test", Port: 1080, Username: "user", Password: "before"}
			if mode != "direct" {
				rule.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
				rule.Targets = nil
			}
			compile := func() map[string]json.RawMessage {
				t.Helper()
				probe := ProbeIdentity{UUID: "615db532-728a-4a34-8353-873b34d12de2", EchoPort: 31998}
				listener := Listener{Address: "127.0.0.1", Port: 24443}
				var raw []byte
				var err error
				if mode == "direct" {
					raw, err = CompileDirectWithProbe(rule, validIdentity(), validProfile(), listener, probe)
				} else {
					raw, err = CompileDirectWithProbeAndSOCKS5(rule, validIdentity(), validProfile(), listener, probe, upstream)
				}
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]json.RawMessage
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				return doc
			}
			old := compile()
			switch mode {
			case "direct":
				rule.Targets[0].Port++
			case "socks-address":
				upstream.Hostname = "other.example.test"
			case "socks-password":
				upstream.Password = "after"
			}
			next := compile()
			if string(old["inbounds"]) != string(next["inbounds"]) {
				t.Fatal("egress edit changed customer handshake, identity or listener")
			}
			var oldOut, nextOut []map[string]any
			_ = json.Unmarshal(old["outbounds"], &oldOut)
			_ = json.Unmarshal(next["outbounds"], &nextOut)
			if oldOut[0]["tag"] == nextOut[0]["tag"] || !reflect.DeepEqual(oldOut[1], nextOut[1]) {
				t.Fatal("egress version did not change independently of probe")
			}
			var oldRouting, nextRouting struct {
				Rules []map[string]any `json:"rules"`
			}
			_ = json.Unmarshal(old["routing"], &oldRouting)
			_ = json.Unmarshal(next["routing"], &nextRouting)
			if !reflect.DeepEqual(oldRouting.Rules[0], nextRouting.Rules[0]) || nextRouting.Rules[1]["outboundTag"] != nextOut[0]["tag"] {
				t.Fatal("probe routing changed or customer route not switched")
			}
		})
	}
}
