package vlessconfig

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func validRule() forwarding.Rule {
	return forwarding.Rule{
		ID: "rule-vless", IngressProtocol: forwarding.IngressVLESSReality,
		Protocol: forwarding.ProtocolTCP, EgressMode: forwarding.EgressDirect,
		Targets: []forwarding.Target{{Host: "target.example.test", Port: 443}},
	}
}

func validProfile() RealityProfile {
	return RealityProfile{
		ServerName: "www.example.com", Dest: "www.example.com:443",
		PrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		ShortIDs:   []string{"0123456789abcdef"}, Flow: "xtls-rprx-vision",
	}
}

func validIdentity() Identity {
	return Identity{UUID: "ed08862b-0b98-4a96-b860-503c97b78f55"}
}

func TestCompileDirectRealityVision(t *testing.T) {
	raw, err := CompileDirect(validRule(), validIdentity(), validProfile(), Listener{Address: "0.0.0.0", Port: 24443})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Port     int    `json:"port"`
			Settings struct {
				Clients []struct{ ID, Flow string } `json:"clients"`
			} `json:"settings"`
			StreamSettings struct {
				Security        string `json:"security"`
				RealitySettings struct {
					Dest       string   `json:"dest"`
					PrivateKey string   `json:"privateKey"`
					ShortIDs   []string `json:"shortIds"`
				} `json:"realitySettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Settings struct {
				Redirect string `json:"redirect"`
			} `json:"settings"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 1 || config.Inbounds[0].Protocol != "vless" || config.Inbounds[0].Port != 24443 || config.Inbounds[0].StreamSettings.Security != "reality" {
		t.Fatalf("unexpected inbound: %s", raw)
	}
	client := config.Inbounds[0].Settings.Clients[0]
	if client.ID != validIdentity().UUID || client.Flow != "xtls-rprx-vision" {
		t.Fatalf("unexpected client: %#v", client)
	}
	reality := config.Inbounds[0].StreamSettings.RealitySettings
	if reality.Dest != "www.example.com:443" || reality.PrivateKey == "" || len(reality.ShortIDs) != 1 {
		t.Fatalf("unexpected Reality settings: %#v", reality)
	}
	if len(config.Outbounds) != 1 || config.Outbounds[0].Protocol != "freedom" || config.Outbounds[0].Settings.Redirect != "target.example.test:443" {
		t.Fatalf("unexpected outbound: %#v", config.Outbounds)
	}
	if len(config.Routing.Rules) != 1 || len(config.Routing.Rules[0].InboundTag) != 1 ||
		config.Routing.Rules[0].InboundTag[0] != config.Inbounds[0].Tag ||
		config.Routing.Rules[0].OutboundTag != config.Outbounds[0].Tag {
		t.Fatalf("inbound is not routed to its own outbound: %s", raw)
	}
}

func TestCompileDirectWithSOCKS5KeepsVLESSIngressAndAddsLandingOutbound(t *testing.T) {
	rule := validRule()
	rule.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	rule.Targets = nil
	upstream := SOCKS5Upstream{Hostname: "landing.example.test", Port: 1080, Username: "landing-user", Password: "landing-password"}
	raw, err := CompileDirectWithSOCKS5(rule, validIdentity(), validProfile(), Listener{Address: "0.0.0.0", Port: 24443}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds  []struct{ Protocol string } `json:"inbounds"`
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Settings struct {
				Redirect string `json:"redirect"`
				Servers  []struct {
					Address string                        `json:"address"`
					Port    int                           `json:"port"`
					Users   []struct{ User, Pass string } `json:"users"`
				} `json:"servers"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 1 || config.Inbounds[0].Protocol != "vless" || len(config.Outbounds) != 1 {
		t.Fatalf("unexpected VLESS/SOCKS shape: %s", raw)
	}
	if config.Outbounds[0].Protocol != "socks" || config.Outbounds[0].Settings.Redirect != "" {
		t.Fatalf("SOCKS outbound unexpectedly fixes or bypasses the client target: %s", raw)
	}
	server := config.Outbounds[0].Settings.Servers[0]
	if server.Address != upstream.Hostname || server.Port != upstream.Port || len(server.Users) != 1 || server.Users[0].User != upstream.Username || server.Users[0].Pass != upstream.Password {
		t.Fatalf("SOCKS5 landing credentials missing: %s", raw)
	}
}

func TestCompileRejectsMissingSecretsAndUnsupportedShapes(t *testing.T) {
	base := validRule()
	for name, mutate := range map[string]func(*forwarding.Rule){
		"tcp ingress": func(rule *forwarding.Rule) { rule.IngressProtocol = forwarding.IngressTCP },
		"udp target":  func(rule *forwarding.Rule) { rule.Protocol = forwarding.ProtocolUDP },
		"exit route":  func(rule *forwarding.Rule) { rule.ExitGroupID = "exit"; rule.EgressMode = forwarding.EgressExitGroup },
		"many targets": func(rule *forwarding.Rule) {
			rule.Targets = append(rule.Targets, forwarding.Target{Host: "second.example.test", Port: 443})
		},
	} {
		t.Run(name, func(t *testing.T) {
			rule := base
			mutate(&rule)
			if _, err := CompileDirect(rule, validIdentity(), validProfile(), Listener{Address: "127.0.0.1", Port: 24443}); !errors.Is(err, ErrUnsupportedRoute) {
				t.Fatalf("error = %v, want ErrUnsupportedRoute", err)
			}
		})
	}
	profile := validProfile()
	profile.PrivateKey = "not-a-key"
	if _, err := CompileDirect(base, validIdentity(), profile, Listener{Address: "127.0.0.1", Port: 24443}); !errors.Is(err, ErrInvalidRealityKey) {
		t.Fatalf("private key error = %v, want ErrInvalidRealityKey", err)
	}
	profile = validProfile()
	profile.ShortIDs = []string{"secret\nvalue"}
	if _, err := CompileDirect(base, validIdentity(), profile, Listener{Address: "127.0.0.1", Port: 24443}); !errors.Is(err, ErrRuntimeMaterial) {
		t.Fatalf("short id error = %v, want ErrRuntimeMaterial", err)
	}
	if _, err := CompileDirect(base, Identity{}, validProfile(), Listener{Address: "127.0.0.1", Port: 24443}); !errors.Is(err, ErrRuntimeMaterial) {
		t.Fatalf("identity error = %v, want ErrRuntimeMaterial", err)
	}
}

func TestCompilerDoesNotEchoSecretInValidationErrors(t *testing.T) {
	secret := "not-a-reality-private-key-secret"
	profile := validProfile()
	profile.PrivateKey = secret
	_, err := CompileDirect(validRule(), validIdentity(), profile, Listener{Address: "127.0.0.1", Port: 24443})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), validIdentity().UUID) {
		t.Fatalf("validation error disclosed secret: %v", err)
	}
	if !errors.Is(err, ErrInvalidRealityKey) && !errors.Is(err, ErrRuntimeMaterial) {
		t.Fatalf("error = %v, want bounded material error", err)
	}
}

func TestCompileDirectWithProbeSeparatesIdentityAndEchoRoute(t *testing.T) {
	const probeUUID = "615db532-728a-4a34-8353-873b34d12de2"
	raw, err := CompileDirectWithProbe(validRule(), validIdentity(), validProfile(), Listener{Address: "127.0.0.1", Port: 24443}, ProbeIdentity{UUID: probeUUID, EchoPort: 31998})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct{ ID, Email, Flow string } `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Redirect string `json:"redirect"`
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
	if len(config.Inbounds) != 1 || len(config.Inbounds[0].Settings.Clients) != 2 || len(config.Outbounds) != 2 || len(config.Routing.Rules) != 2 {
		t.Fatalf("invalid probe isolation shape: %s", raw)
	}
	clients := config.Inbounds[0].Settings.Clients
	if clients[0].ID != validIdentity().UUID || clients[0].Email != "" || clients[1].ID != probeUUID || clients[1].Email != "hl-probe-rule-vless" || clients[1].Flow != "xtls-rprx-vision" {
		t.Fatal("probe identity was not isolated from customer identity")
	}
	if config.Outbounds[0].Settings.Redirect != "target.example.test:443" || config.Outbounds[1].Settings.Redirect != "127.0.0.1:31998" || len(config.Routing.Rules[0].User) != 1 || config.Routing.Rules[0].User[0] != clients[1].Email || config.Routing.Rules[0].OutboundTag != config.Outbounds[1].Tag || config.Routing.Rules[1].OutboundTag != config.Outbounds[0].Tag {
		t.Fatal("probe echo route or customer fallback is incorrect")
	}
}

func TestCompileDirectWithProbeRejectsCustomerIdentityAndInvalidPort(t *testing.T) {
	for _, probe := range []ProbeIdentity{
		{UUID: validIdentity().UUID, EchoPort: 31998},
		{UUID: "615db532-728a-4a34-8353-873b34d12de2", EchoPort: 0},
	} {
		if _, err := CompileDirectWithProbe(validRule(), validIdentity(), validProfile(), Listener{Address: "127.0.0.1", Port: 24443}, probe); !errors.Is(err, ErrRuntimeMaterial) {
			t.Fatalf("invalid probe accepted: %v", err)
		}
	}
}
