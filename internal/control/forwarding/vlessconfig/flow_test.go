package vlessconfig

import (
	"encoding/json"
	"testing"
)

func TestExplicitEmptyFlowCompilesCustomerAndProbeWithoutVision(t *testing.T) {
	rule, profile := validRule(), validProfile()
	rule.VLESSFlowExplicit = true
	profile.Flow = ""
	raw, err := CompileDirectWithProbe(rule, validIdentity(), profile, Listener{Address: "127.0.0.1", Port: 24443}, ProbeIdentity{UUID: "615db532-728a-4a34-8353-873b34d12de2", EchoPort: 19090})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Settings struct{ Clients []struct{ ID, Flow string } }
		}
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 1 || len(config.Inbounds[0].Settings.Clients) != 2 {
		t.Fatal("missing customer or isolated probe")
	}
	for _, client := range config.Inbounds[0].Settings.Clients {
		if client.Flow != "" {
			t.Fatal("compiler changed explicit empty flow")
		}
	}
	profile.Flow = "xtls-rprx-vision"
	if _, err := CompileDirect(rule, validIdentity(), profile, Listener{Address: "127.0.0.1", Port: 24443}); err == nil {
		t.Fatal("mismatched profile flow accepted")
	}
}
