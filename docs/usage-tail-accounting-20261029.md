# Usage accounting after rule removal

## Defect and scope

After a rule is paused or removed, an engine can still expose its final counters.
Older agents report these bytes with the newly applied configuration generation.
Rejecting the missing fragment blocks the persistent ordered usage journal,
including later reports for unrelated rules.

This release resolves an absent fragment from the latest earlier matching
configuration for the same node. Both the removal generation and the historical
rule require genuine same-attempt commit and verify receipts and valid hashes.
Current malformed fragments, invalidated history, mismatched ownership, unknown
engine modes and missing receipts remain rejected. Selection never skips an
invalid matching candidate in favor of an older valid one.

The PostgreSQL implementation reads hot and archived evidence in the same
repeatable-read transaction; hot generations take precedence over archived copies.
It does not introduce a database schema migration.

The 30-second recovery reconciliation now also covers unlimited rules, restoring
the last display update after throttling or a crash. Unchanged totals do not write.
The agent preserves the removal generation for schema-1 empty bundles, including
the final-rule removal case. Acknowledgements and persistent ordering are retained.

This change does not alter forwarding rules, subscriptions, connection identities,
engine versions, or engine apply/restart behavior. Xray remains pinned to 26.7.28.

## Isolated regression coverage

- Removed-rule and final-rule tails with legacy and encoded Xray tags.
- Two-node PostgreSQL accounting: 293819 tail bytes, followed by 4100 new-rule bytes.
- Byte-exact ingestion, persisted replay and conflicting duplicate rejection.
- Receipt, hash, engine, time, node, ownership and metadata validation failures.
- Hot/archive precedence and rejection of a damaged latest matching history.
- Agent journal persistence and removal only after a matching acknowledgement.
- Unlimited-rule final display recovery from 1024 to 2049 bytes.
- Display boundaries from 1 byte through fractional KB; storage remains integer bytes.

Relevant package tests, PostgreSQL integration tests, Go vet, formatter tests and
git diff checks passed in the development checkout. The isolated release checkout
and exact commit must pass the official release CI matrix before publication.
Production recovery must be checked separately after the system update.
