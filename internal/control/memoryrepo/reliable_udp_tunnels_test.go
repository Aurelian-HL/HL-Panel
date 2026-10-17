package memoryrepo

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/groupconfig"
	"github.com/hongle/hl-panel/internal/control/groups"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

func reliableUDPFixture(t *testing.T, transport forwarding.TunnelTransport) (*Store, *forwarding.Service, forwarding.Request, forwarding.Rule) {
	t.Helper()
	s, req := forwardingRepositoryFixture(t)
	s.nodes["entry-node"] = cloneNode(nodes.Node{ID: "entry-node", CredentialHash: "entry-credential", Capabilities: []string{"gost-reliable-udp-v1"}})
	s.nodesByCredential["entry-credential"] = "entry-node"
	s.membersByGroup[req.EntryGroupID]["entry-node"] = groups.Member{GroupID: req.EntryGroupID, NodeID: "entry-node", DialHost: "127.0.0.1", Weight: 1}
	n := s.nodes["exit-node"]
	n.Capabilities = []string{"gost-reliable-udp-v1"}
	s.nodes[n.ID] = cloneNode(n)
	req.EgressMode = forwarding.EgressExitGroup
	req.ExitGroupID = "exit-1"
	req.TunnelTransport = transport
	svc := forwarding.NewService(s, nil)
	rule, _, err := svc.Create(context.Background(), "admin", req, "reliable-udp")
	if err != nil {
		t.Fatal(err)
	}
	return s, svc, req, rule
}
func acknowledgeUDPConfig(t *testing.T, s *Store, nodeID string) generations.NodeConfigGeneration {
	t.Helper()
	ctx := context.Background()
	n := s.nodes[nodeID]
	c := s.nodeConfigsByNode[nodeID][n.DesiredGeneration]
	if c.Generation == 0 {
		t.Fatal("missing desired config for " + nodeID)
	}
	for _, p := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		_, err := s.RecordApplyResult(ctx, generations.ApplyResult{NodeID: nodeID, Generation: c.Generation, ConfigSHA256: c.ConfigSHA256, AttemptID: "0123456789abcdef0123456789abcdef", Phase: p, Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost", CreatedAt: time.Now()}, audit.Event{})
		if err != nil {
			t.Fatal(err)
		}
	}
	return c
}
func udpReceipt(t *testing.T, s *Store, ruleID, nodeID string) deploymentreceipts.Status {
	t.Helper()
	in, err := s.RuleNodeDeploymentInput(context.Background(), ruleID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return deploymentreceipts.Evaluate(in)
}

func assertUDPSnapshotUnchanged(t *testing.T, before, after []byte) {
	t.Helper()
	var a, b map[string]any
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
		t.Fatal("invalid snapshot comparison")
	}
	for key, value := range a {
		if !reflect.DeepEqual(value, b[key]) {
			t.Errorf("failed mutation changed snapshot field %s", key)
		}
	}
	if len(a) != len(b) {
		t.Error("failed mutation changed snapshot field count")
	}
}
func TestReliableUDPSharedNodeReceiptsDoNotLoop(t *testing.T) {
	for _, transport := range []forwarding.TunnelTransport{forwarding.TunnelQUIC, forwarding.TunnelKCP} {
		t.Run(string(transport), func(t *testing.T) {
			s, req := forwardingRepositoryFixture(t)
			n := s.nodes["exit-node"]
			n.Capabilities = []string{"gost-reliable-udp-v1"}
			s.nodes[n.ID] = n
			s.membersByGroup[req.EntryGroupID][n.ID] = groups.Member{GroupID: req.EntryGroupID, NodeID: n.ID, DialHost: "127.0.0.1", Weight: 1}
			req.EgressMode = forwarding.EgressExitGroup
			req.ExitGroupID = "exit-1"
			req.TunnelTransport = transport
			rule, _, err := forwarding.NewService(s, nil).Create(context.Background(), "admin", req, "shared-udp")
			if err != nil {
				t.Fatal(err)
			}
			generation := s.nodes[n.ID].DesiredGeneration
			for i := 0; i < 3; i++ {
				acknowledgeUDPConfig(t, s, n.ID)
				if s.nodes[n.ID].DesiredGeneration != generation {
					t.Fatal("shared entry/exit generated a receipt loop")
				}
			}
			if !udpReceipt(t, s, rule.ID, n.ID).ReceiptVerified {
				t.Fatal("shared node failed to verify")
			}
		})
	}
}

func TestReliableUDPExitFirstThenEntryAndLifecycle(t *testing.T) {
	for _, transport := range []forwarding.TunnelTransport{forwarding.TunnelQUIC, forwarding.TunnelKCP} {
		t.Run(string(transport), func(t *testing.T) {
			s, svc, req, r := reliableUDPFixture(t, transport)
			ctx := context.Background()
			if s.nodes["entry-node"].DesiredGeneration != 0 {
				t.Fatal("entry published before exit verify")
			}
			if !validReliableUDPPort(s.reliableUDPTunnels[r.ID].Port) {
				t.Fatal("missing automatic internal UDP port")
			}
			if udpReceipt(t, s, r.ID, "exit-node").ReceiptVerified {
				t.Fatal("unapplied exit ready")
			}
			exitConfig := acknowledgeUDPConfig(t, s, "exit-node")
			if !udpReceipt(t, s, r.ID, "exit-node").ReceiptVerified || s.nodes["entry-node"].DesiredGeneration == 0 {
				t.Fatal("exit receipt did not unlock entry")
			}
			acknowledgeUDPConfig(t, s, "entry-node")
			v, err := svc.Get(ctx, r.ID)
			if err != nil || !v.Deployed || v.Status != forwarding.StatusActive {
				t.Fatalf("both verified not active: status=%s reason=%s err=%v", v.Status, v.ActivationReason, err)
			}
			generation := s.nodes["entry-node"].DesiredGeneration
			replay := s.applyResultsByNode["exit-node"][exitConfig.Generation]["verify"]
			if _, err = s.RecordApplyResult(ctx, replay, audit.Event{}); err != nil {
				t.Fatal(err)
			}
			if generation != s.nodes["entry-node"].DesiredGeneration {
				t.Fatal("receipt replay reissued entry")
			}
			raw, err := s.EncodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DecodeSnapshot(raw)
			if err != nil {
				t.Fatal(err)
			}
			if restored.reliableUDPTunnels[r.ID] != s.reliableUDPTunnels[r.ID] {
				t.Fatal("private material changed on restore")
			}
			public, _ := json.Marshal(v)
			events, _ := json.Marshal(s.auditEvents)
			m := s.reliableUDPTunnels[r.ID]
			for _, secret := range []string{m.Password, m.KeyPEM, m.KCPKey} {
				if secret != "" && (strings.Contains(string(public), secret) || strings.Contains(string(events), secret)) {
					t.Fatal("secret leaked")
				}
			}
			req.Revision = v.Revision
			req.Paused = true
			paused, _, err := svc.Update(ctx, "admin", r.ID, req, "pause-udp")
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"entry-node", "exit-node"} {
				c := s.nodeConfigsByNode[id][s.nodes[id].DesiredGeneration]
				var b agentv1.ConfigurationBundle
				if json.Unmarshal(c.Config, &b) != nil || len(b.Fragments) != 0 {
					t.Fatal("pause left a tunnel listener")
				}
			}
			req.Paused = false
			req.Revision = paused.Revision
			if _, _, err = svc.Update(ctx, "admin", r.ID, req, "resume-udp"); err != nil {
				t.Fatal(err)
			}
			acknowledgeUDPConfig(t, s, "exit-node")
			acknowledgeUDPConfig(t, s, "entry-node")
			if !udpReceipt(t, s, r.ID, "entry-node").ReceiptVerified {
				t.Fatal("resume did not recover")
			}
		})
	}
}
func TestReliableUDPCapabilityHeartbeatReconcilesBothEnds(t *testing.T) {
	s, _, _, r := reliableUDPFixture(t, forwarding.TunnelKCP)
	acknowledgeUDPConfig(t, s, "exit-node")
	acknowledgeUDPConfig(t, s, "entry-node")
	_, err := s.UpdateHeartbeat(context.Background(), "exit-node", nodes.Heartbeat{}, time.Now(), audit.Event{})
	if err != nil {
		t.Fatal(err)
	}
	if udpReceipt(t, s, r.ID, "entry-node").FragmentEmitted {
		t.Fatal("entry retained unsupported exit")
	}
	_, err = s.UpdateHeartbeat(context.Background(), "exit-node", nodes.Heartbeat{Capabilities: []string{"gost-reliable-udp-v1"}}, time.Now(), audit.Event{})
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeUDPConfig(t, s, "exit-node")
	if !udpReceipt(t, s, r.ID, "entry-node").FragmentEmitted {
		t.Fatal("upgraded exit did not republish entry")
	}
}

