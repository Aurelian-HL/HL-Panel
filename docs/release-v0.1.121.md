# v0.1.121

- Bundle 257 country and territory flags locally for the probe page, including
  Japan and all ISO 3166-1 alpha-2 entries. Reject unsupported asset codes.
- Recover expired or missing VLESS protocol health leases through the small
  PostgreSQL proof overlay when the durable rule has already been activated.
  Previously the public, health-dependent `Deployed` projection incorrectly
  triggered a full snapshot rewrite during ordinary health recovery.
- Preserve proof configuration hashes, generation, receipt, membership and
  expiry validation. A TCP connection alone still cannot grant protocol health.

Regression coverage includes a synthetic shared group of 42 rules and 28 nodes,
an isolated PostgreSQL recovery test that asserts byte-for-byte snapshot
preservation and no repeated activation audit, and asset coverage for every
bundled flag. No customer URLs, identities or forwarding configuration change.
