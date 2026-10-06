package vless

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestSOCKSLandingDoesNotChangeCustomerProtocolOrURI(t *testing.T) {
	profile := testProfile()
	before, _ := URI(profile)
	listener := Listener{Address: "127.0.0.1", Port: 8443, CertificateFile: filepath.Join(t.TempDir(), "certificate.pem"), PrivateKeyFile: filepath.Join(t.TempDir(), "key.pem")}
	upstream := &SOCKS5Upstream{Hostname: "landing.example.test", Port: 1080, Username: "landing-user", Password: "test-secret"}
	config, err := Compile(profile, listener, Egress{Mode: EgressSOCKSSingle, SOCKS5: upstream})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Inbounds  []struct{ Protocol string }
		Outbounds []struct {
			Protocol string
			Settings struct {
				Servers []struct {
					Address string
					Users   []struct{ User, Pass string }
				}
			}
		}
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 1 || parsed.Inbounds[0].Protocol != "vless" || len(parsed.Outbounds) != 1 || parsed.Outbounds[0].Protocol != "socks" {
		t.Fatal("inbound and landing protocols were confused")
	}
	server := parsed.Outbounds[0].Settings.Servers[0]
	if server.Address != upstream.Hostname || len(server.Users) != 1 || server.Users[0].Pass != upstream.Password {
		t.Fatal("landing authentication missing")
	}
	after, _ := URI(profile)
	if before != after || strings.Contains(after, upstream.Password) || strings.Contains(after, upstream.Hostname) {
		t.Fatal("landing changed or leaked into customer URI")
	}
	ordinaryJSON, _ := json.Marshal(upstream)
	if strings.Contains(string(ordinaryJSON), upstream.Password) || strings.Contains(string(ordinaryJSON), upstream.Username) {
		t.Fatal("landing credentials leaked into inventory JSON")
	}
}

func TestEgressRejectsAmbiguousAndUnsupportedPaths(t *testing.T) {
	for _, egress := range []Egress{
		{Mode: ""}, {Mode: "RELAY_EXIT_GROUP"}, {Mode: EgressSOCKSSingle},
		{Mode: EgressDirect, SOCKS5: &SOCKS5Upstream{}},
		{Mode: EgressSOCKSSingle, SOCKS5: &SOCKS5Upstream{Hostname: "host", Port: 1080, Password: "unpaired-secret"}},
	} {
		outbound, err := egress.nativeOutbound()
		if err == nil || outbound != nil {
			t.Fatal("invalid route silently defaulted to DIRECT")
		}
		if strings.Contains(err.Error(), "unpaired-secret") {
			t.Fatal("error leaked landing password")
		}
	}
}
