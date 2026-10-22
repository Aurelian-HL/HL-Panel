package postgressnapshot

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
)

// Readers keep different PostgreSQL snapshots while the shared cache advances.
// Each returned slice is deliberately mutated to expose accidental sharing.
func TestProjectionCacheConcurrentRefreshDetachedSnapshots(t *testing.T) {
	s := openHealthWriteStore(t)
	ctx := context.Background()
	const rows = 128
	if _, err := s.db.Exec(`INSERT INTO hl_panel_rule_traffic
		SELECT 'rule-'||i,1,false,'',10,now() FROM generate_series(1,$1::int) i`, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO hl_panel_protocol_health
		SELECT 'rule-'||i,'node',convert_to(json_build_object('RuleID','rule-'||i,'NodeID','node','RuleRevision',10)::text,'UTF8')
		FROM generate_series(1,$1::int) i`, rows); err != nil {
		t.Fatal(err)
	}
	read := func(tx *sql.Tx, want int64) error {
		traffic, health, err := s.forwardingOverlays(ctx, tx)
		if err != nil {
			return err
		}
		if len(traffic) != rows || len(health) != rows {
			return fmt.Errorf("projection lengths=%d/%d", len(traffic), len(health))
		}
		for i := range traffic {
			if traffic[i].State.TrafficUsedBytes != want || health[i].RuleRevision != want {
				return fmt.Errorf("snapshot values=%d/%d, want=%d", traffic[i].State.TrafficUsedBytes, health[i].RuleRevision, want)
			}
			traffic[i].State.TrafficUsedBytes = -1
			health[i].RuleRevision = -1
		}
		return nil
	}
	const readers = 6
	old := make([]*sql.Tx, readers)
	for i := range old {
		old[i] = overlayReadTransaction(t, s)
		// Establish each snapshot without warming the shared projection cache,
		// so concurrent readers also exercise same-version cache misses.
		var count int
		if err := old[i].QueryRow(`SELECT count(*) FROM hl_panel_rule_traffic`).Scan(&count); err != nil || count != rows {
			t.Fatalf("establish old snapshot: count=%d, err=%v", count, err)
		}
	}
	start := make(chan struct{})
	errors := make(chan error, readers+1)
	var workers sync.WaitGroup
	for _, pinned := range old {
		workers.Go(func() {
			<-start
			for range 12 {
				if err := read(pinned, 10); err != nil {
					errors <- err
					return
				}
				tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
				if err != nil {
					errors <- err
					return
				}
				err = func() error {
					defer tx.Rollback()
					var want int64
					if err := tx.QueryRow(`SELECT used_bytes FROM hl_panel_rule_traffic LIMIT 1`).Scan(&want); err != nil {
						return err
					}
					for range 2 {
						if err := read(tx, want); err != nil {
							return err
						}
					}
					return nil
				}()
				if err != nil {
					errors <- err
					return
				}
			}
		})
	}
	workers.Go(func() {
		<-start
		for value := int64(11); value <= 22; value++ {
			err := func() error {
				tx, err := s.db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				if _, err := tx.Exec(`UPDATE hl_panel_rule_traffic SET used_bytes=$1`, value); err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE hl_panel_protocol_health
					SET payload=convert_to(jsonb_set(convert_from(payload,'UTF8')::jsonb,'{RuleRevision}',to_jsonb($1::bigint))::text,'UTF8')`, value); err != nil {
					return err
				}
				return tx.Commit()
			}()
			if err != nil {
				errors <- err
				return
			}
		}
	})
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	if err := read(overlayReadTransaction(t, s), 22); err != nil {
		t.Fatal(err)
	}
	for _, tx := range old {
		if err := read(tx, 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := read(overlayReadTransaction(t, s), 22); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionCacheEmptySlicesRemainCached(t *testing.T) {
	s := openHealthWriteStore(t)
	tx := overlayReadTransaction(t, s)
	ctx := context.Background()
	if _, _, err := s.forwardingOverlays(ctx, tx); err != nil {
		t.Fatal(err)
	}
	s.projectionMu.RLock()
	trafficRevision, healthRevision := s.projections.ruleTrafficRevision, s.projections.healthRevision
	s.projectionMu.RUnlock()
	// A nil transaction is safe only if the empty cache is recognized as a hit.
	traffic, err := s.ruleTrafficOverlaysAtRevision(ctx, nil, trafficRevision)
	if err != nil || traffic == nil || len(traffic) != 0 {
		t.Fatalf("empty traffic cache: %v %v", traffic, err)
	}
	health, err := s.protocolHealthOverlaysAtRevision(ctx, nil, healthRevision)
	if err != nil || health == nil || len(health) != 0 {
		t.Fatalf("empty health cache: %v %v", health, err)
	}
}
