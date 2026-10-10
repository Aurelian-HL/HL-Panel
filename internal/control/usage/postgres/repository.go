// Package postgres persists the usage ledger in normalized PostgreSQL tables.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/usage"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

func (repository *Repository) Ingest(ctx context.Context, event usage.Event, project usage.DecisionProjector) (usage.IngestResult, error) {
	if repository == nil || repository.db == nil {
		return usage.IngestResult{}, errors.New("usage PostgreSQL repository is unavailable")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return usage.IngestResult{}, errors.New("usage PostgreSQL transaction unavailable")
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_ingest_cursors
		(node_id, boot_id, last_sequence, updated_at) VALUES ($1, $2, 0, $3)
		ON CONFLICT (node_id, boot_id) DO NOTHING`, event.NodeID, event.BootID, event.ReceivedAt); err != nil {
		return usage.IngestResult{}, errors.New("usage cursor initialization failed")
	}
	var lastSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT last_sequence FROM usage_ingest_cursors
		WHERE node_id=$1 AND boot_id=$2 FOR UPDATE`, event.NodeID, event.BootID).Scan(&lastSequence); err != nil {
		return usage.IngestResult{}, errors.New("usage cursor lock failed")
	}

	stored, exists, err := eventByKey(ctx, tx, event.NodeID, event.BootID, event.Sequence)
	if err != nil {
		return usage.IngestResult{}, err
	}
	if exists {
		if stored.Event.PayloadSHA256 != event.PayloadSHA256 {
			return usage.IngestResult{}, usage.ErrIdempotencyConflict
		}
		totals, err := customerTotals(ctx, tx, stored.Event.CustomerID)
		if err != nil {
			return usage.IngestResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return usage.IngestResult{}, errors.New("usage replay transaction commit failed")
		}
		return usage.IngestResult{Event: stored.Event, CustomerTotals: totals, Decision: stored.Decision, Replayed: true}, nil
	}
	if lastSequence == math.MaxInt64 {
		return usage.IngestResult{}, usage.ErrSequenceOverflow
	}
	expected := lastSequence + 1
	if event.Sequence < expected {
		return usage.IngestResult{}, fmt.Errorf("%w: expected %d, received %d", usage.ErrSequenceOutOfOrder, expected, event.Sequence)
	}
	if event.Sequence > expected {
		return usage.IngestResult{}, fmt.Errorf("%w: expected %d, received %d", usage.ErrSequenceGap, expected, event.Sequence)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events (
		node_id, boot_id, sequence, payload_sha256, customer_id, rule_id, entry_group_id, exit_group_id,
		protocol, occurred_at, period_started_at, period_ended_at, rule_actual_bytes, customer_actual_bytes,
		charged_bytes, entry_multiplier_micros, exit_multiplier_micros, received_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		event.NodeID, event.BootID, event.Sequence, event.PayloadSHA256, event.CustomerID, event.RuleID,
		event.EntryGroupID, event.ExitGroupID, event.Protocol, event.OccurredAt, event.PeriodStartedAt, event.PeriodEndedAt,
		event.RuleActualBytes, event.CustomerActualBytes, event.ChargedBytes,
		event.EntryMultiplierMicros, event.ExitMultiplierMicros, event.ReceivedAt); err != nil {
		return usage.IngestResult{}, errors.New("usage event insert failed")
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_customer_totals
		(customer_id, actual_bytes, charged_bytes, last_usage_occurred_at, updated_at)
		VALUES ($1,0,0,$2,$3) ON CONFLICT (customer_id) DO NOTHING`,
		event.CustomerID, event.OccurredAt, event.ReceivedAt); err != nil {
		return usage.IngestResult{}, errors.New("usage customer total initialization failed")
	}
	current, err := lockCustomerTotals(ctx, tx, event.CustomerID)
	if err != nil {
		return usage.IngestResult{}, err
	}
	if event.CustomerActualBytes > math.MaxInt64-current.ActualBytes || event.ChargedBytes > math.MaxInt64-current.ChargedBytes {
		return usage.IngestResult{}, usage.ErrByteOverflow
	}
	current.ActualBytes += event.CustomerActualBytes
	current.ChargedBytes += event.ChargedBytes
	if event.OccurredAt.After(current.LastUsageOccurredAt) {
		current.LastUsageOccurredAt = event.OccurredAt
	}
	current.UpdatedAt = event.ReceivedAt
	if _, err := tx.ExecContext(ctx, `UPDATE usage_customer_totals SET actual_bytes=$2, charged_bytes=$3,
		last_usage_occurred_at=$4, updated_at=$5 WHERE customer_id=$1`, current.CustomerID,
		current.ActualBytes, current.ChargedBytes, current.LastUsageOccurredAt, current.UpdatedAt); err != nil {
		return usage.IngestResult{}, errors.New("usage customer total update failed")
	}

	var decision *usage.EnforcementDecision
	if project != nil {
		decision, err = project(current)
		if err != nil {
			return usage.IngestResult{}, err
		}
	}
	if decision != nil {
		if decision.Status != usage.EnforcementPending || decision.CustomerID != event.CustomerID ||
			decision.TriggerNodeID != event.NodeID || decision.TriggerBootID != event.BootID ||
			decision.TriggerSequence != event.Sequence {
			return usage.IngestResult{}, errors.New("usage enforcement projection is invalid")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_enforcement_decisions (
			id, customer_id, rule_id, protocol, reason, action, status, trigger_node_id, trigger_boot_id, trigger_sequence,
			customer_charged_bytes, traffic_limit_bytes, created_at, updated_at, revision, last_error
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, decision.ID, decision.CustomerID,
			decision.RuleID, decision.Protocol, decision.Reason, decision.Action, decision.Status, decision.TriggerNodeID, decision.TriggerBootID,
			decision.TriggerSequence, decision.CustomerChargedBytes, decision.TrafficLimitBytes, decision.CreatedAt,
			decision.UpdatedAt, decision.Revision, decision.LastError); err != nil {
			return usage.IngestResult{}, errors.New("usage enforcement decision insert failed")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE usage_ingest_cursors SET last_sequence=$3, updated_at=$4
		WHERE node_id=$1 AND boot_id=$2`, event.NodeID, event.BootID, event.Sequence, event.ReceivedAt); err != nil {
		return usage.IngestResult{}, errors.New("usage cursor update failed")
	}
	if err := tx.Commit(); err != nil {
		return usage.IngestResult{}, errors.New("usage ingest transaction commit failed")
	}
	return usage.IngestResult{Event: event, CustomerTotals: current, Decision: decision}, nil
}

func (repository *Repository) Query(ctx context.Context, query usage.Query) (usage.QueryResult, error) {
	if repository == nil || repository.db == nil {
		return usage.QueryResult{}, errors.New("usage PostgreSQL repository is unavailable")
	}
	// Aggregates and pagination must observe the same committed ledger, even
	// when an agent reports usage between the two SELECTs. MVCC adds no write
	// locks and lets ingest continue while this bounded request reads.
	tx, err := repository.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return usage.QueryResult{}, errors.New("usage query transaction unavailable")
	}
	defer tx.Rollback()
	where, args := queryFilter(query)
	var result usage.QueryResult
	totalsSQL := `SELECT count(*), COALESCE(sum(rule_actual_bytes),0)::bigint,
		COALESCE(sum(customer_actual_bytes),0)::bigint, COALESCE(sum(charged_bytes),0)::bigint
		FROM usage_events e` + where
	if err := tx.QueryRowContext(ctx, totalsSQL, args...).Scan(
		&result.Total, &result.Totals.RuleActualBytes, &result.Totals.CustomerActualBytes, &result.Totals.ChargedBytes,
	); err != nil {
		return usage.QueryResult{}, errors.New("usage aggregate query failed")
	}

	pageArgs := append([]any(nil), args...)
	limitParameter := fmt.Sprintf("$%d", len(pageArgs)+1)
	pageArgs = append(pageArgs, query.PageSize)
	offsetParameter := fmt.Sprintf("$%d", len(pageArgs)+1)
	pageArgs = append(pageArgs, (query.Page-1)*query.PageSize)
	rows, err := tx.QueryContext(ctx, `SELECT
		e.node_id,e.boot_id,e.sequence,e.customer_id,e.rule_id,e.entry_group_id,e.exit_group_id,e.protocol,
		e.occurred_at,e.period_started_at,e.period_ended_at,e.rule_actual_bytes,e.customer_actual_bytes,
		e.entry_multiplier_micros,e.exit_multiplier_micros,e.charged_bytes,e.payload_sha256,e.received_at,
		d.id,d.customer_id,d.rule_id,d.protocol,d.reason,d.action,d.status,d.trigger_node_id,d.trigger_boot_id,d.trigger_sequence,
		d.customer_charged_bytes,d.traffic_limit_bytes,d.created_at,d.updated_at,d.revision,d.last_error
		FROM usage_events e LEFT JOIN usage_enforcement_decisions d
		ON d.trigger_node_id=e.node_id AND d.trigger_boot_id=e.boot_id AND d.trigger_sequence=e.sequence`+
		where+` ORDER BY e.occurred_at DESC,e.node_id,e.boot_id,e.sequence DESC LIMIT `+limitParameter+` OFFSET `+offsetParameter,
		pageArgs...)
	if err != nil {
		return usage.QueryResult{}, errors.New("usage detail query failed")
	}
	defer rows.Close()
	result.Items = make([]usage.Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return usage.QueryResult{}, err
		}
		result.Items = append(result.Items, record)
	}
	if err := rows.Err(); err != nil {
		return usage.QueryResult{}, errors.New("usage detail iteration failed")
	}
	if err := rows.Close(); err != nil {
		return usage.QueryResult{}, errors.New("usage detail close failed")
	}
	if err := tx.Commit(); err != nil {
		return usage.QueryResult{}, errors.New("usage query transaction commit failed")
	}
	result.Page, result.PageSize = query.Page, query.PageSize
	return result, nil
}

