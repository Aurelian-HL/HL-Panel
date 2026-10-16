package memoryrepo

import (
	"context"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

func TestRuleConnectionsRequireFreshVerifiedRuleListener(t *testing.T) {
	for _, test := range []struct {
		name, status, reason string
		modify               func(*Store)
	}{
		{"online", "online", "established", func(*Store) {}},
		{"idle", "offline", "no_connections", func(s *Store) {
			s.nodes["node"].Resources["host"].(map[string]any)["tcp_listeners"] = map[string]any{"443": float64(0)}
		}},
		{"missing listener", "error", "listener_or_apply_failed", func(s *Store) { s.nodes["node"].Resources["host"].(map[string]any)["tcp_listeners"] = map[string]any{} }},
		{"old agent", "offline", "not_sampled", func(s *Store) { delete(s.nodes["node"].Resources["host"].(map[string]any), "tcp_listeners") }},
		{"failed collection", "offline", "not_sampled", func(s *Store) { s.nodes["node"].Resources["host"].(map[string]any)["tcp_listeners"] = nil }},
		{"stale", "offline", "not_sampled", func(s *Store) {
			n := s.nodes["node"]
			at := time.Now().Add(-time.Minute)
			n.LastHeartbeatAt = &at
			s.nodes["node"] = n
		}},
		{"paused", "offline", "inactive", func(s *Store) { r := s.forwardRules["rule"]; r.Paused = true; s.forwardRules["rule"] = r }},
		{"unverified", "offline", "not_sampled", func(s *Store) {
			a := s.applyAttemptsByNode["node"][1]
			a.Invalidated = true
			s.applyAttemptsByNode["node"][1] = a
		}},
		{"deleted node", "offline", "not_sampled", func(s *Store) { n := s.nodes["node"]; at := time.Now(); n.DeletedAt = &at; s.nodes["node"] = n }},
		{"bad sample", "offline", "not_sampled", func(s *Store) {
			s.nodes["node"].Resources["host"].(map[string]any)["tcp_listeners"] = map[string]any{"443": -1.0}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, proof := protocolPublicationFixture(t)
			if err := s.PublishProtocolHealth(context.Background(), proof); err != nil {
				t.Fatal(err)
			}
			r := s.forwardRules["rule"]
			// Retain the verified fixture's actual port instead of assuming its value.
			port := r.ListenPort
			if port != 443 {
				t.Fatalf("fixture changed listener: %d", port)
			}
			n := s.nodes["node"]
			now := time.Now().UTC()
			n.LastHeartbeatAt = &now
			n.Resources = map[string]any{"host": map[string]any{"tcp_listeners": map[string]any{"443": float64(7)}}}
			s.nodes["node"] = n
			test.modify(s)
			got, err := s.ForwardingRule(context.Background(), "rule")
			if err != nil || got.Connection == nil || got.Connection.Status != test.status || got.Connection.Reason != test.reason {
				t.Fatalf("connection: %+v %v", got.Connection, err)
			}
			if test.status == "online" && got.Connection.Connections != 7 {
				t.Fatal("wrong per-port count")
			}
			if s.forwardRules["rule"].Connection != nil {
				t.Fatal("live state persisted in rule")
			}
			// Heartbeat overlays refresh a cached view without rebuilding a snapshot.
			view := s.ForwardingReadView().WithOverlays(2, nil, []gatewaymembership.ProtocolObservation{proof}, nil)
			again, err := view.ForwardingRule(context.Background(), "rule")
			if err != nil || again.Connection.Status != got.Connection.Status {
				t.Fatal("read view lost live state", err)
			}
		})
	}
}
