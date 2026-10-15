package subscriptions

import (
	"context"
	"errors"
	"testing"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
)

type countingBatchResolver struct {
	calls int
}

func (r *countingBatchResolver) ResolveSubscriptionLine(context.Context, string, string, string) (string, error) {
	return "", errors.New("single-line resolver should not be called")
}

func (r *countingBatchResolver) ResolveSubscriptionLines(_ context.Context, _ string, _ string, bindings []string) (map[string]string, map[string]error, error) {
	r.calls++
	if len(bindings) != 2 || bindings[0] != "binding-a" || bindings[1] != "binding-b" {
		return nil, nil, errors.New("unexpected batch input")
	}
	return map[string]string{"binding-a": testURI}, map[string]error{"binding-b": faults.ErrConflict}, nil
}

func TestResolveUsesOneNativeBatchSnapshot(t *testing.T) {
	resolver := &countingBatchResolver{}
	record := Record{
		OwnerID: "admin",
		Item:    Item{ForwardingRuleID: "rule", CustomerID: forwarding.AdministratorSubjectID("admin")},
		Published: []Line{
			{Name: "可用", BindingID: "binding-a"},
			{Name: "待恢复", BindingID: "binding-b"},
		},
	}
	lines, err := NewService(nil, resolver, nil).Resolve(context.Background(), record, false)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("batch resolver calls = %d, want 1", resolver.calls)
	}
	expected, _ := ParseProxy(testURI, "可用")
	if len(lines) != 2 || lines[0].URI != expected.URI || lines[0].Error != "" || lines[1].Error == "" {
		t.Fatalf("unexpected resolved lines: %#v", lines)
	}
}
