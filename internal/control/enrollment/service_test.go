package enrollment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
)

func TestNezhaSelectionFailsClosedWithoutVerifier(t *testing.T) {
	service := NewService(nil, time.Now, time.Hour)
	_, err := service.IssueForGroupWithNezha(context.Background(), "admin", "edge", "group", 1, time.Minute)
	if !errors.Is(err, faults.ErrValidation) {
		t.Fatalf("selection without a monitoring service was accepted: %v", err)
	}
	denied := errors.New("upstream unavailable")
	service = NewService(nil, time.Now, time.Hour, WithNezhaServerValidator(func(context.Context, uint64) error { return denied }))
	_, err = service.IssueForGroupWithNezha(context.Background(), "admin", "edge", "group", 1, time.Minute)
	if !errors.Is(err, denied) {
		t.Fatalf("monitoring validation failure was ignored: %v", err)
	}
}
