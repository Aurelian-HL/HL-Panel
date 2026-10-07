package panelruntime

import (
	"context"
	"testing"
	"time"
)

func TestControlWithoutControllerIsExplicitlyFailed(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	service := NewService("v1.2.3", nil, nil, func() time.Time { return now })
	result := service.Control(context.Background(), "restart")
	if result.Status != "failed" || result.Message == "" {
		t.Fatalf("expected explicit failure, got %+v", result)
	}
	if current, ok := service.LastControl(context.Background()); !ok || current.CommandID != result.CommandID {
		t.Fatalf("last result not retained: %+v %v", current, ok)
	}
}
