BEGIN;

DROP TABLE IF EXISTS usage_enforcement_results;
DROP TABLE IF EXISTS usage_enforcement_decisions;
DROP TABLE IF EXISTS usage_customer_totals;
DROP TABLE IF EXISTS usage_events;
DROP TABLE IF EXISTS usage_ingest_cursors;

COMMIT;
