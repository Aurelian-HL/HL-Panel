package vless

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func testProfile() Profile {
	return Profile{Endpoint: Endpoint{Hostname: "entry.example.test", Port: 443, Name: "广州入口 / 单服务"},
		Identity: Identity{UUID: "ed08862b-0b98-4a96-b860-503c97b78f55"}, TLS: TLSProfile{ServerName: "entry.example.test"}}
}

func TestSingleURIAndIndependentBackendBindings(t *testing.T) {
	profile := testProfile()
	link, err := URI(profile)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "entry.example.test:443" || parsed.Fragment != profile.Endpoint.Name || parsed.Query().Get("security") != "tls" || strings.ContainsAny(link, "\r\n") {
		t.Fatal("subscription is not one encoded stable endpoint")
	}
	for _, host := range []string{"127.0.0.1", "127.0.0.2"} {
		config, err := CompileDirect(profile, Listener{Address: host, Port: 8443,
			CertificateFile: filepath.Join(t.TempDir(), "cert.pem"), PrivateKeyFile: filepath.Join(t.TempDir(), "key.pem")})
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(config) || strings.Contains(string(config), profile.Endpoint.Hostname) {
			t.Fatal("public endpoint leaked into backend binding")
		}
		after, _ := URI(profile)
		if after != link {
			t.Fatal("backend changed customer URI")
		}
	}
	profile.Endpoint.Hostname = "2001:db8::1"
	link, err = URI(profile)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ = url.Parse(link)
	if parsed.Host != "[2001:db8::1]:443" {
		t.Fatal("IPv6 URI is not bracketed")
	}
}

func TestInvalidProfilesDoNotEchoSecrets(t *testing.T) {
	mutations := []func(*Profile){
		func(p *Profile) { p.Endpoint.Hostname = "user:secret@host" },
		func(p *Profile) { p.Endpoint.Hostname = "https://host/path" },
		func(p *Profile) { p.Endpoint.Hostname = "host\nsecond" },
		func(p *Profile) { p.Identity.UUID = "sensitive-invalid-identity" },
		func(p *Profile) { p.TLS.ServerName = "" },
		func(p *Profile) { p.Identity.Flow = "unsupported" },
	}
	for _, mutate := range mutations {
		p := testProfile()
		mutate(&p)
		link, err := URI(p)
		if err == nil || link != "" {
			t.Fatal("invalid profile produced URI")
		}
		if strings.Contains(err.Error(), p.Identity.UUID) || strings.Contains(err.Error(), "secret@") {
			t.Fatal("error disclosed identity")
		}
	}
	encoded, _ := json.Marshal(testProfile().Identity)
	if strings.Contains(string(encoded), testProfile().Identity.UUID) {
		t.Fatal("ordinary identity JSON leaks UUID")
	}
}
