package usage

import (
	"context"
	"errors"
	"testing"
)

type conditionalCounter struct {
	calls, commits, rollbacks int
	err                       error
}

func (c *conditionalCounter) CollectUsage(context.Context, CollectionWindow) ([]CounterDelta, error) {
	c.calls++
	return []CounterDelta{{LegacyRuleID: "rule", RuleActualBytes: 42}}, c.err
}
func (c *conditionalCounter) BeginCollection() error { return nil }
func (c *conditionalCounter) CommitCollection()      { c.commits++ }
func (c *conditionalCounter) RollbackCollection()    { c.rollbacks++ }

func TestUnusedEngineDoesNotBlockMixedTrafficButConfiguredFailureDoes(t *testing.T) {
	expected := false
	failed := &conditionalCounter{err: errors.New("engine unavailable")}
	good := &conditionalCounter{}
	source, err := NewMultiSource(ConditionalSource{Source: failed, Expected: func() bool { return expected }}, good)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.BeginCollection(); err != nil {
		t.Fatal(err)
	}
	deltas, err := source.CollectUsage(context.Background(), CollectionWindow{})
	if err != nil || len(deltas) != 1 || deltas[0].RuleActualBytes != 42 || failed.calls != 0 {
		t.Fatalf("unused engine blocked traffic: %v %+v", err, deltas)
	}
	source.CommitCollection()
	expected = true
	if err := source.BeginCollection(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CollectUsage(context.Background(), CollectionWindow{}); !errors.Is(err, failed.err) {
		t.Fatalf("configured failure suppressed: %v", err)
	}
	source.RollbackCollection()
	if failed.rollbacks != 1 || good.rollbacks != 1 || good.commits != 1 {
		t.Fatal("failed collection advanced checkpoints")
	}
}
