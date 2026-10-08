package memoryrepo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

func TestFirstExecutableVLESSGenerationContainsIndependentProbeIdentity(t *testing.T) {
	f := newVLESSBundleFixture(t, true)
	s := f.store
	probe, err := s.ProtocolProbeConfig(context.Background(), f.rule.ID)
	if err != nil || probe.UUID == "" || probe.UUID == f.credential {
		t.Fatal("rule creation did not persist an independent probe identity")
	}
	wake := s.DesiredConfigChanges(f.nodeID)
	configuration, changed, err := s.compileNodeConfigLocked(f.nodeID, time.Now())
	if err != nil || !changed {
		t.Fatalf("first executable generation: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(configuration.Config), probe.UUID) || !strings.Contains(string(configuration.Config), f.credential) {
		t.Fatal("first executable generation omitted customer or probe identity")
	}
	select {
	case <-wake:
	default:
		t.Fatal("new configuration did not wake node")
	}
	wake = s.DesiredConfigChanges(f.nodeID)
	if err := s.ConfigureProtocolProbe(context.Background(), f.rule.ID, probe); !errors.Is(err, faults.ErrConflict) {
		t.Fatal(err)
	}
	if s.nodes[f.nodeID].DesiredGeneration != configuration.Generation {
		t.Fatal("existing probe forced a second deployment")
	}
	select {
	case <-wake:
		t.Fatal("unchanged generation woke node")
	default:
	}
	public, _ := json.Marshal(s.forwardingViewLocked(s.forwardRules[f.rule.ID]))
	audits, _ := json.Marshal(s.AuditEvents())
	if strings.Contains(string(public), probe.UUID) || strings.Contains(string(audits), probe.UUID) {
		t.Fatal("probe secret leaked")
	}
}

func TestConfiguredProbeEchoPortAndCreationFailure(t *testing.T) {
	s, request := forwardingRepositoryFixture(t)
	request.IngressProtocol = forwarding.IngressVLESSReality
	request.VLESSOutboundMode = forwarding.VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.test"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "test-user"
	request.VLESSSOCKS5Password = "test-secret"
	request.ListenPort = 12001
	defaults := forwarding.WithRealityDefaults(forwarding.RealityDefaults{ServerName: "example.com", Destination: "example.com:443"})
	service := forwarding.NewService(s, time.Now, defaults, forwarding.WithProtocolProbeEchoPort(39001))
	rule, _, err := service.CreateForAdministrator(context.Background(), "admin-test", request, "probe-custom-port")
	if err != nil {
		t.Fatal(err)
	}
	if s.protocolProbes[rule.ID].EchoPort != 39001 {
		t.Fatal("custom echo port was ignored")
	}
	beforeRules, beforeProbes := len(s.forwardRules), len(s.protocolProbes)
	request.ListenPort = 12002
	bad := forwarding.NewService(s, time.Now, defaults, forwarding.WithProtocolProbeEchoPort(70000))
	if _, _, err := bad.CreateForAdministrator(context.Background(), "admin-test", request, "probe-bad-port"); err == nil {
		t.Fatal("invalid probe port accepted")
	}
	if len(s.forwardRules) != beforeRules || len(s.protocolProbes) != beforeProbes {
		t.Fatal("failed creation persisted partial state")
	}
}
