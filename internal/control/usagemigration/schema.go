package usagemigration

import (
	"context"
	"database/sql"
	"fmt"
)

type constraintSpec struct {
	name string
	kind string
}

type columnSpec struct {
	name     string
	dataType string
	nullable bool
}

type relationSpec struct {
	name        string
	columns     []columnSpec
	constraints []constraintSpec
	indexes     []string
}

var schemaRelations = []relationSpec{
	{
		name: "usage_ingest_cursors",
		constraints: []constraintSpec{
			{name: "usage_ingest_cursors_pkey", kind: "p"},
			{name: "usage_cursor_node_length", kind: "c"},
			{name: "usage_cursor_boot_length", kind: "c"},
			{name: "usage_cursor_sequence_nonnegative", kind: "c"},
		},
	},
	{
		name: "usage_events",
		columns: []columnSpec{
			{name: "protocol", dataType: "text"},
		},
		constraints: []constraintSpec{
			{name: "usage_events_pkey", kind: "p"},
			{name: "usage_event_cursor_fk", kind: "f"},
			{name: "usage_event_sequence_positive", kind: "c"},
			{name: "usage_event_payload_sha256", kind: "c"},
			{name: "usage_event_protocol_valid", kind: "c"},
			{name: "usage_event_period_order", kind: "c"},
			{name: "usage_event_bytes_nonnegative", kind: "c"},
			{name: "usage_event_entry_multiplier_range", kind: "c"},
			{name: "usage_event_exit_multiplier_range", kind: "c"},
		},
		indexes: []string{
			"usage_events_customer_time_idx",
			"usage_events_rule_time_idx",
			"usage_events_entry_group_time_idx",
			"usage_events_exit_group_time_idx",
			"usage_events_occurred_idx",
		},
	},
	{
		name: "usage_customer_totals",
		constraints: []constraintSpec{
			{name: "usage_customer_totals_pkey", kind: "p"},
			{name: "usage_customer_totals_nonnegative", kind: "c"},
		},
	},
	{
		name: "usage_enforcement_decisions",
		columns: []columnSpec{
			{name: "rule_id", dataType: "text"},
			{name: "protocol", dataType: "text"},
		},
		constraints: []constraintSpec{
			{name: "usage_enforcement_decisions_pkey", kind: "p"},
			{name: "usage_decision_event_fk", kind: "f"},
			{name: "usage_decision_event_uq", kind: "u"},
			{name: "usage_decision_reason_valid", kind: "c"},
			{name: "usage_decision_protocol_valid", kind: "c"},
			{name: "usage_decision_action_valid", kind: "c"},
			{name: "usage_decision_status_valid", kind: "c"},
			{name: "usage_decision_totals_nonnegative", kind: "c"},
			{name: "usage_decision_revision_positive", kind: "c"},
			{name: "usage_decision_last_error_bounded", kind: "c"},
			{name: "usage_decision_revoke_admin_length", kind: "c"},
			{name: "usage_decision_revoke_key_length", kind: "c"},
			{name: "usage_decision_revoke_metadata_complete", kind: "c"},
		},
		indexes: []string{
			"usage_enforcement_pending_idx",
			"usage_enforcement_node_commands_idx",
		},
	},
	{
		name: "usage_enforcement_results",
		constraints: []constraintSpec{
			{name: "usage_enforcement_results_pkey", kind: "p"},
			{name: "usage_enforcement_results_decision_id_fkey", kind: "f"},
			{name: "usage_enforcement_result_revision_positive", kind: "c"},
			{name: "usage_enforcement_result_action_valid", kind: "c"},
			{name: "usage_enforcement_result_status_valid", kind: "c"},
			{name: "usage_enforcement_result_payload_sha256", kind: "c"},
			{name: "usage_enforcement_result_message_bounded", kind: "c"},
		},
		indexes: []string{"usage_enforcement_results_created_idx"},
	},
}

var rollbackProtectedRelations = []string{
	"usage_events",
	"usage_enforcement_decisions",
	"usage_enforcement_results",
}

func verifySchema(ctx context.Context, tx *sql.Tx) error {
	for _, relation := range schemaRelations {
		kind, err := relationKind(ctx, tx, relation.name)
		if err != nil {
			return err
		}
		if kind != "r" && kind != "p" {
			return driftf("required table %s is missing", relation.name)
		}
		if err := verifyColumns(ctx, tx, relation); err != nil {
			return err
		}
		if err := verifyConstraints(ctx, tx, relation); err != nil {
			return err
		}
		if err := verifyIndexes(ctx, tx, relation); err != nil {
			return err
		}
	}
	return nil
}

