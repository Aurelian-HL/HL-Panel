package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCheckerReportsIndependentEngineAndEgressObservations(t *testing.T) {
	t.Parallel()
	checker := NewChecker(StaticEngineProbe{}, StaticEgressProbe{Err: errors.New("egress timeout")})
	checker.now = func() time.Time { return time.Unix(100, 0) }
	snapshot := checker.Check(context.Background())
	if snapshot.Engine.Status != StatusHealthy {
		t.Fatalf("engine status = %q, want healthy", snapshot.Engine.Status)
	}
	if snapshot.Egress.Status != StatusFailed || snapshot.Egress.Message != "egress timeout" {
		t.Fatalf("egress observation = %#v", snapshot.Egress)
	}
	if !snapshot.CheckedAt.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("checked_at = %v", snapshot.CheckedAt)
	}
}

func TestCheckerDoesNotExposeControlPlaneSecrets(t *testing.T) {
	t.Parallel()
	checker := NewChecker(nil, StaticEgressProbe{Err: errors.New("Bearer secret-token\ninternal")})
	snapshot := checker.Check(context.Background())
	if snapshot.Engine.Status != StatusUnknown || snapshot.Egress.Status != StatusFailed {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Egress.Message != "Bearer [redacted] internal" {
		t.Fatalf("sanitized message = %q", snapshot.Egress.Message)
	}
}
