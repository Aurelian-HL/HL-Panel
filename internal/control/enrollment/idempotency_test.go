package enrollment_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/auth"
	"github.com/hongle/hl-panel/internal/control/enrollment"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func TestIssueIdempotencyReplaysSameToken(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	store := memoryrepo.New(auth.Administrator{ID: "admin", Username: "admin", PasswordHash: []byte("hash"), CreatedAt: now})
	service := enrollment.NewService(store, func() time.Time { return now }, time.Hour, enrollment.WithIdempotencySecret([]byte("stable-secret")))
	first, replayed, err := service.IssueForGroupWithNezhaIdempotent(context.Background(), "admin", "edge", "", 0, time.Minute, "issue-key")
	if err != nil || replayed {
		t.Fatalf("initial issue failed: %+v %v", first, err)
	}
	second, replayed, err := service.IssueForGroupWithNezhaIdempotent(context.Background(), "admin", "edge", "", 0, time.Minute, "issue-key")
	if err != nil || !replayed || second.ID != first.ID || second.Token != first.Token {
		t.Fatalf("issue was not replayed: %+v %v %v", second, replayed, err)
	}
	if _, _, err = service.IssueForGroupWithNezhaIdempotent(context.Background(), "admin", "other", "", 0, time.Minute, "issue-key"); !errors.Is(err, faults.ErrIdempotencyConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}
