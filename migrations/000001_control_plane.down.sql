BEGIN;

DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS node_apply_results;
DROP TABLE IF EXISTS node_config_generation_revisions;
DROP TABLE IF EXISTS node_config_generations;
DROP TABLE IF EXISTS group_revisions;
DROP TABLE IF EXISTS device_group_members;
DROP TABLE IF EXISTS device_groups;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS enrollment_tokens;
DROP TABLE IF EXISTS administrator_sessions;
DROP TABLE IF EXISTS administrators;

COMMIT;
