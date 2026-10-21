# Control dispatch performance update

This control-plane update keeps existing subscription addresses, identities,
Reality parameters, durable accounting, and exact node acknowledgements.
It does not require an Xray or node-engine upgrade.

## Changes

- Usage-subject validation and desired-node configuration use detached read
  projections with PostgreSQL format, revision, and transaction-version checks.
  Database failures do not turn into successful stale-cache responses.
- The HTTP API and periodic VLESS reconciler share one setup service. Two
  immediate workers continuously drain a bounded queue of 256 waiting rules.
  Queued saves for the same rule and administrator are coalesced. Overflow
  wakes durable reconciliation without creating an unbounded goroutine backlog.
- Workers reload committed rules before setup so pauses, edits, and deletions
  take precedence over a queued save. The 15-second recovery scan remains.
- Saved connection reads retain authorization checks; rule deletion retires
  owned VLESS material atomically, and listener edits update bound endpoints.
  Historical verified UDP usage remains readable.

## Local validation

On 2026-10-22, the merged tree passed `go test ./...` (2024 tests passed,
29 optional tests skipped), `go vet ./...`, the VLESS scheduler race checks,
web type checking, 359 frontend tests, the frontend build, and whitespace checks.
PostgreSQL tests used a disposable local database.

In isolated synthetic snapshots of approximately 6.6 MB, 25-sample mean
usage-subject reads changed from 47.253244 ms to 0.143040 ms; desired reads
changed from 44.866448 ms to 0.323104 ms. These are measurements of individual
read paths, not end-to-end deployment speed or a production guarantee.

Formal release CI must additionally pass installation, update, rollback,
migration, and real-engine loopback checks before publication. Production
acceptance must separately verify the installed artifact, existing client
identities, node operation, and real-client connectivity.
