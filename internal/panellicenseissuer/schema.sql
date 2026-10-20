CREATE SCHEMA IF NOT EXISTS hl_license_v2;
CREATE TABLE IF NOT EXISTS hl_license_v2.licenses (
 id text PRIMARY KEY,
 domain text NOT NULL UNIQUE,
 code_hash text NOT NULL UNIQUE,
 document jsonb NOT NULL
);
-- Retain former domains after explicit rebinding: a domain never gains a second license.
CREATE TABLE IF NOT EXISTS hl_license_v2.domains (
 domain text PRIMARY KEY,
 license_id text NOT NULL
);
CREATE TABLE IF NOT EXISTS hl_license_v2.reservations (
 id text PRIMARY KEY,
 license_id text NOT NULL REFERENCES hl_license_v2.licenses(id),
 instance_id text NOT NULL,
 public_key text NOT NULL,
 expires_at timestamptz NOT NULL,
 document jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS reservations_identity ON hl_license_v2.reservations(license_id,instance_id,public_key);
CREATE TABLE IF NOT EXISTS hl_license_v2.operations (
 key text PRIMARY KEY,
 digest text NOT NULL,
 result bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS hl_license_v2.audit (
 id text PRIMARY KEY,
 license_id text NOT NULL REFERENCES hl_license_v2.licenses(id),
 at timestamptz NOT NULL,
 document jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_license_time ON hl_license_v2.audit(license_id, at, id);
