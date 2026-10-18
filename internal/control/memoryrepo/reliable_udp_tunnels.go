package memoryrepo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/deploymentreceipts"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/control/generations"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
)

// Tunnel ports are internal UDP listeners, independent of customer ENTRY ranges.
// EXIT networks intentionally have no customer-facing listen range.
const reliableUDPPortStart = 20000
const reliableUDPPortEnd = 59999

func validReliableUDPPort(port int) bool {
	return port >= reliableUDPPortStart && port <= reliableUDPPortEnd
}

func nodeSupportsReliableUDP(node nodes.Node) bool {
	if node.DeletedAt != nil {
		return false
	}
	for _, c := range node.Capabilities {
		if c == "gost-reliable-udp-v1" {
			return true
		}
	}
	return false
}

func (s *Store) tunnelPortAvailableLocked(ruleID, groupID string, port int) bool {
	for id, m := range s.reliableUDPTunnels {
		if id != ruleID && m.Port == port && s.entryGroupsShareListenerLocked(m.ExitGroupID, groupID) {
			return false
		}
	}
	for _, r := range s.forwardRules {
		if r.EffectiveIngressProtocol() == forwarding.IngressUDP && r.ListenPort == port && s.entryGroupsShareListenerLocked(r.EntryGroupID, groupID) {
			return false
		}
	}
	return true
}

func (s *Store) prepareReliableUDPTunnelLocked(rule forwarding.Rule) (gostconfig.TunnelMaterial, error) {
	if !rule.UsesReliableUDPTunnel() {
		return gostconfig.TunnelMaterial{}, nil
	}
	if old, ok := s.reliableUDPTunnels[rule.ID]; ok && old.ExitGroupID == rule.ExitGroupID && old.Transport == rule.TunnelTransport && validReliableUDPPort(old.Port) && s.tunnelPortAvailableLocked(rule.ID, rule.ExitGroupID, old.Port) {
		return old, nil
	}
	port := 0
	for p := reliableUDPPortStart; p <= reliableUDPPortEnd; p++ {
		if s.tunnelPortAvailableLocked(rule.ID, rule.ExitGroupID, p) {
			port = p
			break
		}
	}
	if port == 0 {
		return gostconfig.TunnelMaterial{}, fmt.Errorf("%w: exit group UDP ports are exhausted", faults.ErrConflict)
	}
	secret := func() (string, error) {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		return base64.RawURLEncoding.EncodeToString(b), err
	}
	password, err := secret()
	if err != nil {
		return gostconfig.TunnelMaterial{}, err
	}
	key, err := secret()
	if err != nil {
		return gostconfig.TunnelMaterial{}, err
	}
	m := gostconfig.TunnelMaterial{ExitGroupID: rule.ExitGroupID, Transport: rule.TunnelTransport, Port: port, Username: "hl-" + rule.ID, Password: password}
	if rule.TunnelTransport == forwarding.TunnelKCP {
		m.KCPKey = key
		return m, nil
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return m, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return m, err
	}
	m.ServerName = "hl-tunnel.internal"
	now := time.Now().UTC()
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: m.ServerName}, DNSNames: []string{m.ServerName}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &private.PublicKey, private)
	if err != nil {
		return m, err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return m, err
	}
	m.CertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	m.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}))
	return m, nil
}

