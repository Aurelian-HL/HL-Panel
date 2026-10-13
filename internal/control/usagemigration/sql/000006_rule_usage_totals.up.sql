BEGIN;

-- Block ledger writes only during the one-time backfill. The trigger and
-- initial totals become visible together, so concurrent reports are not lost.
LOCK TABLE usage_events IN SHARE ROW EXCLUSIVE MODE;
CREATE TABLE usage_rule_totals (
    rule_id text NOT NULL,
    period text NOT NULL,
    charged_bytes numeric NOT NULL CHECK (charged_bytes >= 0),
    PRIMARY KEY (rule_id, period),
    CHECK (period = '' OR period ~ '^[0-9]{4}-[0-9]{2}$')
);
INSERT INTO usage_rule_totals(rule_id, period, charged_bytes)
SELECT rule_id, '', sum(charged_bytes) FROM usage_events GROUP BY rule_id
UNION ALL
SELECT rule_id, to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM'), sum(charged_bytes)
FROM usage_events GROUP BY rule_id, to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM');

-- Statement triggers also cover bulk import and maintenance SQL. Retry inserts
-- which affect no rows, and transactions which roll back, cannot double count.
CREATE FUNCTION usage_rule_totals_added() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_rule_totals(rule_id, period, charged_bytes)
    SELECT rule_id, period, sum(charged_bytes) FROM (
        SELECT rule_id, '' AS period, charged_bytes FROM added_events
        UNION ALL
        SELECT rule_id, to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM'), charged_bytes FROM added_events
    ) additions GROUP BY rule_id, period ORDER BY rule_id, period
    ON CONFLICT (rule_id, period) DO UPDATE
    SET charged_bytes = usage_rule_totals.charged_bytes + excluded.charged_bytes;
    RETURN NULL;
END;
$$;
CREATE FUNCTION usage_rule_totals_removed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE usage_rule_totals totals SET charged_bytes = totals.charged_bytes - removed.charged_bytes
    FROM (
        SELECT rule_id, period, sum(charged_bytes) AS charged_bytes FROM (
            SELECT rule_id, '' AS period, charged_bytes FROM removed_events
            UNION ALL
            SELECT rule_id, to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM'), charged_bytes FROM removed_events
        ) removals GROUP BY rule_id, period
    ) removed WHERE totals.rule_id = removed.rule_id AND totals.period = removed.period;
    RETURN NULL;
END;
$$;
CREATE FUNCTION usage_rule_totals_cleared() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM usage_rule_totals;
    RETURN NULL;
END;
$$;
CREATE TRIGGER usage_rule_totals_insert AFTER INSERT ON usage_events
REFERENCING NEW TABLE AS added_events FOR EACH STATEMENT EXECUTE FUNCTION usage_rule_totals_added();
CREATE TRIGGER usage_rule_totals_delete AFTER DELETE ON usage_events
REFERENCING OLD TABLE AS removed_events FOR EACH STATEMENT EXECUTE FUNCTION usage_rule_totals_removed();
-- PostgreSQL fires triggers with the same event alphabetically: subtract the
-- previous rows before adding their replacements, including changed UTC months.
CREATE TRIGGER usage_rule_totals_update_1_remove AFTER UPDATE ON usage_events
REFERENCING OLD TABLE AS removed_events FOR EACH STATEMENT EXECUTE FUNCTION usage_rule_totals_removed();
CREATE TRIGGER usage_rule_totals_update_2_add AFTER UPDATE ON usage_events
REFERENCING NEW TABLE AS added_events FOR EACH STATEMENT EXECUTE FUNCTION usage_rule_totals_added();
CREATE TRIGGER usage_rule_totals_truncate AFTER TRUNCATE ON usage_events
FOR EACH STATEMENT EXECUTE FUNCTION usage_rule_totals_cleared();

COMMIT;
