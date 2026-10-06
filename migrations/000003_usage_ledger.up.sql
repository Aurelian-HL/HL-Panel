BEGIN;

-- Usage is deliberately stored outside nyvp_control_snapshots: high-volume,
-- append-only accounting data must never consume the bounded 16 MiB snapshot.
-- Customer/rule/group identifiers are not foreign keys during the snapshot to
-- relational transition; authenticated services validate them before ingest.
CREATE TABLE usage_ingest_cursors (
    node_id       text NOT NULL,
    boot_id       text NOT NULL,
    last_sequence bigint NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL,
    PRIMARY KEY (node_id, boot_id),
    CONSTRAINT usage_cursor_node_length CHECK (char_length(node_id) BETWEEN 1 AND 128),
    CONSTRAINT usage_cursor_boot_length CHECK (char_length(boot_id) BETWEEN 1 AND 128),
    CONSTRAINT usage_cursor_sequence_nonnegative CHECK (last_sequence >= 0)
);

CREATE TABLE usage_events (
    node_id                 text NOT NULL,
    boot_id                 text NOT NULL,
    sequence                bigint NOT NULL,
    payload_sha256          text NOT NULL,
    customer_id             text NOT NULL,
    rule_id                 text NOT NULL,
    entry_group_id          text NOT NULL,
    exit_group_id           text NOT NULL DEFAULT '',
    protocol                text NOT NULL,
    occurred_at             timestamptz NOT NULL,
    period_started_at       timestamptz NOT NULL,
    period_ended_at         timestamptz NOT NULL,
    rule_actual_bytes       bigint NOT NULL,
    customer_actual_bytes   bigint NOT NULL,
    charged_bytes           bigint NOT NULL,
    entry_multiplier_micros bigint NOT NULL,
    exit_multiplier_micros  bigint NOT NULL,
    received_at             timestamptz NOT NULL,
    PRIMARY KEY (node_id, boot_id, sequence),
    CONSTRAINT usage_event_cursor_fk FOREIGN KEY (node_id, boot_id)
        REFERENCES usage_ingest_cursors(node_id, boot_id) ON DELETE RESTRICT,
    CONSTRAINT usage_event_sequence_positive CHECK (sequence > 0),
    CONSTRAINT usage_event_payload_sha256 CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT usage_event_protocol_valid CHECK (protocol IN ('tcp', 'udp')),
    CONSTRAINT usage_event_period_order CHECK (period_ended_at > period_started_at),
    CONSTRAINT usage_event_bytes_nonnegative CHECK (
        rule_actual_bytes >= 0 AND customer_actual_bytes >= 0 AND charged_bytes >= 0
    ),
    CONSTRAINT usage_event_entry_multiplier_range CHECK (
        entry_multiplier_micros BETWEEN 1 AND 1000000000
    ),
    CONSTRAINT usage_event_exit_multiplier_range CHECK (
        exit_multiplier_micros BETWEEN 1 AND 1000000000
    )
);

CREATE INDEX usage_events_customer_time_idx ON usage_events (customer_id, occurred_at DESC);
CREATE INDEX usage_events_rule_time_idx ON usage_events (rule_id, occurred_at DESC);
CREATE INDEX usage_events_entry_group_time_idx ON usage_events (entry_group_id, occurred_at DESC);
CREATE INDEX usage_events_exit_group_time_idx ON usage_events (exit_group_id, occurred_at DESC)
    WHERE exit_group_id <> '';
CREATE INDEX usage_events_occurred_idx ON usage_events (occurred_at DESC);

CREATE TABLE usage_customer_totals (
    customer_id            text PRIMARY KEY,
    actual_bytes           bigint NOT NULL,
    charged_bytes          bigint NOT NULL,
    last_usage_occurred_at timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    CONSTRAINT usage_customer_totals_nonnegative CHECK (actual_bytes >= 0 AND charged_bytes >= 0)
);

