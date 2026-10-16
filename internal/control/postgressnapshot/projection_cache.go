package postgressnapshot

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
	"github.com/hongle/hl-panel/internal/control/memoryrepo"
)

const (
	ruleTrafficProjection = "rule_traffic"
	healthProjection      = "protocol_health"
)

// Projection versions are tiny, transactional invalidation markers. They
// avoid scanning the high-frequency projection tables on every snapshot read
// while remaining correct across multiple control-api processes.
func initializeProjectionRevisionTracking(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS hl_panel_projection_revisions (
        projection text PRIMARY KEY, revision bigint NOT NULL CHECK (revision >= 0)
    )`); err != nil {
		return errors.New("projection revision table initialization failed")
	}
	for _, name := range []string{ruleTrafficProjection, healthProjection} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hl_panel_projection_revisions(projection,revision)
            VALUES($1,0) ON CONFLICT (projection) DO NOTHING`, name); err != nil {
			return errors.New("projection revision initialization failed")
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE OR REPLACE FUNCTION hl_panel_bump_projection_revision() RETURNS trigger
        LANGUAGE plpgsql AS $$
        BEGIN
            INSERT INTO hl_panel_projection_revisions(projection,revision)
            VALUES(TG_ARGV[0],1)
            ON CONFLICT(projection) DO UPDATE SET revision=hl_panel_projection_revisions.revision+1;
            RETURN NULL;
        END $$`); err != nil {
		return errors.New("projection revision trigger initialization failed")
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS hl_panel_rule_traffic_revision ON hl_panel_rule_traffic;
        CREATE TRIGGER hl_panel_rule_traffic_revision AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON hl_panel_rule_traffic
        FOR EACH STATEMENT EXECUTE FUNCTION hl_panel_bump_projection_revision('rule_traffic');
        DROP TRIGGER IF EXISTS hl_panel_protocol_health_revision ON hl_panel_protocol_health;
        CREATE TRIGGER hl_panel_protocol_health_revision AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON hl_panel_protocol_health
        FOR EACH STATEMENT EXECUTE FUNCTION hl_panel_bump_projection_revision('protocol_health')`); err != nil {
		return errors.New("projection revision trigger creation failed")
	}
	return nil
}

func (s *Store) projectionRevision(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM hl_panel_projection_revisions WHERE projection=$1`, name).Scan(&revision); err != nil {
		return 0, errors.New("projection revision read failed")
	}
	return revision, nil
}

func (s *Store) ruleTrafficOverlays(ctx context.Context, tx *sql.Tx) ([]memoryrepo.RuleTrafficOverlay, error) {
	revision, err := s.projectionRevision(ctx, tx, ruleTrafficProjection)
	if err != nil {
		return nil, err
	}
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

func (s *Store) protocolHealthOverlays(ctx context.Context, tx *sql.Tx) ([]gatewaymembership.ProtocolObservation, error) {
	revision, err := s.projectionRevision(ctx, tx, healthProjection)
	if err != nil {
		return nil, err
	}
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
