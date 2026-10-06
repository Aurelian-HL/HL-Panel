BEGIN;

CREATE TABLE administrators (
    id              text PRIMARY KEY,
    username        text NOT NULL,
    password_hash   text NOT NULL,
    created_at      timestamptz NOT NULL,
    CONSTRAINT administrators_username_length CHECK (char_length(username) BETWEEN 1 AND 128)
);

CREATE UNIQUE INDEX administrators_username_lower_uq ON administrators (lower(username));

CREATE TABLE administrator_sessions (
    id          text PRIMARY KEY,
    admin_id    text NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
    token_hash  text NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL,
    CONSTRAINT administrator_sessions_token_hash_sha256 CHECK (token_hash ~ '^[0-9a-f]{64}$')
);

CREATE INDEX administrator_sessions_expiry_idx ON administrator_sessions (expires_at);

CREATE TABLE enrollment_tokens (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    token_hash  text NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_by  text NOT NULL REFERENCES administrators(id),
    created_at  timestamptz NOT NULL,
    CONSTRAINT enrollment_tokens_name_length CHECK (char_length(name) BETWEEN 1 AND 128),
    CONSTRAINT enrollment_tokens_hash_sha256 CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT enrollment_tokens_used_after_create CHECK (used_at IS NULL OR used_at >= created_at)
);

CREATE INDEX enrollment_tokens_expiry_idx ON enrollment_tokens (expires_at) WHERE used_at IS NULL;

