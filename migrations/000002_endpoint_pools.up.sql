BEGIN;

-- An endpoint pool is the server-side candidate set behind one stable customer
-- endpoint. It is intentionally separate from subscriptions: customers never
-- receive this member list.
CREATE TABLE endpoint_pools (
    id               text PRIMARY KEY,
    name             text NOT NULL,
    group_id         text NOT NULL REFERENCES device_groups(id) ON DELETE RESTRICT,
    mode             text NOT NULL,
    protocol         text NOT NULL,
    hostname         text NOT NULL,
    port             integer NOT NULL,
    selection_policy text NOT NULL,
    created_by       text NOT NULL REFERENCES administrators(id),
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    CONSTRAINT endpoint_pools_name_length CHECK (char_length(name) BETWEEN 1 AND 128),
    CONSTRAINT endpoint_pools_mode_valid CHECK (mode = 'SINGLE_SERVICE_ENDPOINT'),
    CONSTRAINT endpoint_pools_protocol_valid CHECK (protocol IN ('vless', 'tcp', 'socks5')),
    CONSTRAINT endpoint_pools_hostname_length CHECK (char_length(hostname) BETWEEN 1 AND 253),
    CONSTRAINT endpoint_pools_port_range CHECK (port BETWEEN 1 AND 65535),
    CONSTRAINT endpoint_pools_selection_policy_valid CHECK (
        selection_policy IN ('weighted_round_robin', 'weighted_least_connections', 'rendezvous_hash')
    ),
    CONSTRAINT endpoint_pools_name_per_group_uq UNIQUE (group_id, name),
    CONSTRAINT endpoint_pools_id_group_uq UNIQUE (id, group_id),
    CONSTRAINT endpoint_pools_stable_endpoint_uq UNIQUE (hostname, port, protocol)
);

CREATE TABLE endpoint_pool_members (
    pool_id             text NOT NULL REFERENCES endpoint_pools(id) ON DELETE CASCADE,
    group_id            text NOT NULL,
    node_id             text NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    weight              integer NOT NULL,
    priority            integer NOT NULL DEFAULT 0,
    routing_state       text NOT NULL DEFAULT 'eligible',
    last_health_at      timestamptz,
    last_health_reason  text NOT NULL DEFAULT '',
    active_connections  bigint NOT NULL DEFAULT 0,
    updated_at          timestamptz NOT NULL,
    PRIMARY KEY (pool_id, node_id),
    CONSTRAINT endpoint_pool_members_pool_group_fk FOREIGN KEY (pool_id, group_id)
        REFERENCES endpoint_pools(id, group_id) ON DELETE CASCADE,
    CONSTRAINT endpoint_pool_members_group_node_fk FOREIGN KEY (group_id, node_id)
        REFERENCES device_group_members(group_id, node_id) ON DELETE RESTRICT,
    CONSTRAINT endpoint_pool_members_weight_range CHECK (weight BETWEEN 1 AND 1000),
    CONSTRAINT endpoint_pool_members_priority_range CHECK (priority BETWEEN 0 AND 1000),
    CONSTRAINT endpoint_pool_members_state_valid CHECK (
        routing_state IN ('eligible', 'draining', 'quarantined', 'disabled')
    ),
    CONSTRAINT endpoint_pool_members_connections_nonnegative CHECK (active_connections >= 0),
    CONSTRAINT endpoint_pool_members_health_reason_length CHECK (char_length(last_health_reason) <= 1024)
);

CREATE INDEX endpoint_pool_members_candidates_idx
    ON endpoint_pool_members (pool_id, routing_state, priority, weight)
    WHERE routing_state = 'eligible';

COMMIT;