func (repository *Repository) DesiredEnforcement(ctx context.Context, nodeID string) (*usage.EnforcementDecision, error) {
	if repository == nil || repository.db == nil {
		return nil, errors.New("usage PostgreSQL repository is unavailable")
	}
	row := repository.db.QueryRowContext(ctx, `SELECT `+decisionColumns+` FROM usage_enforcement_decisions
		WHERE trigger_node_id=$1 AND status IN ('pending','revoke_pending')
		ORDER BY created_at,id LIMIT 1`, nodeID)
	decision, err := scanDecision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &decision, nil
}

func (repository *Repository) RecordEnforcementResult(ctx context.Context, result usage.EnforcementResult) (usage.EnforcementDecision, bool, error) {
	if repository == nil || repository.db == nil {
		return usage.EnforcementDecision{}, false, errors.New("usage PostgreSQL repository is unavailable")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return usage.EnforcementDecision{}, false, errors.New("usage enforcement result transaction unavailable")
	}
	defer tx.Rollback()
	var existingHash string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM usage_enforcement_results
		WHERE decision_id=$1 AND command_revision=$2`, result.DecisionID, result.Revision).Scan(&existingHash)
	if err == nil {
		if existingHash != result.PayloadSHA256 {
			return usage.EnforcementDecision{}, false, usage.ErrIdempotencyConflict
		}
		decision, err := decisionByID(ctx, tx, result.DecisionID, false)
		if err != nil {
			return usage.EnforcementDecision{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return usage.EnforcementDecision{}, false, errors.New("usage enforcement replay commit failed")
		}
		return decision, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return usage.EnforcementDecision{}, false, errors.New("usage enforcement result lookup failed")
	}
	decision, err := decisionByID(ctx, tx, result.DecisionID, true)
	if err != nil {
		return usage.EnforcementDecision{}, false, err
	}
	if decision.TriggerNodeID != result.NodeID || decision.Revision != result.Revision || decision.Action != result.Action {
		return usage.EnforcementDecision{}, false, fmt.Errorf("%w: enforcement command is stale or belongs to another node", faults.ErrConflict)
	}
	if decision.Status != usage.EnforcementPending && decision.Status != usage.EnforcementRevokePending {
		return usage.EnforcementDecision{}, false, fmt.Errorf("%w: enforcement command is not pending", faults.ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_enforcement_results
		(decision_id,command_revision,node_id,action,result_status,payload_sha256,sanitized_message,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, result.DecisionID, result.Revision, result.NodeID, result.Action,
		result.Status, result.PayloadSHA256, result.Message, result.CreatedAt); err != nil {
		return usage.EnforcementDecision{}, false, errors.New("usage enforcement result insert failed")
	}
	if decision.Revision == math.MaxInt64 {
		return usage.EnforcementDecision{}, false, usage.ErrSequenceOverflow
	}
	if result.Status == agentv1.EnforcementResultSucceeded {
		if decision.Status == usage.EnforcementPending {
			decision.Status = usage.EnforcementApplied
		} else {
			decision.Status = usage.EnforcementRevoked
		}
		decision.LastError = ""
	} else {
		if decision.Status == usage.EnforcementPending {
			decision.Status = usage.EnforcementApplyFailed
		} else {
			decision.Status = usage.EnforcementRevokeFailed
		}
		decision.LastError = result.Message
	}
	decision.Revision++
	decision.UpdatedAt = result.CreatedAt
	if _, err := tx.ExecContext(ctx, `UPDATE usage_enforcement_decisions SET status=$2,last_error=$3,
		revision=$4,updated_at=$5 WHERE id=$1`, decision.ID, decision.Status, decision.LastError,
		decision.Revision, decision.UpdatedAt); err != nil {
		return usage.EnforcementDecision{}, false, errors.New("usage enforcement decision update failed")
	}
	if err := tx.Commit(); err != nil {
		return usage.EnforcementDecision{}, false, errors.New("usage enforcement result commit failed")
	}
	return decision, false, nil
}

func (repository *Repository) RequestRevoke(ctx context.Context, request usage.RevokeRequest) (usage.EnforcementDecision, bool, error) {
	if repository == nil || repository.db == nil {
		return usage.EnforcementDecision{}, false, errors.New("usage PostgreSQL repository is unavailable")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return usage.EnforcementDecision{}, false, errors.New("usage revoke transaction unavailable")
	}
	defer tx.Rollback()
	decision, err := decisionByID(ctx, tx, request.DecisionID, true)
	if err != nil {
		return usage.EnforcementDecision{}, false, err
	}
	var storedKey, storedHash sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT revoke_idempotency_key,revoke_request_sha256
		FROM usage_enforcement_decisions WHERE id=$1`, request.DecisionID).Scan(&storedKey, &storedHash); err != nil {
		return usage.EnforcementDecision{}, false, errors.New("usage revoke idempotency lookup failed")
	}
	if storedKey.Valid || storedHash.Valid {
		if storedKey.String != request.IdempotencyKey || storedHash.String != request.RequestSHA256 {
			return usage.EnforcementDecision{}, false, usage.ErrIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return usage.EnforcementDecision{}, false, errors.New("usage revoke replay commit failed")
		}
		return decision, true, nil
	}
	if decision.Status == usage.EnforcementRevoked {
		return usage.EnforcementDecision{}, false, fmt.Errorf("%w: enforcement decision is already revoked", faults.ErrConflict)
	}
	if decision.Revision == math.MaxInt64 {
		return usage.EnforcementDecision{}, false, usage.ErrSequenceOverflow
	}
	decision.Action = agentv1.EnforcementEnableCustomer
	decision.Status = usage.EnforcementRevokePending
	decision.LastError = ""
	decision.Revision++
	decision.UpdatedAt = request.RequestedAt
	if _, err := tx.ExecContext(ctx, `UPDATE usage_enforcement_decisions SET action=$2,status=$3,last_error='',
		revision=$4,updated_at=$5,revoke_administrator_id=$6,revoke_idempotency_key=$7,
		revoke_request_sha256=$8,revoke_requested_at=$5 WHERE id=$1`, decision.ID, decision.Action,
		decision.Status, decision.Revision, decision.UpdatedAt, request.AdministratorID,
		request.IdempotencyKey, request.RequestSHA256); err != nil {
		return usage.EnforcementDecision{}, false, errors.New("usage revoke update failed")
	}
	if err := tx.Commit(); err != nil {
		return usage.EnforcementDecision{}, false, errors.New("usage revoke commit failed")
	}
	return decision, false, nil
}