func (s *Store) compileReliableUDPFragmentsLocked(nodeID string) ([]agentv1.ConfigurationFragment, error) {
	fragments := []agentv1.ConfigurationFragment{}
	if !nodeSupportsReliableUDP(s.nodes[nodeID]) {
		return fragments, nil
	}
	ids := make([]string, 0)
	for id, r := range s.forwardRules {
		if r.UsesReliableUDPTunnel() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := s.forwardingBaseViewLocked(s.forwardRules[id])
		if r.Paused || !forwardingRuleCompilableStatus(r.Status) || r.PendingActivationReason() != "" {
			continue
		}
		entryMember, e := s.membersByGroup[r.EntryGroupID][nodeID]
		exitMember, x := s.membersByGroup[r.ExitGroupID][nodeID]
		entry, exit := e && entryMember.RetiredAt == nil, x && exitMember.RetiredAt == nil
		if !entry && !exit {
			continue
		}
		m, ok := s.reliableUDPTunnels[id]
		if !ok {
			return nil, fmt.Errorf("missing reliable UDP runtime material")
		}
		hosts := []string{}
		exitIDs := []string{}
		for n := range s.membersByGroup[r.ExitGroupID] {
			exitIDs = append(exitIDs, n)
		}
		sort.Strings(exitIDs)
		for _, n := range exitIDs {
			member := s.membersByGroup[r.ExitGroupID][n]
			if member.RetiredAt != nil || strings.TrimSpace(member.DialHost) == "" || !nodeSupportsReliableUDP(s.nodes[n]) {
				continue
			}
			// Wait for a verified exit before publishing a new ingress listener.
			ready := n == nodeID && exit
			if !ready {
				input, err := s.ruleNodeDeploymentInputBorrowedLocked(id, n)
				input.RequireTunnelEntry = false // Exit readiness must prove the relay role independently.
				ready = err == nil && deploymentreceipts.Evaluate(input).ReceiptVerified
			}
			if ready {
				hosts = append(hosts, member.DialHost)
			}
		}
		entry = entry && len(hosts) > 0
		if !entry && !exit {
			continue
		}
		raw, err := gostconfig.CompileTunnel(r, m, hosts, entry, exit)
		if err != nil {
			return nil, err
		}
		fragments = append(fragments, agentv1.ConfigurationFragment{GroupID: generations.ForwardingFragmentID(agentv1.EngineGOST, r.ID), GroupRevision: agentv1.GroupRevision(r.Revision), Engine: agentv1.EngineGOST, Config: raw})
	}
	return fragments, nil
}

func (s *Store) reliableUDPDependentGroupsLocked(groupIDs []string) []string {
	out := append([]string(nil), groupIDs...)
	for _, r := range s.forwardRules {
		if !r.UsesReliableUDPTunnel() {
			continue
		}
		for _, g := range groupIDs {
			if g == r.ExitGroupID || g == r.EntryGroupID {
				out = append(out, r.EntryGroupID, r.ExitGroupID)
				break
			}
		}
	}
	return out
}

func (s *Store) validateReliableUDPSnapshotLocked() error {
	for id, m := range s.reliableUDPTunnels {
		r, ok := s.forwardRules[id]
		if !ok || !r.UsesReliableUDPTunnel() || m.ExitGroupID != r.ExitGroupID || m.Transport != r.TunnelTransport || !validReliableUDPPort(m.Port) || !s.tunnelPortAvailableLocked(id, m.ExitGroupID, m.Port) {
			return fmt.Errorf("invalid persisted reliable UDP binding")
		}
		check := r
		check.Paused = false
		check.Status = forwarding.StatusPendingActivation
		check.AcceptProxyProtocol = false
		check.SendProxyProtocol = 0
		check.SpeedLimitMbps = 0
		check.IPLimit = 0
		check.ConnectionLimit = 0
		if check.SelectionPolicy == forwarding.SelectionLeastLoad {
			check.SelectionPolicy = forwarding.SelectionRoundRobin
		}
		if _, err := gostconfig.CompileTunnel(check, m, nil, false, true); err != nil {
			return fmt.Errorf("invalid persisted reliable UDP material")
		}
	}
	for id, r := range s.forwardRules {
		if r.UsesReliableUDPTunnel() {
			if _, ok := s.reliableUDPTunnels[id]; !ok {
				return fmt.Errorf("missing persisted reliable UDP material")
			}
		}
	}
	return nil
}

// Repeated receipt delivery also retries this projection if an earlier compile
// failed. Bundle hashes suppress duplicate generations and engine restarts.
func (s *Store) recompileReliableUDPAfterReceiptLocked(nodeID string, now time.Time) error {
	groups := []string{}
	for _, r := range s.forwardRules {
		if !r.UsesReliableUDPTunnel() {
			continue
		}
		member, ok := s.membersByGroup[r.ExitGroupID][nodeID]
		if !ok || member.RetiredAt != nil {
			continue
		}
		// A failed or rolled-back exit must also withdraw its ingress route.
		// Restricting this to successful receipts leaves stale exits selectable.
		groups = append(groups, r.EntryGroupID)
	}
	return s.recompileForwardingGroupsLocked(groups, now)
}
