# v0.1.137

This release collects the control-plane and agent performance work completed
after v0.1.136. It reduces repeated work during rule compilation, configuration
delivery, receipt persistence, health renewal and administrative reads.

- Deliver durable apply receipts through one signaled background worker. A
  successful commit/verify pair can use one authenticated, atomic transaction;
  older panels and agents retain the single-receipt protocol. Failed delivery
  preserves the durable queue and bounded retry backoff.
- Read deployment and desired configuration inputs without loading unrelated
  runtime overlays. Reuse detached, revision-checked read projections and batch
  accepted health-overlay writes in the same database transaction.
- Avoid repeated full-rule scans, identity/endpoint projections and canonical
  JSON decoding during bundle compilation. Reuse pure Reality key-pair checks
  only inside one locked multi-node compilation; authorization, revocation,
  native configuration validation and rollback checks remain authoritative.
- Bind health evidence to the rule fingerprint so unrelated configuration
  changes do not force every rule to repeat its challenge. Cached challenges
  retain their actual observation time and cannot renew a health lease.

There are no database schema changes or changes to customer credentials,
subscription URLs, Reality settings, forwarding protocols or engine pins.
The existing server Xray pin remains unchanged; the release pipeline separately
checks compatibility with the pinned v25.10.15 legacy client.

Upgrade the panel first to enable the control-plane improvements. Agent-side
receipt delivery improvements take effect after updating that node. Older
agents remain supported; publishing this release does not update live machines.
Node updates can restart the agent and its engines and interrupt active
connections. No connection-preserving upgrade guarantee is made.

Synthetic multi-node compilation (500 independent rules, three nodes, eight
alternating runs) improved from a median 148.66 ms to 103.60 ms for the final
key-comparison optimization. The single-node case showed no gain. These are
local compilation measurements, not end-to-end delivery or production CPU
measurements. See control-dispatch-optimization-batch-20261022.md for all
nineteen changes, measurements, regressions and acceptance boundaries.

The release workflow gates publication on Go tests, race checks, vet, both web
builds, real loopback engine tests, fresh installations, Nginx compatibility,
preserved upgrades, node installation, migrations and web upgrade/rollback.
Published assets are hl-panel-linux-amd64.tar.gz and its SHA256 checksum.