func TestReliableUDPExitRollbackWithdrawsEntryAndCanRecover(t *testing.T) {
	s, _, _, r := reliableUDPFixture(t, forwarding.TunnelQUIC)
	c := acknowledgeUDPConfig(t, s, "exit-node")
	acknowledgeUDPConfig(t, s, "entry-node")
	_, err := s.RecordApplyResult(context.Background(), generations.ApplyResult{NodeID: c.NodeID, Generation: c.Generation, ConfigSHA256: c.ConfigSHA256, AttemptID: "0123456789abcdef0123456789abcdef", Phase: agentv1.ApplyPhaseRollback, Status: agentv1.ApplyStatusRolledBack, EngineMode: "gost", CreatedAt: time.Now()}, audit.Event{})
	if err != nil {
		t.Fatal(err)
	}
	if udpReceipt(t, s, r.ID, "entry-node").FragmentEmitted {
		t.Fatal("rolled back exit left an ingress route")
	}
	for _, p := range []agentv1.ApplyPhase{agentv1.ApplyPhaseCommit, agentv1.ApplyPhaseVerify} {
		_, err = s.RecordApplyResult(context.Background(), generations.ApplyResult{NodeID: c.NodeID, Generation: c.Generation, ConfigSHA256: c.ConfigSHA256, AttemptID: applyAttemptB, Phase: p, Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost", CreatedAt: time.Now()}, audit.Event{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !udpReceipt(t, s, r.ID, "entry-node").FragmentEmitted {
		t.Fatal("reverified exit did not restore ingress")
	}
}

func TestReliableUDPReceiptCompileFailureIsAtomicAndRetryable(t *testing.T) {
	s, _, req, _ := reliableUDPFixture(t, forwarding.TunnelKCP)
	n := s.nodes["exit-node"]
	c := s.nodeConfigsByNode[n.ID][n.DesiredGeneration]
	result := generations.ApplyResult{NodeID: n.ID, Generation: c.Generation, ConfigSHA256: c.ConfigSHA256, AttemptID: applyAttemptA, Phase: agentv1.ApplyPhaseCommit, Status: agentv1.ApplyStatusSucceeded, EngineMode: "gost", CreatedAt: time.Now()}
	if _, err := s.RecordApplyResult(context.Background(), result, audit.Event{}); err != nil {
		t.Fatal(err)
	}
	before, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	previousRevisions, hadRevisions := s.revisionsByGroup[req.EntryGroupID]
	s.revisionsByGroup[req.EntryGroupID] = []generations.GroupRevision{{GroupID: req.EntryGroupID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
	result.Phase = agentv1.ApplyPhaseVerify
	if _, err = s.RecordApplyResult(context.Background(), result, audit.Event{}); err == nil {
		t.Fatal("invalid dependent bundle accepted")
	}
	if hadRevisions {
		s.revisionsByGroup[req.EntryGroupID] = previousRevisions
	} else {
		delete(s.revisionsByGroup, req.EntryGroupID)
	}
	after, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	assertUDPSnapshotUnchanged(t, before, after)
	if _, err = s.RecordApplyResult(context.Background(), result, audit.Event{}); err != nil {
		t.Fatal("retry failed", err)
	}
	if s.nodes["entry-node"].DesiredGeneration == 0 {
		t.Fatal("retry did not publish ingress")
	}
}

func TestReliableUDPDeleteExitRollsBackCompileFailure(t *testing.T) {
	s, _, req, r := reliableUDPFixture(t, forwarding.TunnelKCP)
	acknowledgeUDPConfig(t, s, "exit-node")
	acknowledgeUDPConfig(t, s, "entry-node")
	before, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	previousRevisions, hadRevisions := s.revisionsByGroup[req.EntryGroupID]
	s.revisionsByGroup[req.EntryGroupID] = []generations.GroupRevision{{GroupID: req.EntryGroupID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
	input := nodes.DeleteInput{NodeID: "exit-node", DeletedBy: "admin", IdempotencyKey: "delete-udp-exit", RequestSHA256: "delete-hash", DeletedAt: time.Now(), OnlineFor: time.Minute}
	if _, _, err = s.DeleteOffline(context.Background(), input, audit.Event{}); err == nil {
		t.Fatal("invalid compile accepted")
	}
	if hadRevisions {
		s.revisionsByGroup[req.EntryGroupID] = previousRevisions
	} else {
		delete(s.revisionsByGroup, req.EntryGroupID)
	}
	after, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	assertUDPSnapshotUnchanged(t, before, after)
	if _, replay, err := s.DeleteOffline(context.Background(), input, audit.Event{}); err != nil || replay {
		t.Fatalf("retry: replay=%v err=%v", replay, err)
	}
	if s.nodes["exit-node"].DeletedAt == nil || udpReceipt(t, s, r.ID, "entry-node").FragmentEmitted {
		t.Fatal("deleted exit left an ingress route")
	}
}

func TestReliableUDPNetworkMultiplierFailureRollsBack(t *testing.T) {
	s, _, req, _ := reliableUDPFixture(t, forwarding.TunnelQUIC)
	acknowledgeUDPConfig(t, s, "exit-node")
	acknowledgeUDPConfig(t, s, "entry-node")
	before, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	previousRevisions, hadRevisions := s.revisionsByGroup[req.EntryGroupID]
	s.revisionsByGroup[req.EntryGroupID] = []generations.GroupRevision{{GroupID: req.EntryGroupID, Revision: 1, Engine: agentv1.EngineGOST, Config: json.RawMessage(`{broken`)}}
	input := groupconfig.Request{ConnectHost: "entry.example.test", PortStart: 12000, PortEnd: 12009, AllowDirect: true, AllowedExitGroupIDs: []string{"exit-1"}, TrafficMultiplier: 2, Revision: 1}
	svc := groupconfig.NewService(s, nil)
	if _, _, err = svc.Update(context.Background(), "admin", req.EntryGroupID, input, "udp-multiplier"); err == nil {
		t.Fatal("invalid compile accepted")
	}
	if hadRevisions {
		s.revisionsByGroup[req.EntryGroupID] = previousRevisions
	} else {
		delete(s.revisionsByGroup, req.EntryGroupID)
	}
	after, err := s.EncodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	assertUDPSnapshotUnchanged(t, before, after)
	if _, replay, err := svc.Update(context.Background(), "admin", req.EntryGroupID, input, "udp-multiplier"); err != nil || replay {
		t.Fatalf("retry: replay=%v err=%v", replay, err)
	}
}

func TestReliableUDPSharedListenerPortReservation(t *testing.T) {
	s, svc, req, first := reliableUDPFixture(t, forwarding.TunnelKCP)
	member := s.membersByGroup[req.EntryGroupID]["entry-node"]
	member.GroupID = req.ExitGroupID
	s.membersByGroup[req.ExitGroupID][member.NodeID] = member
	port := s.reliableUDPTunnels[first.ID].Port
	network := s.groupNetworks[req.EntryGroupID]
	network.PortEnd = port
	s.groupNetworks[req.EntryGroupID] = network
	udp := req
	udp.Name = "conflicting-customer-udp"
	udp.IngressProtocol = forwarding.IngressUDP
	udp.Protocol = forwarding.ProtocolUDP
	udp.EgressMode, udp.ExitGroupID, udp.TunnelTransport = forwarding.EgressDirect, "", ""
	udp.ListenPort = port
	if _, _, err := svc.Create(context.Background(), "admin", udp, "conflicting-udp"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("customer UDP collision was not rejected: %v", err)
	}
	req.Name = "second"
	second, _, err := svc.Create(context.Background(), "admin", req, "second-udp")
	if err != nil {
		t.Fatal(err)
	}
	if s.reliableUDPTunnels[second.ID].Port == port {
		t.Fatal("tunnels share a UDP listener")
	}
}
