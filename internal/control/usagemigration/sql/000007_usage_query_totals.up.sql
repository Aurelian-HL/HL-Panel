BEGIN;

-- Two rows per reporting tuple: lifetime and UTC month. Keeping node_id in
-- the key avoids a single site-wide counter lock on every agent report.
LOCK TABLE usage_events IN SHARE ROW EXCLUSIVE MODE;
CREATE TABLE usage_query_totals (
    node_id text NOT NULL,
    customer_id text NOT NULL,
    rule_id text NOT NULL,
    entry_group_id text NOT NULL,
    exit_group_id text NOT NULL,
    period text NOT NULL,
    event_count numeric NOT NULL CHECK (event_count >= 0),
    rule_actual_bytes numeric NOT NULL CHECK (rule_actual_bytes >= 0),
    customer_actual_bytes numeric NOT NULL CHECK (customer_actual_bytes >= 0),
    charged_bytes numeric NOT NULL CHECK (charged_bytes >= 0),
    PRIMARY KEY (node_id, customer_id, rule_id, entry_group_id, exit_group_id, period),
    CHECK (period = '' OR period ~ '^[0-9]{4}-[0-9]{2}$')
);
CREATE INDEX usage_query_totals_customer ON usage_query_totals(customer_id, period);
CREATE INDEX usage_query_totals_rule ON usage_query_totals(rule_id, period);
CREATE INDEX usage_query_totals_entry ON usage_query_totals(entry_group_id, period);
CREATE INDEX usage_query_totals_exit ON usage_query_totals(exit_group_id, period);
CREATE INDEX usage_query_totals_period ON usage_query_totals(period);

INSERT INTO usage_query_totals
SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id, period,
       count(*), sum(rule_actual_bytes), sum(customer_actual_bytes), sum(charged_bytes)
FROM (
    SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id, '' AS period,
           rule_actual_bytes, customer_actual_bytes, charged_bytes FROM usage_events
    UNION ALL
    SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id,
           to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM'),
           rule_actual_bytes, customer_actual_bytes, charged_bytes FROM usage_events
) events GROUP BY node_id, customer_id, rule_id, entry_group_id, exit_group_id, period;

CREATE FUNCTION usage_query_totals_added() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_query_totals
    SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id, period,
           count(*), sum(rule_actual_bytes), sum(customer_actual_bytes), sum(charged_bytes)
    FROM (
        SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id, '' AS period,
               rule_actual_bytes, customer_actual_bytes, charged_bytes FROM added_events
        UNION ALL
        SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id,
               to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM'),
               rule_actual_bytes, customer_actual_bytes, charged_bytes FROM added_events
    ) events GROUP BY node_id, customer_id, rule_id, entry_group_id, exit_group_id, period
    ORDER BY node_id, customer_id, rule_id, entry_group_id, exit_group_id, period
    ON CONFLICT (node_id, customer_id, rule_id, entry_group_id, exit_group_id, period) DO UPDATE
    SET event_count = usage_query_totals.event_count + excluded.event_count,
        rule_actual_bytes = usage_query_totals.rule_actual_bytes + excluded.rule_actual_bytes,
        customer_actual_bytes = usage_query_totals.customer_actual_bytes + excluded.customer_actual_bytes,
        charged_bytes = usage_query_totals.charged_bytes + excluded.charged_bytes;
    RETURN NULL;
END;
$$;
CREATE FUNCTION usage_query_totals_removed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE removed record;
BEGIN
    FOR removed IN
        SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id, period,
               count(*) AS event_count, sum(rule_actual_bytes) AS rule_actual_bytes,
               sum(customer_actual_bytes) AS customer_actual_bytes, sum(charged_bytes) AS charged_bytes
        FROM (
            SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id, '' AS period,
                   rule_actual_bytes, customer_actual_bytes, charged_bytes FROM removed_events
            UNION ALL
            SELECT node_id, customer_id, rule_id, entry_group_id, exit_group_id,
                   to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM'),
                   rule_actual_bytes, customer_actual_bytes, charged_bytes FROM removed_events
        ) events GROUP BY node_id, customer_id, rule_id, entry_group_id, exit_group_id, period
        ORDER BY node_id, customer_id, rule_id, entry_group_id, exit_group_id, period
    LOOP
        UPDATE usage_query_totals t
        SET event_count = t.event_count - removed.event_count,
            rule_actual_bytes = t.rule_actual_bytes - removed.rule_actual_bytes,
            customer_actual_bytes = t.customer_actual_bytes - removed.customer_actual_bytes,
            charged_bytes = t.charged_bytes - removed.charged_bytes
        WHERE t.node_id=removed.node_id AND t.customer_id=removed.customer_id AND t.rule_id=removed.rule_id
          AND t.entry_group_id=removed.entry_group_id AND t.exit_group_id=removed.exit_group_id AND t.period=removed.period;
    END LOOP;
    RETURN NULL;
END;
$$;
CREATE FUNCTION usage_query_totals_cleared() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM usage_query_totals;
    RETURN NULL;
END;
$$;
CREATE TRIGGER usage_query_totals_insert AFTER INSERT ON usage_events
REFERENCING NEW TABLE AS added_events FOR EACH STATEMENT EXECUTE FUNCTION usage_query_totals_added();
CREATE TRIGGER usage_query_totals_delete AFTER DELETE ON usage_events
REFERENCING OLD TABLE AS removed_events FOR EACH STATEMENT EXECUTE FUNCTION usage_query_totals_removed();
CREATE TRIGGER usage_query_totals_update_1_remove AFTER UPDATE ON usage_events
REFERENCING OLD TABLE AS removed_events FOR EACH STATEMENT EXECUTE FUNCTION usage_query_totals_removed();
CREATE TRIGGER usage_query_totals_update_2_add AFTER UPDATE ON usage_events
REFERENCING NEW TABLE AS added_events FOR EACH STATEMENT EXECUTE FUNCTION usage_query_totals_added();
CREATE TRIGGER usage_query_totals_truncate AFTER TRUNCATE ON usage_events
FOR EACH STATEMENT EXECUTE FUNCTION usage_query_totals_cleared();

COMMIT;