CREATE TABLE usage_enforcement_decisions (
    id                       text PRIMARY KEY,
    customer_id              text NOT NULL,
    rule_id                  text NOT NULL,
    protocol                 text NOT NULL,
    reason                   text NOT NULL,
    action                   text NOT NULL,
    status                   text NOT NULL DEFAULT 'pending',
    trigger_node_id          text NOT NULL,
    trigger_boot_id          text NOT NULL,
    trigger_sequence         bigint NOT NULL,
    customer_charged_bytes   bigint NOT NULL,
    traffic_limit_bytes      bigint NOT NULL,
    created_at               timestamptz NOT NULL,
    updated_at               timestamptz NOT NULL,
    revision                 bigint NOT NULL DEFAULT 1,
    last_error               text NOT NULL DEFAULT '',
    revoke_administrator_id  text,
    revoke_idempotency_key   text,
    revoke_request_sha256    text,
    revoke_requested_at      timestamptz,
    CONSTRAINT usage_decision_event_fk FOREIGN KEY (trigger_node_id, trigger_boot_id, trigger_sequence)
        REFERENCES usage_events(node_id, boot_id, sequence) ON DELETE RESTRICT,
    CONSTRAINT usage_decision_event_uq UNIQUE (trigger_node_id, trigger_boot_id, trigger_sequence),
    CONSTRAINT usage_decision_reason_valid CHECK (
        reason IN ('customer_disabled', 'customer_expired', 'quota_exhausted')
    ),
    CONSTRAINT usage_decision_protocol_valid CHECK (protocol IN ('tcp', 'udp')),
    CONSTRAINT usage_decision_action_valid CHECK (
        action IN ('disable_customer_access', 'enable_customer_access')
    ),
    CONSTRAINT usage_decision_status_valid CHECK (
        status IN ('pending', 'applied', 'apply_failed', 'revoke_pending', 'revoked', 'revoke_failed')
    ),
    CONSTRAINT usage_decision_totals_nonnegative CHECK (
        customer_charged_bytes >= 0 AND traffic_limit_bytes >= 0
    ),
    CONSTRAINT usage_decision_revision_positive CHECK (revision > 0),
    CONSTRAINT usage_decision_last_error_bounded CHECK (octet_length(last_error) <= 512),
    CONSTRAINT usage_decision_revoke_admin_length CHECK (
        revoke_administrator_id IS NULL OR char_length(revoke_administrator_id) BETWEEN 1 AND 128
    ),
    CONSTRAINT usage_decision_revoke_key_length CHECK (
        revoke_idempotency_key IS NULL OR char_length(revoke_idempotency_key) BETWEEN 1 AND 128
    ),
    CONSTRAINT usage_decision_revoke_metadata_complete CHECK (
        (revoke_administrator_id IS NULL AND revoke_idempotency_key IS NULL AND revoke_request_sha256 IS NULL AND revoke_requested_at IS NULL)
        OR
        (revoke_administrator_id IS NOT NULL AND revoke_idempotency_key IS NOT NULL AND revoke_request_sha256 ~ '^[0-9a-f]{64}$' AND revoke_requested_at IS NOT NULL)
    )
);

CREATE INDEX usage_enforcement_pending_idx
    ON usage_enforcement_decisions (status, created_at DESC, customer_id);

CREATE INDEX usage_enforcement_node_commands_idx
    ON usage_enforcement_decisions (trigger_node_id, created_at, id)
    WHERE status IN ('pending', 'revoke_pending');

CREATE TABLE usage_enforcement_results (
    decision_id       text NOT NULL REFERENCES usage_enforcement_decisions(id) ON DELETE RESTRICT,
    command_revision  bigint NOT NULL,
    node_id           text NOT NULL,
    action            text NOT NULL,
    result_status     text NOT NULL,
    payload_sha256    text NOT NULL,
    sanitized_message text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL,
    PRIMARY KEY (decision_id, command_revision),
    CONSTRAINT usage_enforcement_result_revision_positive CHECK (command_revision > 0),
    CONSTRAINT usage_enforcement_result_action_valid CHECK (
        action IN ('disable_customer_access', 'enable_customer_access')
    ),
    CONSTRAINT usage_enforcement_result_status_valid CHECK (result_status IN ('succeeded', 'failed')),
    CONSTRAINT usage_enforcement_result_payload_sha256 CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT usage_enforcement_result_message_bounded CHECK (octet_length(sanitized_message) <= 512)
);

CREATE INDEX usage_enforcement_results_created_idx
    ON usage_enforcement_results (created_at DESC, decision_id);

COMMIT;