CREATE TABLE nodes (
    id                      text PRIMARY KEY,
    name                    text NOT NULL,
    hostname                text NOT NULL,
    platform                text NOT NULL,
    architecture            text NOT NULL,
    agent_version           text NOT NULL,
    capabilities            jsonb NOT NULL DEFAULT '[]'::jsonb,
    boot_id                 text NOT NULL DEFAULT '',
    engine_versions         jsonb NOT NULL DEFAULT '{}'::jsonb,
    resources               jsonb NOT NULL DEFAULT '{}'::jsonb,
    credential_hash         text NOT NULL UNIQUE,
    desired_generation      bigint NOT NULL DEFAULT 0,
    applied_generation      bigint NOT NULL DEFAULT 0,
    last_apply_generation   bigint NOT NULL DEFAULT 0,
    last_apply_status       text NOT NULL DEFAULT '',
    last_heartbeat_at       timestamptz,
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL,
    retired_at              timestamptz,
    CONSTRAINT nodes_credential_hash_sha256 CHECK (credential_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT nodes_generation_nonnegative CHECK (
        desired_generation >= 0 AND applied_generation >= 0 AND last_apply_generation >= 0
    ),
    CONSTRAINT nodes_applied_not_ahead CHECK (applied_generation <= desired_generation),
    CONSTRAINT nodes_apply_status_valid CHECK (last_apply_status IN ('', 'succeeded', 'failed', 'rolled_back'))
);

CREATE INDEX nodes_heartbeat_idx ON nodes (last_heartbeat_at) WHERE retired_at IS NULL;

CREATE TABLE device_groups (
    id                  text PRIMARY KEY,
    name                text NOT NULL,
    kind                text NOT NULL,
    selection_policy    text NOT NULL DEFAULT 'weighted_least_connections',
    description         text NOT NULL DEFAULT '',
    current_revision    bigint NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    CONSTRAINT device_groups_name_length CHECK (char_length(name) BETWEEN 1 AND 128),
    CONSTRAINT device_groups_name_unique UNIQUE (name),
    CONSTRAINT device_groups_kind_valid CHECK (kind IN ('ENTRY', 'EXIT', 'EDGE', 'HYBRID')),
    CONSTRAINT device_groups_selection_policy_valid CHECK (
        selection_policy IN ('weighted_round_robin', 'weighted_least_connections', 'rendezvous_hash')
    ),
    CONSTRAINT device_groups_revision_nonnegative CHECK (current_revision >= 0)
);

CREATE TABLE device_group_members (
    group_id       text NOT NULL REFERENCES device_groups(id) ON DELETE CASCADE,
    node_id        text NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    weight         integer NOT NULL,
    priority       integer NOT NULL DEFAULT 0,
    retired_at     timestamptz,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    PRIMARY KEY (group_id, node_id),
    CONSTRAINT device_group_members_weight_range CHECK (weight BETWEEN 1 AND 1000),
    CONSTRAINT device_group_members_priority_range CHECK (priority BETWEEN 0 AND 1000)
);

CREATE INDEX device_group_members_node_idx ON device_group_members (node_id) WHERE retired_at IS NULL;

CREATE TABLE group_revisions (
    id               text PRIMARY KEY,
    group_id         text NOT NULL REFERENCES device_groups(id) ON DELETE CASCADE,
    revision         bigint NOT NULL,
    engine           text NOT NULL,
    config           jsonb NOT NULL,
    config_sha256    text NOT NULL,
    request_sha256   text NOT NULL,
    idempotency_key  text NOT NULL,
    created_by       text NOT NULL REFERENCES administrators(id),
    created_at       timestamptz NOT NULL,
    CONSTRAINT group_revisions_number_positive CHECK (revision > 0),
    CONSTRAINT group_revisions_engine_valid CHECK (engine IN ('xray', 'gost')),
    CONSTRAINT group_revisions_config_hash_sha256 CHECK (config_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT group_revisions_request_hash_sha256 CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT group_revisions_idempotency_length CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    CONSTRAINT group_revisions_group_number_uq UNIQUE (group_id, revision),
    CONSTRAINT group_revisions_group_idempotency_uq UNIQUE (group_id, idempotency_key)
);

CREATE TABLE node_config_generations (
    id               text PRIMARY KEY,
    node_id          text NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    generation       bigint NOT NULL,
    engine           text NOT NULL,
    config           jsonb NOT NULL,
    config_sha256    text NOT NULL,
    created_at       timestamptz NOT NULL,
    CONSTRAINT node_config_generations_number_positive CHECK (generation > 0),
    CONSTRAINT node_config_generations_engine_valid CHECK (engine = 'node-bundle'),
    CONSTRAINT node_config_generations_hash_sha256 CHECK (config_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT node_config_generations_node_number_uq UNIQUE (node_id, generation)
);

CREATE TABLE node_config_generation_revisions (
    node_config_generation_id text NOT NULL REFERENCES node_config_generations(id) ON DELETE CASCADE,
    group_revision_id         text NOT NULL REFERENCES group_revisions(id) ON DELETE RESTRICT,
    PRIMARY KEY (node_config_generation_id, group_revision_id)
);

CREATE INDEX node_config_generation_revisions_group_idx ON node_config_generation_revisions (group_revision_id);

CREATE TABLE node_apply_results (
    id               text PRIMARY KEY,
    node_id          text NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    generation       bigint NOT NULL,
    phase            text NOT NULL,
    result_status    text NOT NULL,
    config_sha256    text NOT NULL,
    sanitized_message text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL,
    CONSTRAINT node_apply_results_generation_positive CHECK (generation > 0),
    CONSTRAINT node_apply_results_phase_valid CHECK (phase IN ('prepare', 'validate', 'commit', 'verify', 'rollback')),
    CONSTRAINT node_apply_results_status_valid CHECK (result_status IN ('succeeded', 'failed', 'rolled_back')),
    CONSTRAINT node_apply_results_hash_sha256 CHECK (config_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT node_apply_results_message_bounded CHECK (char_length(sanitized_message) <= 1024),
    CONSTRAINT node_apply_results_phase_idempotency_uq UNIQUE (node_id, generation, phase),
    CONSTRAINT node_apply_results_generation_fk FOREIGN KEY (node_id, generation)
        REFERENCES node_config_generations(node_id, generation) ON DELETE CASCADE
);

CREATE TABLE audit_events (
    id             text PRIMARY KEY,
    actor_type     text NOT NULL,
    actor_id       text NOT NULL,
    action         text NOT NULL,
    resource_type  text NOT NULL,
    resource_id    text NOT NULL,
    outcome        text NOT NULL,
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at     timestamptz NOT NULL,
    CONSTRAINT audit_events_outcome_valid CHECK (outcome IN ('succeeded', 'failed'))
);

CREATE INDEX audit_events_created_idx ON audit_events (created_at DESC);
CREATE INDEX audit_events_resource_idx ON audit_events (resource_type, resource_id, created_at DESC);

COMMIT;
