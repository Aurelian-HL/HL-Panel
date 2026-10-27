package postgressnapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/hongle/hl-panel/internal/control/gatewaymembership"
)

const protocolHealthWriteBatchSize = 128

const protocolHealthInsertPrefix = `INSERT INTO hl_panel_protocol_health(rule_id,node_id,payload) VALUES`
const protocolHealthInsertSuffix = ` ON CONFLICT(rule_id,node_id) DO UPDATE SET payload=excluded.payload`

type protocolHealthWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Proofs have already been checked sequentially against the locked snapshot.
// Keep each key's last accepted proof: rejected later observations must never
// replace it. Only the small overlay is coalesced, not the per-observation
// results, first-activation audit or snapshot revision. All chunks still belong
// to the caller's single transaction; any error requires its rollback.
func writeProtocolHealthOverlays(ctx context.Context, tx protocolHealthWriter, observations []gatewaymembership.ProtocolObservation, results []error) error {
	if len(observations) != len(results) {
		return errors.New("protocol health result count mismatch")
	}
	type proofKey struct{ rule, node string }
	positions := make(map[proofKey]int)
	accepted := make([]gatewaymembership.ProtocolObservation, 0, len(observations))
	for i, observation := range observations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if results[i] != nil {
			continue
		}
		key := proofKey{observation.RuleID, observation.NodeID}
		if position, exists := positions[key]; exists {
			accepted[position] = observation
		} else {
			positions[key] = len(accepted)
			accepted = append(accepted, observation)
		}
	}
	for start := 0; start < len(accepted); start += protocolHealthWriteBatchSize {
		end := min(start+protocolHealthWriteBatchSize, len(accepted))
		batch := accepted[start:end]
		args := make([]any, 0, len(batch)*3)
		for _, observation := range batch {
			raw, err := json.Marshal(observation)
			if err != nil {
				return errors.New("protocol health projection encode failed")
			}
			args = append(args, observation.RuleID, observation.NodeID, raw)
		}
		if _, err := tx.ExecContext(ctx, protocolHealthInsertQuery(len(batch)), args...); err != nil {
			return snapshotFailure(ctx, "overlay_write", err)
		}
	}
	return nil
}

func protocolHealthInsertQuery(count int) string {
	if count == 1 {
		return protocolHealthInsertPrefix + "($1,$2,$3)" + protocolHealthInsertSuffix
	}
	var query strings.Builder
	query.Grow(len(protocolHealthInsertPrefix) + len(protocolHealthInsertSuffix) + count*20)
	query.WriteString(protocolHealthInsertPrefix)
	for i := range count {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteString("($")
		query.WriteString(strconv.Itoa(i*3 + 1))
		query.WriteString(",$")
		query.WriteString(strconv.Itoa(i*3 + 2))
		query.WriteString(",$")
		query.WriteString(strconv.Itoa(i*3 + 3))
		query.WriteByte(')')
	}
	query.WriteString(protocolHealthInsertSuffix)
	return query.String()
}