func queryFilter(query usage.Query) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	parameter := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	switch query.Scope {
	case usage.ScopeCustomer:
		conditions = append(conditions, "e.customer_id="+parameter(query.ScopeID))
	case usage.ScopeRule:
		conditions = append(conditions, "e.rule_id="+parameter(query.ScopeID))
	case usage.ScopeDeviceGroup:
		value := parameter(query.ScopeID)
		conditions = append(conditions, "(e.entry_group_id="+value+" OR e.exit_group_id="+value+")")
	}
	if query.From != nil {
		conditions = append(conditions, "e.occurred_at >= "+parameter(*query.From))
	}
	if query.To != nil {
		conditions = append(conditions, "e.occurred_at < "+parameter(*query.To))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

type scanner interface{ Scan(...any) error }

func eventByKey(ctx context.Context, tx *sql.Tx, nodeID, bootID string, sequence int64) (usage.Record, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT
		e.node_id,e.boot_id,e.sequence,e.customer_id,e.rule_id,e.entry_group_id,e.exit_group_id,e.protocol,
		e.occurred_at,e.period_started_at,e.period_ended_at,e.rule_actual_bytes,e.customer_actual_bytes,
		e.entry_multiplier_micros,e.exit_multiplier_micros,e.charged_bytes,e.payload_sha256,e.received_at,
		d.id,d.customer_id,d.rule_id,d.protocol,d.reason,d.action,d.status,d.trigger_node_id,d.trigger_boot_id,d.trigger_sequence,
		d.customer_charged_bytes,d.traffic_limit_bytes,d.created_at,d.updated_at,d.revision,d.last_error
		FROM usage_events e LEFT JOIN usage_enforcement_decisions d
		ON d.trigger_node_id=e.node_id AND d.trigger_boot_id=e.boot_id AND d.trigger_sequence=e.sequence
		WHERE e.node_id=$1 AND e.boot_id=$2 AND e.sequence=$3`, nodeID, bootID, sequence)
	record, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return usage.Record{}, false, nil
	}
	if err != nil {
		return usage.Record{}, false, err
	}
	return record, true, nil
}

func scanRecord(source scanner) (usage.Record, error) {
	var record usage.Record
	var decisionID, decisionCustomerID, decisionRuleID, decisionProtocol, reason, action, status, lastError sql.NullString
	var triggerNodeID, triggerBootID sql.NullString
	var triggerSequence, customerChargedBytes, trafficLimitBytes sql.NullInt64
	var decisionCreatedAt, decisionUpdatedAt sql.NullTime
	var decisionRevision sql.NullInt64
	err := source.Scan(
		&record.NodeID, &record.BootID, &record.Sequence, &record.CustomerID, &record.RuleID,
		&record.EntryGroupID, &record.ExitGroupID, &record.Protocol, &record.OccurredAt, &record.PeriodStartedAt,
		&record.PeriodEndedAt, &record.RuleActualBytes, &record.CustomerActualBytes,
		&record.EntryMultiplierMicros, &record.ExitMultiplierMicros, &record.ChargedBytes,
		&record.PayloadSHA256, &record.ReceivedAt, &decisionID, &decisionCustomerID, &decisionRuleID,
		&decisionProtocol, &reason, &action,
		&status, &triggerNodeID, &triggerBootID, &triggerSequence, &customerChargedBytes,
		&trafficLimitBytes, &decisionCreatedAt, &decisionUpdatedAt, &decisionRevision, &lastError,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return usage.Record{}, sql.ErrNoRows
		}
		return usage.Record{}, errors.New("usage event decode failed")
	}
	if decisionID.Valid {
		record.Decision = &usage.EnforcementDecision{
			ID: decisionID.String, CustomerID: decisionCustomerID.String, RuleID: decisionRuleID.String,
			Protocol: decisionProtocol.String, Reason: usage.EnforcementReason(reason.String),
			Action: agentv1.EnforcementAction(action.String), Status: usage.EnforcementStatus(status.String), TriggerNodeID: triggerNodeID.String,
			TriggerBootID: triggerBootID.String, TriggerSequence: triggerSequence.Int64,
			CustomerChargedBytes: customerChargedBytes.Int64, TrafficLimitBytes: trafficLimitBytes.Int64,
			CreatedAt: decisionCreatedAt.Time, UpdatedAt: decisionUpdatedAt.Time, Revision: decisionRevision.Int64,
			LastError: lastError.String,
		}
	}
	return record, nil
}

const decisionColumns = `id,customer_id,rule_id,protocol,reason,action,status,trigger_node_id,trigger_boot_id,trigger_sequence,
	customer_charged_bytes,traffic_limit_bytes,created_at,updated_at,revision,last_error`

func decisionByID(ctx context.Context, tx *sql.Tx, decisionID string, lock bool) (usage.EnforcementDecision, error) {
	query := `SELECT ` + decisionColumns + ` FROM usage_enforcement_decisions WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	decision, err := scanDecision(tx.QueryRowContext(ctx, query, decisionID))
	if errors.Is(err, sql.ErrNoRows) {
		return usage.EnforcementDecision{}, faults.ErrNotFound
	}
	return decision, err
}

func scanDecision(source scanner) (usage.EnforcementDecision, error) {
	var decision usage.EnforcementDecision
	err := source.Scan(&decision.ID, &decision.CustomerID, &decision.RuleID, &decision.Protocol, &decision.Reason, &decision.Action, &decision.Status,
		&decision.TriggerNodeID, &decision.TriggerBootID, &decision.TriggerSequence, &decision.CustomerChargedBytes,
		&decision.TrafficLimitBytes, &decision.CreatedAt, &decision.UpdatedAt, &decision.Revision, &decision.LastError)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return usage.EnforcementDecision{}, sql.ErrNoRows
		}
		return usage.EnforcementDecision{}, errors.New("usage enforcement decision decode failed")
	}
	return decision, nil
}

