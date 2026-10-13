package postgressnapshot

import (
	"context"
	"errors"
	"time"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

func (s *Store) SyncRuleTraffic(ctx context.Context, id string, total int64, at time.Time) error {
	return s.applyRuleTraffic(ctx, forwarding.RuleTrafficProjection{RuleID: id, TotalBytes: total, At: at}, false)
}
func (s *Store) ApplyRuleTraffic(ctx context.Context, projection forwarding.RuleTrafficProjection) error {
	return s.applyRuleTraffic(ctx, projection, true)
}
func (s *Store) applyRuleTraffic(parent context.Context, p forwarding.RuleTrafficProjection, checked bool) error {
	if p.TotalBytes < 0 || (checked && (p.ExpectedRevision < 1 || p.At.IsZero())) {
		return faults.ErrValidation
	}
	ctx, cancel := context.WithTimeout(parent, operationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("rule traffic transaction unavailable")
	}
	defer tx.Rollback()
	// Concurrent counter writes share this lock. Business edits, restore and
	// quota transitions take the exclusive snapshot lock before reading counters.
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM nyvp_control_snapshots WHERE singleton=true FOR SHARE`).Scan(&revision); err != nil {
		return errors.New("rule traffic snapshot lock failed")
	}
	cached, _, err := s.validatedReadSnapshot(ctx, tx)
	if err != nil {
		return err
	}
	rule, err := cached.usage.RuleTrafficState(ctx, p.RuleID)
	if err != nil {
		return err
	}
	overlays, err := readRuleTrafficOverlays(ctx, tx, p.RuleID)
	if err != nil {
		return err
	}
	for _, overlay := range overlays {
		rule = memoryrepo.MergeRuleTrafficOverlay(rule, overlay)
	}
	ignored := checked && (rule.Revision != p.ExpectedRevision || rule.TrafficQuotaMonthly != p.Monthly)
	period := rule.TrafficUsagePeriod
	if rule.TrafficQuotaMonthly {
		period = forwarding.TrafficQuotaPeriod(p.At)
		ignored = ignored || rule.TrafficUsagePeriod > period
	}
	changedPeriod := rule.TrafficQuotaMonthly && period != rule.TrafficUsagePeriod
	if ignored || (!changedPeriod && p.TotalBytes <= rule.TrafficUsedBytes) {
		if err = tx.Commit(); err != nil {
			return errors.New("rule traffic transaction commit failed")
		}
		return nil
	}
	before := rule.TrafficLimitBytes > 0 && rule.TrafficUsedBytes >= rule.TrafficLimitBytes
	after := rule.TrafficLimitBytes > 0 && p.TotalBytes >= rule.TrafficLimitBytes
	if changedPeriod || before != after {
		// Release the shared lock/connection before the normal exclusive path.
		// It rereads both stores and retains compile rollback and audit semantics.
		if err = tx.Rollback(); err != nil {
			return errors.New("rule traffic read rollback failed")
		}
		_, err = transact(parent, s, true, func(m *memoryrepo.Store) (struct{}, error) {
			if checked {
				return struct{}{}, m.ApplyRuleTraffic(parent, p)
			}
			return struct{}{}, m.SyncRuleTraffic(parent, p.RuleID, p.TotalBytes, p.At)
		})
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hl_panel_rule_traffic(rule_id,rule_revision,monthly,period,used_bytes,updated_at)
    VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(rule_id) DO UPDATE SET
    rule_revision=excluded.rule_revision,monthly=excluded.monthly,period=excluded.period,
    used_bytes=CASE WHEN hl_panel_rule_traffic.rule_revision=excluded.rule_revision AND hl_panel_rule_traffic.monthly=excluded.monthly AND hl_panel_rule_traffic.period=excluded.period
    THEN GREATEST(hl_panel_rule_traffic.used_bytes,excluded.used_bytes) ELSE excluded.used_bytes END,
    updated_at=CASE WHEN hl_panel_rule_traffic.rule_revision=excluded.rule_revision AND hl_panel_rule_traffic.monthly=excluded.monthly AND hl_panel_rule_traffic.period=excluded.period AND hl_panel_rule_traffic.used_bytes>excluded.used_bytes
    THEN hl_panel_rule_traffic.updated_at ELSE excluded.updated_at END`, rule.ID, rule.Revision, rule.TrafficQuotaMonthly, period, p.TotalBytes, p.At)
	if err != nil {
		return errors.New("rule traffic projection write failed")
	}
	if err = tx.Commit(); err != nil {
		return errors.New("rule traffic transaction commit failed")
	}
	return nil
}
