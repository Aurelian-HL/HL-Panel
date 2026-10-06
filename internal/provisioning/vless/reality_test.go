package vless

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func realityTestProfile() Profile {
	private := bytes.Repeat([]byte{0x11}, 32)
	return Profile{
		Endpoint: Endpoint{Hostname: "edge.example.test", Port: 443, Name: "广州 Reality 入口"},
		Identity: Identity{UUID: "04f78a4e-bb88-4a65-86f0-9b7c53f11e0d", Flow: "xtls-rprx-vision"},
		Reality: &RealityProfile{
			ServerName: "www.example.com", PublicKey: base64.RawURLEncoding.EncodeToString(x25519PublicKey(private)),
			PrivateKey: base64.RawURLEncoding.EncodeToString(private), ShortID: "a1b2c3d4",
			Destination: "www.example.com:443", Fingerprint: "chrome", SpiderX: "/index.html",
		},
	}
}

func TestGenerateRealityKeyPairProducesX25519Keys(t *testing.T) {
	publicKey, privateKey, err := GenerateRealityKeyPair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string]string{"public": publicKey, "private": privateKey} {
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(raw) != 32 {
			t.Fatalf("%s key is not a 32-byte raw base64url value", name)
		}
	}
	if publicKey == privateKey {
		t.Fatal("Reality public and private keys unexpectedly match")
	}
	private, _ := base64.RawURLEncoding.DecodeString(privateKey)
	if got := base64.RawURLEncoding.EncodeToString(x25519PublicKey(private)); got != publicKey {
		t.Fatal("generated Reality key pair does not match")
	}
}

func TestRealityVisionURIAndServerConfig(t *testing.T) {
	profile := realityTestProfile()
	link, err := URI(profile)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "vless" {
		t.Fatal("invalid Reality URI")
	}
	query := parsed.Query()
	if query.Get("security") != "reality" || query.Get("flow") != "xtls-rprx-vision" || query.Get("pbk") != profile.Reality.PublicKey || query.Get("sid") != profile.Reality.ShortID || query.Get("sni") != profile.Reality.ServerName || query.Get("fp") != "chrome" || query.Get("spx") != "/index.html" {
		t.Fatalf("Reality URI parameters are incomplete: %s", link)
	}
	config, err := Compile(profile, Listener{Address: "127.0.0.1", Port: 8443}, Egress{Mode: EgressDirect})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(config, &document); err != nil {
		t.Fatal(err)
	}
	inbound := document["inbounds"].([]any)[0].(map[string]any)
	stream := inbound["streamSettings"].(map[string]any)
	if stream["security"] != "reality" || stream["network"] != "tcp" {
		t.Fatalf("unexpected Reality stream settings: %#v", stream)
	}
	reality := stream["realitySettings"].(map[string]any)
	if reality["privateKey"] != profile.Reality.PrivateKey || reality["dest"] != profile.Reality.Destination {
		t.Fatal("Reality server settings were not compiled")
	}
	if strings.Contains(string(config), profile.Reality.PublicKey) {
		t.Fatal("client public key was unexpectedly embedded in server config")
	}
}

func TestCompileDirectForwardUsesOneExplicitTarget(t *testing.T) {
	config, err := CompileDirectForward(realityTestProfile(), Listener{Address: "127.0.0.1", Port: 8443}, ForwardTarget{Host: "origin.example.test", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(config, &document); err != nil {
		t.Fatal(err)
	}
	outbound := document["outbounds"].([]any)[0].(map[string]any)
	settings := outbound["settings"].(map[string]any)
	if settings["redirect"] != "origin.example.test:443" {
		t.Fatalf("unexpected direct target: %#v", settings)
	}
}

func TestRealityRequiresVisionAndServerKey(t *testing.T) {
	profile := realityTestProfile()
	profile.Identity.Flow = ""
	if _, err := URI(profile); err == nil {
		t.Fatal("Reality without Vision flow was accepted")
	}
	profile = realityTestProfile()
	profile.Reality.PrivateKey = ""
	if _, err := Compile(profile, Listener{Address: "127.0.0.1", Port: 8443}, Egress{Mode: EgressDirect}); err == nil {
		t.Fatal("Reality server without private key was accepted")
	}
	profile = realityTestProfile()
	profile.Reality.Destination = "origin.example.test:99999"
	if _, err := URI(profile); err == nil {
		t.Fatal("invalid Reality destination port was accepted")
	}
}

func TestRealityRejectsMismatchedKeysWithoutLeakingPrivateKey(t *testing.T) {
	profile := realityTestProfile()
	otherPrivate := bytes.Repeat([]byte{0x22}, 32)
	profile.Reality.PrivateKey = base64.RawURLEncoding.EncodeToString(otherPrivate)
	for _, run := range []func(Profile) error{
		func(p Profile) error { _, err := URI(p); return err },
		func(p Profile) error {
			_, err := Compile(p, Listener{Address: "127.0.0.1", Port: 8443}, Egress{Mode: EgressDirect})
			return err
		},
	} {
		err := run(profile)
		if err == nil || strings.Contains(err.Error(), profile.Reality.PrivateKey) || strings.Contains(err.Error(), profile.Identity.UUID) {
			t.Fatal("mismatched Reality material was accepted or disclosed")
		}
	}
	profile.Reality.PrivateKey = ""
	if _, err := URI(profile); err != nil {
		t.Fatal("client-only Reality profile must not require the server private key:", err)
	}
}
