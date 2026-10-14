package vlessconfig

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func TestProbeLoopbackPermissionIsIsolatedToEchoTCPPort(t *testing.T) {
	const probeUUID = "615db532-728a-4a34-8353-873b34d12de2"
	for _, mode := range []string{"direct", "socks5"} {
		t.Run(mode, func(t *testing.T) {
			rule := validRule()
			probe := ProbeIdentity{UUID: probeUUID, EchoPort: 31998}
			listener := Listener{Address: "127.0.0.1", Port: 24443}
			var raw []byte
			var err error
			if mode == "socks5" {
				rule.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
				raw, err = CompileDirectWithProbeAndSOCKS5(rule, validIdentity(), validProfile(), listener, probe, SOCKS5Upstream{Hostname: "landing.example.test", Port: 1080})
			} else {
				raw, err = CompileDirectWithProbe(rule, validIdentity(), validProfile(), listener, probe)
			}
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Outbounds []struct {
					Tag      string `json:"tag"`
					Settings struct {
						Redirect   string `json:"redirect"`
						FinalRules []struct {
							Action  string   `json:"action"`
							Network string   `json:"network"`
							Port    int      `json:"port"`
							IP      []string `json:"ip"`
						} `json:"finalRules"`
					} `json:"settings"`
				} `json:"outbounds"`
				Routing struct {
					Rules []struct {
						User        []string `json:"user"`
						OutboundTag string   `json:"outboundTag"`
					} `json:"rules"`
				} `json:"routing"`
			}
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			if len(config.Outbounds) != 2 || len(config.Routing.Rules) != 2 {
				t.Fatal("expected separate customer and probe paths")
			}
			customer, echo := config.Outbounds[0], config.Outbounds[1]
			if mode == "socks5" && len(customer.Settings.FinalRules) != 0 {
				t.Fatal("probe exception leaked into the SOCKS outbound")
			}
			if mode == "direct" && (len(customer.Settings.FinalRules) != 1 || len(customer.Settings.FinalRules[0].IP) != 0 || customer.Settings.FinalRules[0].Port != rule.Targets[0].Port || customer.Settings.Redirect != "target.example.test:443") {
				t.Fatal("DIRECT must allow only its fixed configured redirect, not inherit the probe exception")
			}
			if len(echo.Settings.FinalRules) != 1 || echo.Settings.Redirect != "127.0.0.1:31998" {
				t.Fatal("missing bounded echo exception")
			}
			permission := echo.Settings.FinalRules[0]
			if permission.Action != "allow" || permission.Network != "tcp" || permission.Port != probe.EchoPort || !reflect.DeepEqual(permission.IP, []string{"127.0.0.1/32", "::1/128"}) {
				t.Fatal("echo exception must grant only exact loopback TCP destinations at the echo port")
			}
			if config.Routing.Rules[0].OutboundTag != echo.Tag || !reflect.DeepEqual(config.Routing.Rules[0].User, []string{"hl-probe-" + rule.ID}) || config.Routing.Rules[1].OutboundTag != customer.Tag || len(config.Routing.Rules[1].User) != 0 {
				t.Fatal("echo exception is not isolated by the probe identity route")
			}
		})
	}
}

func TestDirectWithoutProbeAllowsOnlyItsFixedDestination(t *testing.T) {
	for _, test := range []struct {
		host, redirect string
		ip             []string
	}{
		{"127.0.0.1", "127.0.0.1:443", []string{"127.0.0.1/32"}},
		{"10.4.3.2", "10.4.3.2:443", []string{"10.4.3.2/32"}},
		{"::1", "[::1]:443", []string{"::1/128"}},
		{"::ffff:10.4.3.2", "[::ffff:10.4.3.2]:443", []string{"10.4.3.2/32"}},
		{"target.example.test", "target.example.test:443", nil},
	} {
		t.Run(test.host, func(t *testing.T) {
			rule := validRule()
			rule.Targets[0].Host = test.host
			raw, err := CompileDirect(rule, validIdentity(), validProfile(), Listener{Address: "127.0.0.1", Port: 24443})
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Outbounds []struct {
					Settings struct {
						Redirect   string `json:"redirect"`
						FinalRules []struct {
							Action  string   `json:"action"`
							Network string   `json:"network"`
							Port    int      `json:"port"`
							IP      []string `json:"ip"`
						} `json:"finalRules"`
					} `json:"settings"`
				} `json:"outbounds"`
			}
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			if len(config.Outbounds) != 1 || len(config.Outbounds[0].Settings.FinalRules) != 1 || config.Outbounds[0].Settings.Redirect != test.redirect {
				t.Fatal("DIRECT must retain its single fixed redirect and bounded destination permission")
			}
			permission := config.Outbounds[0].Settings.FinalRules[0]
			if permission.Action != "allow" || permission.Network != "tcp" || permission.Port != rule.Targets[0].Port || !reflect.DeepEqual(permission.IP, test.ip) {
				t.Fatal("DIRECT exception must match the configured TCP destination only")
			}
		})
	}
}
