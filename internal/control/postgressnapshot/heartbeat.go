package postgressnapshot

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/hongle/hl-panel/internal/control/nodes"
)

// heartbeatStableView deliberately excludes resources: host and process
// counters are high-frequency telemetry, while lifecycle and configuration
// fields must be durable immediately.
type heartbeatStableView struct {
	Hostname                 string
	Platform                 string
	Architecture             string
	BootID                   string
	AgentVersion             string
	EngineVersions           map[string]string
	Capabilities             []string
	CurrentAppliedGeneration int64
	LastApplyPhase           string
	LastApplyStatus          string
	LastApplyMessage         string
}

func heartbeatFingerprint(value nodes.Heartbeat) [32]byte {
	stable := heartbeatStableView{
		Hostname: value.Hostname, Platform: value.Platform,
		Architecture: value.Architecture, BootID: value.BootID,
		AgentVersion: value.AgentVersion, EngineVersions: value.EngineVersions,
		Capabilities:             value.Capabilities,
		CurrentAppliedGeneration: value.CurrentAppliedGeneration,
		LastApplyPhase:           value.LastApplyPhase,
		LastApplyStatus:          value.LastApplyStatus,
		LastApplyMessage:         value.LastApplyMessage,
	}
	raw, _ := json.Marshal(stable)
	return sha256.Sum256(raw)
}

func (s *Store) shouldPersistHeartbeat(nodeID string, value nodes.Heartbeat, now time.Time) bool {
	fingerprint := heartbeatFingerprint(value)
	s.heartbeatMu.Lock()
	defer s.heartbeatMu.Unlock()
	previous, ok := s.heartbeats[nodeID]
	// The agent timestamp is wall-clock time. NTP corrections can move it
	// backwards, which would otherwise make the interval negative and suppress
	// persistence until the clock catches up with the old timestamp. Persist
	// once immediately after a rollback so the control plane cannot retain a
	// stale LastHeartbeatAt/Resources projection.
	return !ok || previous.fingerprint != fingerprint || now.Before(previous.persisted) || now.Sub(previous.persisted) >= heartbeatSnapshotInterval
}

// claimHeartbeatPersistence is a single-flight gate for the expensive full
// snapshot write. A burst of concurrent heartbeats must not all serialize on
// PostgreSQL row locks and amplify latency for subscriptions and config polls.
func (s *Store) claimHeartbeatPersistence(nodeID string, value nodes.Heartbeat, now time.Time) bool {
	return s.claimHeartbeatWrite(nodeID, value, now)
}

// Compiler reconciliation must be durable even inside the telemetry interval.
// It shares the single-flight gate so simultaneous polls cannot enqueue a full
// snapshot write for each request.
func (s *Store) claimHeartbeatCompilerReconcile(nodeID string, value nodes.Heartbeat, now time.Time) bool {
	fingerprint := heartbeatFingerprint(value)
	s.heartbeatMu.Lock()
	defer s.heartbeatMu.Unlock()
	if s.heartbeats == nil {
		s.heartbeats = make(map[string]heartbeatPersistence)
	}
	previous := s.heartbeats[nodeID]
	if previous.inFlight {
		return false
	}
	// A failed compiler reconcile must not turn every agent heartbeat into a
	// full snapshot decode and write. The first observation is immediate; after
	// that, retry at the normal snapshot interval. A wall-clock rollback is
	// treated as an immediate retry so a stale node cannot remain unreconciled.
	if !previous.compilerAttempted.IsZero() && !now.Before(previous.compilerAttempted) &&
		now.Sub(previous.compilerAttempted) < heartbeatSnapshotInterval {
		return false
	}
	previous.fingerprint = fingerprint
	previous.compilerAttempted = now
	previous.inFlight = true
	s.heartbeats[nodeID] = previous
	return true
}

func (s *Store) claimHeartbeatWrite(nodeID string, value nodes.Heartbeat, now time.Time) bool {
	fingerprint := heartbeatFingerprint(value)
	s.heartbeatMu.Lock()
	defer s.heartbeatMu.Unlock()
	if s.heartbeats == nil {
		s.heartbeats = make(map[string]heartbeatPersistence)
	}
	previous := s.heartbeats[nodeID]
	if previous.inFlight {
		return false
	}
	if previous.fingerprint == fingerprint && !now.Before(previous.persisted) && now.Sub(previous.persisted) < heartbeatSnapshotInterval {
		return false
	}
	previous.inFlight = true
	s.heartbeats[nodeID] = previous
	return true
}

func (s *Store) releaseHeartbeatPersistence(nodeID string) {
	s.heartbeatMu.Lock()
	defer s.heartbeatMu.Unlock()
	if previous, ok := s.heartbeats[nodeID]; ok {
		previous.inFlight = false
		s.heartbeats[nodeID] = previous
	}
}

func (s *Store) markHeartbeatPersisted(nodeID string, value nodes.Heartbeat, now time.Time) {
	s.heartbeatMu.Lock()
	defer s.heartbeatMu.Unlock()
	if s.heartbeats == nil {
		s.heartbeats = make(map[string]heartbeatPersistence)
	}
	s.heartbeats[nodeID] = heartbeatPersistence{
		fingerprint: heartbeatFingerprint(value), persisted: now, inFlight: false,
	}
}
