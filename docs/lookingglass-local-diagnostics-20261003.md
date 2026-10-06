# Local LookingGlass diagnostics

This is a bounded, administrator-only diagnostic on the **control-plane host**.
It is not NY's cross-node LookingGlass and does not run on edge nodes.

`CONTROL_LOOKINGGLASS_TARGETS` is an optional comma-separated allow-list of
`label|public-hostname-or-IP`. It is empty by default. Labels are returned to
the browser; addresses are not. Up to 20 fixed targets can be configured.
For example: `Resolver|one.one.one.one`.

The API is `GET /api/v1/diagnostics/targets` and
`POST /api/v1/diagnostics/run` with `{"target_id":"target-1","action":"ping"}`
or `"dns"` for a hostname. Both require an administrator bearer session.
The client cannot submit an address or command. DNS results are checked for
non-public addresses before Ping uses one pinned IP. Ping executes one packet
with fixed arguments, a four-second deadline, two concurrent slots, and a
two-second global cooldown. No command output or resolved address is returned.
Requests and outcomes are audited; execution stops if the initial audit write
fails. The implementation makes no changes to network routes, DNS, or nodes.

Remaining NY parity work: authorized node tree, secure agent task transport,
per-node diagnosis, site policy switch, and end-to-end browser validation.
None of those are claimed by this local-only feature.
