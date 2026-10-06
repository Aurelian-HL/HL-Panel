package usage

import (
	"context"
	"github.com/hongle/hl-panel/internal/agent/usage/xraystatsproto"
	"testing"
)

func TestEpochSourceCountsFirstTrafficAcrossProcessRestartsAndJournalRollback(t *testing.T) {
	server, _ := gostServer(t, gostPayload(100, 0), gostPayload(150, 0), gostPayload(500, 0), gostPayload(500, 0), gostPayload(7, 0))
	epoch := uint64(1)
	source := &EpochSource{Source: sourceForServer(t, server), Epoch: func() uint64 { return epoch }}
	collect := func(want int64, rollback bool) {
		t.Helper()
		if err := source.BeginCollection(); err != nil {
			t.Fatal(err)
		}
		got, err := source.CollectUsage(context.Background(), gostWindow())
		if err != nil || len(got) != 1 || got[0].RuleActualBytes != want {
			t.Fatalf("want %d got %v error %v", want, got, err)
		}
		if rollback {
			source.RollbackCollection()
		} else {
			source.CommitCollection()
		}
	}
	collect(100, false) // process created before the first periodic sample
	collect(50, false)
	epoch++
	collect(500, true)  // new total exceeds old total; subtraction would lose 150
	collect(500, false) // failed persistence must not advance the epoch or cursor
	epoch++
	collect(7, false) // new total is smaller than old total
}

func TestXrayKnownNewProcessStartsFromZero(t *testing.T) {
	client := &statsClientStub{responses: []*xraystatsproto.QueryStatsResponse{{Stat: []*xraystatsproto.Stat{{Name: "inbound>>>vless-reality-fwd_rule-1>>>traffic>>>uplink", Value: 42}}}}}
	source := NewXrayStatsSourceWithClient(client, nil)
	source.ResetCounters()
	got, err := source.CollectUsage(context.Background(), gostWindow())
	if err != nil || len(got) != 1 || got[0].RuleActualBytes != 42 {
		t.Fatalf("first owned-process sample: %v %v", got, err)
	}
}
