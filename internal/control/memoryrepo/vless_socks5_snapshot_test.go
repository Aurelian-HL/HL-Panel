package memoryrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/hongle/hl-panel/internal/control/forwarding"
	provisioningvless "github.com/hongle/hl-panel/internal/provisioning/vless"
)

func TestVLESSSOCKS5CredentialSnapshotRoundTripStaysOutOfRuleProjection(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	request.Name = "vless socks landing"
	request.ListenPort = 12000
	request.IngressProtocol = forwarding.IngressVLESSReality
	request.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.test"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "landing-user"
	request.VLESSSOCKS5Password = "snapshot-secret"
	request.VLESSFlow = "xtls-rprx-vision"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = "AbCdEf0123456789AbCdEf0123456789AbCdEf01234"
	request.RealityShortID = "0123456789abcdef"
	rule, _, err := forwarding.NewService(store, nil).Create(context.Background(), "admin", request, "snapshot-vless-socks")
	if err != nil {
		t.Fatalf("create VLESS rule: %v", err)
	}
	secret := provisioningvless.SOCKS5Upstream{Hostname: "landing.example.test", Port: 1080, Username: "landing-user", Password: "snapshot-secret"}

	public, err := store.ForwardingRule(context.Background(), rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	publicRaw, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(publicRaw, []byte(secret.Password)) {
		t.Fatal("SOCKS5 password leaked through forwarding rule projection")
	}

	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	if !bytes.Contains(raw, []byte(secret.Password)) {
		t.Fatal("confidential snapshot did not preserve SOCKS5 secret")
	}
	var encoded snapshot
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	encodedRule, ok := encoded.ForwardRules[rule.ID]
	if !ok {
		t.Fatal("snapshot did not preserve forwarding rule")
	}
	if encodedRule.VLESSSOCKS5Username != "" {
		t.Fatal("SOCKS5 username leaked through snapshot rule projection")
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	got, ok := restored.VLESSSOCKS5Upstream(rule.ID)
	if !ok || got != secret {
		t.Fatalf("restored SOCKS5 credential = %#v, found=%v", got, ok)
	}
}

func TestVLESSSOCKS5SnapshotRejectsOrphanAndMalformedCredentials(t *testing.T) {
	store, request := forwardingRepositoryFixture(t)
	request.Name = "vless socks landing"
	request.ListenPort = 12001
	request.IngressProtocol = forwarding.IngressVLESSReality
	request.VLESSFlow = "xtls-rprx-vision"
	request.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.test"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "landing-user"
	request.VLESSSOCKS5Password = "snapshot-secret"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = "AbCdEf0123456789AbCdEf0123456789AbCdEf01234"
	request.RealityShortID = "0123456789abcdef"
	rule, _, err := forwarding.NewService(store, nil).Create(context.Background(), "admin", request, "snapshot-vless-socks-invalid")
	if err != nil {
		t.Fatalf("create VLESS rule: %v", err)
	}
	store.vlessSOCKS5Upstreams[rule.ID] = provisioningvless.SOCKS5Upstream{Hostname: "landing.example.test", Port: 1080, Username: "user", Password: "password"}
	raw, err := store.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var state snapshot
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.VLESSSOCKS5Upstreams["missing-rule"] = storedVLESSSOCKS5Upstream{Hostname: "landing.example.test", Port: 1080}
	corrupt, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(corrupt); err == nil {
		t.Fatal("orphan SOCKS5 snapshot record was accepted")
	}

	state.VLESSSOCKS5Upstreams = map[string]storedVLESSSOCKS5Upstream{
		rule.ID: {Hostname: "landing.example.test", Port: 1080, Username: "user", Password: ""},
	}
	corrupt, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(corrupt); err == nil {
		t.Fatal("unpaired SOCKS5 credentials were accepted")
	}
}
