package usage

import (
	"context"
	"errors"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReporterJournalsOldGenerationWhileDeliveryIsUnavailable(t *testing.T) {
	store := NewJournalStore(filepath.Join(t.TempDir(), "journal.json"))
	now := time.Now().UTC()
	generation := agentv1.NodeConfigGeneration(4)
	source := &sourceStub{deltas: []CounterDelta{validDelta()}}
	sender := &senderStub{failCount: 1}
	r, err := NewReporter(store, source, sender, "node-one", "credential", 10, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	r.SetGenerationProvider(func() (agentv1.NodeConfigGeneration, error) { return generation, nil })
	now = now.Add(time.Second)
	if err := r.FlushOnce(context.Background()); err == nil {
		t.Fatal("expected offline sender")
	}
	first := sender.reports[0]
	now = now.Add(time.Second)
	if err := r.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	generation++
	now = now.Add(time.Second)
	if err := r.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, err := store.Load()
	if err != nil || len(j.Pending) != 3 || j.Pending[0].ConfigGeneration != 4 || j.Pending[1].ConfigGeneration != 4 || j.Pending[2].ConfigGeneration != 5 {
		t.Fatalf("old/new configuration journal: %v %v", j, err)
	}
	if err := r.FlushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sender.reports[1], first) {
		t.Fatal("pending payload changed during configuration switch")
	}
	for index, report := range sender.reports[1:] {
		if report.Sequence != int64(index+1) {
			t.Fatal("delivery reordered")
		}
	}
	j, _ = store.Load()
	if len(j.Pending) != 0 {
		t.Fatal("delivery did not drain")
	}
}

func TestFinalCollectionProtectsFullQueueAndRollsBackUnpersistableCounters(t *testing.T) {
	for _, full := range []bool{true, false} {
		t.Run(map[bool]string{true: "full queue", false: "oversize journal"}[full], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.json")
			store := NewJournalStore(path)
			now := time.Now().UTC()
			source := &checkpointSourceStub{sourceStub: sourceStub{deltas: []CounterDelta{validDelta()}}}
			r, err := NewReporter(store, source, &senderStub{}, "node-one", "credential", maxPendingItems, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if full {
				_, err = store.update(func(j *Journal) error {
					j.Pending = []agentv1.UsageReport{{NodeID: "node-one", BootID: j.BootID, Sequence: 1}}
					j.NextSequence = 2
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				source.deltas = make([]CounterDelta, maxPendingItems)
				for i := range source.deltas {
					source.deltas[i] = validDelta()
					source.deltas[i].CustomerID = strings.Repeat("c", 128)
					source.deltas[i].RuleID = strings.Repeat("r", 128)
					source.deltas[i].EntryGroupID = strings.Repeat("e", 128)
					source.deltas[i].ExitGroupID = strings.Repeat("x", 128)
				}
			}
			before, _ := os.ReadFile(path)
			now = now.Add(time.Second)
			if err := r.CollectOnce(context.Background()); err == nil || errors.Is(err, ErrCounterReadFailed) {
				t.Fatalf("unsafe checkpoint error: %v", err)
			}
			after, _ := os.ReadFile(path)
			if !reflect.DeepEqual(before, after) || source.commits != 0 {
				t.Fatal("failed checkpoint advanced durable state")
			}
			if full && source.calls != 0 {
				t.Fatal("full queue advanced source counters")
			}
			if !full && source.rollbacks != 1 {
				t.Fatal("failed persistence did not roll back source")
			}
		})
	}
}
