# v0.1.145

Customer service dates are reminders. Reaching the date no longer disables
forwarding rules, subscriptions, connection views, or rule creation/resumption.
Dates remain visible and unchanged. Explicit account suspension, paused rules,
traffic quotas, health verification, and domain-license checks retain their
existing behavior.

The bundle compiler revision restores listeners omitted solely because of the
customer reminder date. Historical pending expiry-disable decisions remain in
the audit ledger but are excluded from agent commands. Explicit revocations
remain deliverable, and historical expiry decisions cannot block quota commands.

Client identities, Reality parameters, and subscription URLs are retained.
Xray remains pinned to 26.7.28 with 25.10.15 client compatibility acceptance.
This control-plane change requires no existing Agent or engine binary upgrade.
Normal rule application retains its live-update safety boundary; no whole-engine
restart fallback is added.

Regression checks cover reminder boundaries, TCP/UDP compilation, old persisted
expiry status, public subscriptions, connection identity, explicit restrictions,
and real isolated PostgreSQL command ordering. Publishing remains gated by the
full release workflow and production results are recorded separately.
