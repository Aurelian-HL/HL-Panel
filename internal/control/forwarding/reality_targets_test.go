package forwarding

import (
	"context"
	"testing"
)

func TestCreationRealityTargetsStayPairedAndKeepReplayDigest(t *testing.T) {
	pool := DefaultRealityTargets()
	want := map[string]string{}
	for _, candidate := range pool {
		normalized, err := NormalizeRealityDefaults(candidate)
		if err != nil || normalized != candidate || candidate.Destination != candidate.ServerName+":443" {
			t.Fatalf("invalid paired candidate: %+v %v", candidate, err)
		}
		want[candidate.ServerName] = candidate.Destination
	}
	if _, ok := want["www.apple.com"]; !ok {
		t.Fatal("Apple candidate missing")
	}
	repo := &captureRepository{}
	service := NewService(repo, nil, WithRealityTargets(pool))
	pool[0].ServerName = "mutated.invalid"
	view := service.RealityTargets()
	view[0].ServerName = "mutated.invalid"
	request := validRequest()
	request.IngressProtocol = IngressVLESSReality
	request.VLESSOutboundMode = VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host, request.VLESSSOCKS5Port = "landing.example.com", 1080
	request.VLESSSOCKS5Username, request.VLESSSOCKS5Password = "user", "secret"
	var hash string
	for i := 0; i < 24; i++ {
		rule, _, err := service.Create(context.Background(), "admin", request, "replay-create")
		if err != nil {
			t.Fatal(err)
		}
		if want[rule.RealityServerName] != rule.RealityDestination || rule.RealityDestination == "" || rule.RealityPublicKey == "" || repo.created.RealityPrivateKey == "" {
			t.Fatal("creation returned an unpaired target or incomplete key material")
		}
		if i > 0 && hash != repo.created.RequestSHA256 {
			t.Fatal("random target changed the idempotency digest")
		}
		hash = repo.created.RequestSHA256
	}
	request.RealityServerName, request.RealityDestination = "custom.example.com", "custom.example.com:8443"
	rule, _, err := service.Create(context.Background(), "admin", request, "custom-create")
	if err != nil || rule.RealityServerName != request.RealityServerName || rule.RealityDestination != request.RealityDestination {
		t.Fatalf("custom target changed: %v", err)
	}
	request.Revision = 1
	_, _, err = service.Update(context.Background(), "admin", "rule-1", request, "custom-update")
	if err != nil || repo.updated.Rule.RealityDestination != request.RealityDestination {
		t.Fatalf("edit target changed: %v", err)
	}
	request.RealityServerName, request.RealityDestination = "", ""
	_, _, err = service.Update(context.Background(), "admin", "rule-1", request, "empty-update")
	if err != nil || !repo.updated.PreserveRealityTarget || repo.updated.Rule.RealityDestination != "" {
		t.Fatalf("edit must preserve previously issued target: %v", err)
	}
}