func customerTotals(ctx context.Context, tx *sql.Tx, customerID string) (usage.CustomerTotals, error) {
	var totals usage.CustomerTotals
	err := tx.QueryRowContext(ctx, `SELECT customer_id,actual_bytes,charged_bytes,last_usage_occurred_at,updated_at
		FROM usage_customer_totals WHERE customer_id=$1`, customerID).Scan(&totals.CustomerID, &totals.ActualBytes,
		&totals.ChargedBytes, &totals.LastUsageOccurredAt, &totals.UpdatedAt)
	if err != nil {
		return usage.CustomerTotals{}, errors.New("usage customer totals lookup failed")
	}
	return totals, nil
}

func lockCustomerTotals(ctx context.Context, tx *sql.Tx, customerID string) (usage.CustomerTotals, error) {
	var totals usage.CustomerTotals
	err := tx.QueryRowContext(ctx, `SELECT customer_id,actual_bytes,charged_bytes,last_usage_occurred_at,updated_at
		FROM usage_customer_totals WHERE customer_id=$1 FOR UPDATE`, customerID).Scan(&totals.CustomerID,
		&totals.ActualBytes, &totals.ChargedBytes, &totals.LastUsageOccurredAt, &totals.UpdatedAt)
	if err != nil {
		return usage.CustomerTotals{}, errors.New("usage customer totals lock failed")
	}
	return totals, nil
}

var _ usage.Repository = (*Repository)(nil)
