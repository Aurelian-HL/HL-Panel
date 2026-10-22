package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

// The two baseline methods below are frozen from the pre-change source.
// SHA256: cdd79c91f489a3aa1f0373b0e37352f56402c4a6cad8b1549789deb3afaee944
// They exist only in tests; the production path never calls them.
func (s *Store) baselineTrafficOverlaysBeforeCopyReduction(ctx context.Context, tx *sql.Tx, revision int64) ([]memoryrepo.RuleTrafficOverlay, error) {
	s.projectionMu.RLock()
	if s.projections.ruleTraffic != nil && s.projections.ruleTrafficRevision == revision {
		items := append([]memoryrepo.RuleTrafficOverlay{}, s.projections.ruleTraffic...)
		s.projectionMu.RUnlock()
		return items, nil
	}
	s.projectionMu.RUnlock()
	items, err := readRuleTrafficOverlays(ctx, tx, "")
	if err != nil {
		return nil, err
	}
	s.projectionMu.Lock()
	if s.projections.ruleTraffic == nil || s.projections.ruleTrafficRevision < revision {
		s.projections.ruleTrafficRevision = revision
		s.projections.ruleTraffic = append([]memoryrepo.RuleTrafficOverlay{}, items...)
	}
	if s.projections.ruleTrafficRevision == revision {
		items = append([]memoryrepo.RuleTrafficOverlay{}, s.projections.ruleTraffic...)
	}
	s.projectionMu.Unlock()
	return items, nil
}

func (s *Store) baselineHealthOverlaysBeforeCopyReduction(ctx context.Context, tx *sql.Tx, revision int64) ([]gatewaymembership.ProtocolObservation, error) {
	s.projectionMu.RLock()
	if s.projections.health != nil && s.projections.healthRevision == revision {
		items := append([]gatewaymembership.ProtocolObservation{}, s.projections.health...)
		s.projectionMu.RUnlock()
		return items, nil
	}
	s.projectionMu.RUnlock()
	items, err := readProtocolHealthOverlays(ctx, tx)
	if err != nil {
		return nil, err
	}
	s.projectionMu.Lock()
	if s.projections.health == nil || s.projections.healthRevision < revision {
		s.projections.healthRevision = revision
		s.projections.health = append([]gatewaymembership.ProtocolObservation{}, items...)
	}
	if s.projections.healthRevision == revision {
		items = append([]gatewaymembership.ProtocolObservation{}, s.projections.health...)
	}
	s.projectionMu.Unlock()
	return items, nil
}

// Opt-in paired measurements isolate projection data reads and copies.
// Revision lookup and transaction setup/commit are deliberately excluded.
func TestProjectionCacheCopyPairedPerformance(t *testing.T) {
	if os.Getenv("NYVP_TEST_CACHE_COPY_PERFORMANCE") != "true" {
		t.Skip("set NYVP_TEST_CACHE_COPY_PERFORMANCE=true for paired measurements")
	}
	for _, count := range []int{0, 269, 2000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := openHealthWriteStore(t)
			ctx := context.Background()
			if _, err := s.db.Exec(`INSERT INTO hl_panel_rule_traffic
                SELECT 'rule-'||i,1,false,'',10,now() FROM generate_series(1,$1::int) i`, count); err != nil {
				t.Fatal(err)
			}
			proofs := syntheticHealthProofs(count)
			payloads := make([]json.RawMessage, len(proofs))
			for i := range proofs {
				raw, err := json.Marshal(proofs[i])
				if err != nil {
					t.Fatal(err)
				}
				payloads[i] = raw
			}
			if count > 0 {
				raw, err := json.Marshal(payloads)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec(`INSERT INTO hl_panel_protocol_health
                    SELECT p->>'RuleID',p->>'NodeID',convert_to(p::text,'UTF8') FROM json_array_elements($1::json) p`, string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			tx := overlayReadTransaction(t, s)
			var trafficRevision, healthRevision int64
			if err := tx.QueryRow(`SELECT
                (SELECT revision FROM hl_panel_projection_revisions WHERE projection=$1),
                (SELECT revision FROM hl_panel_projection_revisions WHERE projection=$2)`, ruleTrafficProjection, healthProjection).Scan(&trafficRevision, &healthRevision); err != nil {
				t.Fatal(err)
			}
			read := func(mode string) {
				t.Helper()
				var traffic []memoryrepo.RuleTrafficOverlay
				var health []gatewaymembership.ProtocolObservation
				var err error
				if mode == "baseline" {
					traffic, err = s.baselineTrafficOverlaysBeforeCopyReduction(ctx, tx, trafficRevision)
				} else {
					traffic, err = s.ruleTrafficOverlaysAtRevision(ctx, tx, trafficRevision)
				}
				if err != nil || len(traffic) != count {
					t.Fatalf("traffic length=%d err=%v", len(traffic), err)
				}
				if mode == "baseline" {
					health, err = s.baselineHealthOverlaysBeforeCopyReduction(ctx, tx, healthRevision)
				} else {
					health, err = s.protocolHealthOverlaysAtRevision(ctx, tx, healthRevision)
				}
				if err != nil || len(health) != count {
					t.Fatalf("health length=%d err=%v", len(health), err)
				}
			}
			for _, phase := range []string{"hot", "cold"} {
				iterations := 8
				if phase == "hot" {
					iterations = 128
				}
				measure := func(mode string, sample int) {
					t.Helper()
					var memBefore, memAfter runtime.MemStats
					runtime.ReadMemStats(&memBefore)
					start := time.Now()
					for range iterations {
						if phase == "cold" {
							s.projectionMu.Lock()
							s.projections = projectionCache{}
							s.projectionMu.Unlock()
						}
						read(mode)
					}
					elapsed := time.Since(start)
					runtime.ReadMemStats(&memAfter)
					if sample >= 0 {
						t.Logf("CACHE_COPY_SAMPLE rows=%d phase=%s mode=%s sample=%d read_ns=%d bytes=%d allocs=%d iterations=%d", count, phase, mode, sample, elapsed.Nanoseconds()/int64(iterations), (memAfter.TotalAlloc-memBefore.TotalAlloc)/uint64(iterations), (memAfter.Mallocs-memBefore.Mallocs)/uint64(iterations), iterations)
					}
				}
				for range 3 {
					measure("baseline", -1)
					measure("candidate", -1)
				}
				for sample := range 20 {
					if sample%2 == 0 {
						measure("baseline", sample)
						measure("candidate", sample)
					} else {
						measure("candidate", sample)
						measure("baseline", sample)
					}
				}
			}
		})
	}
}
