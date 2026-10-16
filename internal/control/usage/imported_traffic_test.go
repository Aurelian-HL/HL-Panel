package usage

import (
	"context"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"testing"
	"time"
)

func TestImportedHistoryIsScopedUndatedAndSeparateFromLedger(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	baseline := forwarding.ImportedTraffic{SourceID: "source", BatchID: "batch", LineID: 1, ObservedAt: now, UploadBytes: 30, DownloadBytes: 70}
	qstore := &quotaStore{rules: []forwarding.Rule{
		{ID: "one", CustomerID: "a", EntryGroupID: "entry", ImportedTraffic: baseline},
		{ID: "two", CustomerID: "b", EntryGroupID: "entry", ExitGroupID: "exit", ImportedTraffic: baseline},
		{ID: "new", CustomerID: "a", EntryGroupID: "other"},
	}}
	s := NewService(&stubRepository{queryResult: QueryResult{Totals: Totals{ChargedBytes: 17}, Total: 2}}, nil, nil, WithRuleTrafficRepository(qstore))
	for _, test := range []struct {
		scope Scope
		id    string
		count int
		total int64
	}{
		{ScopeSite, "", 2, 200}, {ScopeCustomer, "a", 1, 100}, {ScopeRule, "two", 1, 100}, {ScopeDeviceGroup, "entry", 2, 200}, {ScopeDeviceGroup, "exit", 1, 100}, {ScopeCustomer, "unknown", 0, 0},
	} {
		r, err := s.Query(context.Background(), Query{Scope: test.scope, ScopeID: test.id})
		if err != nil {
			t.Fatal(err)
		}
		if r.Total != 2 || r.Totals.ChargedBytes != 17 {
			t.Fatal("source history altered ledger totals or event count")
		}
		if test.count == 0 {
			if r.ImportedTraffic != nil {
				t.Fatal("foreign source leaked")
			}
			continue
		}
		if r.ImportedTraffic == nil || r.ImportedTraffic.RuleCount != test.count || r.ImportedTraffic.TotalBytes != test.total {
			t.Fatal("incorrect history scope", test, r)
		}
	}
	r, err := s.Query(context.Background(), Query{Scope: ScopeSite, From: &now})
	if err != nil || r.ImportedTraffic != nil {
		t.Fatal("undated history included in dated query", err)
	}
}