func verifyColumns(ctx context.Context, tx *sql.Tx, relation relationSpec) error {
	if len(relation.columns) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT column_name,data_type,is_nullable
		FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name=$1`, relation.name)
	if err != nil {
		return fmt.Errorf("inspect columns for %s: %w", relation.name, err)
	}
	defer rows.Close()
	found := make(map[string]columnSpec)
	for rows.Next() {
		var name, dataType, nullable string
		if err := rows.Scan(&name, &dataType, &nullable); err != nil {
			return fmt.Errorf("decode columns for %s: %w", relation.name, err)
		}
		found[name] = columnSpec{name: name, dataType: dataType, nullable: nullable == "YES"}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate columns for %s: %w", relation.name, err)
	}
	for _, expected := range relation.columns {
		actual, ok := found[expected.name]
		if !ok || actual.dataType != expected.dataType || actual.nullable != expected.nullable {
			return driftf("required column %s on %s is missing or incompatible", expected.name, relation.name)
		}
	}
	return nil
}

func verifyConstraints(ctx context.Context, tx *sql.Tx, relation relationSpec) error {
	rows, err := tx.QueryContext(ctx, `SELECT constraint_name, constraint_type::text, convalidated
		FROM (
			SELECT con.conname AS constraint_name, con.contype AS constraint_type, con.convalidated
			FROM pg_catalog.pg_constraint con
			JOIN pg_catalog.pg_class rel ON rel.oid=con.conrelid
			JOIN pg_catalog.pg_namespace ns ON ns.oid=rel.relnamespace
			WHERE ns.nspname=current_schema() AND rel.relname=$1
		) constraints`, relation.name)
	if err != nil {
		return fmt.Errorf("inspect constraints for %s: %w", relation.name, err)
	}
	defer rows.Close()
	found := make(map[string]constraintSpec)
	validated := make(map[string]bool)
	for rows.Next() {
		var spec constraintSpec
		var valid bool
		if err := rows.Scan(&spec.name, &spec.kind, &valid); err != nil {
			return fmt.Errorf("decode constraints for %s: %w", relation.name, err)
		}
		found[spec.name] = spec
		validated[spec.name] = valid
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate constraints for %s: %w", relation.name, err)
	}
	for _, expected := range relation.constraints {
		actual, ok := found[expected.name]
		if !ok || actual.kind != expected.kind || !validated[expected.name] {
			return driftf("required constraint %s on %s is missing, invalid, or unvalidated", expected.name, relation.name)
		}
	}
	return nil
}

func verifyIndexes(ctx context.Context, tx *sql.Tx, relation relationSpec) error {
	rows, err := tx.QueryContext(ctx, `SELECT indexname FROM pg_catalog.pg_indexes
		WHERE schemaname=current_schema() AND tablename=$1`, relation.name)
	if err != nil {
		return fmt.Errorf("inspect indexes for %s: %w", relation.name, err)
	}
	defer rows.Close()
	found := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("decode indexes for %s: %w", relation.name, err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate indexes for %s: %w", relation.name, err)
	}
	for _, expected := range relation.indexes {
		if !found[expected] {
			return driftf("required index %s on %s is missing", expected, relation.name)
		}
	}
	return nil
}

func relationKind(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((
		SELECT rel.relkind::text FROM pg_catalog.pg_class rel
		JOIN pg_catalog.pg_namespace ns ON ns.oid=rel.relnamespace
		WHERE ns.nspname=current_schema() AND rel.relname=$1
	), '')`, name).Scan(&kind)
	if err != nil {
		return "", fmt.Errorf("inspect relation %s: %w", name, err)
	}
	return kind, nil
}

func migrationRelationsPresent(ctx context.Context, tx *sql.Tx) (bool, error) {
	for _, relation := range schemaRelations {
		kind, err := relationKind(ctx, tx, relation.name)
		if err != nil {
			return false, err
		}
		if kind != "" {
			return true, nil
		}
	}
	return false, nil
}

func rollbackProtectedDataExists(ctx context.Context, tx *sql.Tx) (bool, error) {
	for _, name := range rollbackProtectedRelations {
		kind, err := relationKind(ctx, tx, name)
		if err != nil {
			return false, err
		}
		if kind != "r" && kind != "p" {
			continue
		}
		var exists bool
		query := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s LIMIT 1)`, name)
		if err := tx.QueryRowContext(ctx, query).Scan(&exists); err != nil {
			return false, fmt.Errorf("inspect rollback-protected data in %s: %w", name, err)
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}
