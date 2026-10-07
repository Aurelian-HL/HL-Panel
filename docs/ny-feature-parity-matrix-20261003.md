# NY clean-room feature parity matrix

Date: 2026-10-03

This matrix is the implementation and acceptance contract for the independent
platform. It records observable NY workflows without copying private source.
An item is complete only when its UI, authorized API, durable state, audit
record, runtime effect where applicable, and browser/client verification pass.

## Explicitly excluded product domains

The following domains are intentionally absent from navigation, routes,
permissions, storage, dashboards, and customer profile actions:

- Shop
- Push channels and Telegram binding
- Order management and customer order history
- Plan management and customer plan/renewal controls
- Redemption codes
- Invitation records, invitation rebates, and invitation-code flows

Their dependent balance recharge, renewal-price, commission, and payment cards
are excluded as well. This prevents dead buttons that depend on a removed
commercial or notification subsystem.

## Required routes and workflows

| Area | Required pages and actions | Durable/runtime acceptance | Current status |
| --- | --- | --- | --- |
| Public and authentication | Home, login, optional registration policy, token login, logout | Rate-limited sessions; settings-driven registration; no secret echo | Admin login exists; public/customer flows incomplete |
| Home | Welcome, actual platform version, documentation link, active announcements, site/backend detail | Reads the same settings and announcements stored by admin; no fake NY license/version | In implementation |
| Customer profile | Identity, role, user group, expiry, traffic, maximum rules, speed/IP/connection limits, password reset | Customer can read/update only their own permitted data; password hashes never leave server | In implementation |
| Forwarding rules | List, search, traffic summary, create/edit/copy/delete, pause/resume, diagnostics, advanced editor | Separate sync and paused states; each operation audited and published to affected nodes | Basic create/edit/pause model exists; execution incomplete |
| Rule groups | All/ungrouped counts, create/rename/archive, move selected rules | Archived groups return rules to ungrouped; atomic idempotent batch operations | In implementation |
| Rule batch tools | Import/export, move group, pause/resume, reset traffic, delete, change entry/exit and limits | Preview, all-or-nothing commit, conflict report, one consolidated publish | In implementation; import/export pending |
| User administration | UID, username, expiry, traffic, user group, max rules, administrator flag, banned state, note | Create/edit/reset password/delete with reference checks and audit | Partial fields and CRUD only |
| User groups | Create/edit/reorder/delete, user count, resource authorization | Group zero cannot forward; referenced groups cannot be silently removed | Basic authorization exists |
| Device groups | Entry, exit, monitor-only, customer-owned exit, chain; sort/folders; traffic; online nodes | Type-specific validation, token rotation, member lifecycle, group revisions | Basic entry/exit only |
| Device-group policy | Group IDs, multiplier, allowed user groups, entry allowed exits, exit allowed entries, direct policy, fallback, visibility, notes | Four-layer resource authorization is enforced during rule create and publish | Direct policy and allowed exits partial |
| Node enrollment | Online/offline installer, offline package, token rotation, config preview | Signed enrollment, least privilege credential, versioned desired/apply state | One-time enrollment exists; installer incomplete |
| Node status | Group filter, online count, address/region, traffic/speed, CPU/load/RAM/disk, connections, uptime, version, sync | Authenticated telemetry with expiry and protocol health; upgrade/delete/weight changes audited | Heartbeat inventory is partial |
| Restricted operations | Diagnostics and controlled upgrades | Fixed allow-list tasks, no arbitrary shell, full audit and timeout | Required replacement for NY WebSSH |
| LookingGlass | Removed from the current HL-panel scope | No public route, API, configuration, or diagnostic task remains | Removed by product decision |
| Customer-owned exit | Customer-scoped single-end device group and onboarding | Enabled only by site policy and user permission; cannot affect other customers | Missing |
| Traffic statistics | Standalone statistics page and range queries are removed; forwarding rules retain rule-level usage and quota enforcement | Preserve the underlying usage ledger and limit enforcement without a separate statistics surface | Standalone UI removed by product decision |
| Limits | Customer/rule speed, IP and connection limits; expiry and quota | Per-entry enforcement; user/rule limits combine; UDP limitation visible; exhaustion withdraws service | Stored fields only, no enforcement |
| Site settings | Site name, registration policy, challenge policy, customer-owned exit, theme, desktop/mobile background, notice | Validated settings affect actual public/customer/admin views | In implementation |
| Dashboard | Today/yesterday traffic, users, online devices, rule sync and traffic queue, user/node rankings | Definitions and collection timestamps visible; no excluded commercial metrics | Basic node counts only |
| Import/migration | Legacy text and versioned JSON preview/import/export, NY snapshot mapping and rollback | Read-only source, schema evidence, idempotent commit, reconciliation and rollback | Structure preflight only |

## Rule contract

A normal rule form keeps NY's operator order:

1. Name and owner.
2. Required entry group and displayed entry connection information.
3. Listener port, where zero requests atomic allocation.
4. Egress choice: direct or an authorized exit group.
5. One or more host/port targets.
6. Advanced target selection: random, round robin, IP hash, least load, or failover.
7. Proxy Protocol receive/send mode.
8. Rule-level speed, IP, and connection limits.
9. TLS inbound and SNI parent/child settings where supported.

Status is two-dimensional. Sync state is `unsynced`, `normal`, `failed`, or
`disabled`; operator pause is independent. A paused rule must not become active
just because its last sync state was normal.

## Device-group and path contract

Every rule selects an entry group. Direct means the selected entry member
connects to the target without another exit group. Exit-group mode means entry,
transit, then an eligible exit member. Saving two group IDs is not proof of a
working transit line.

Entry authorization combines customer group permission, entry allowed exits,
exit allowed entries, and the entry direct policy. Fallback groups need the
same customer-group authorization. Chain mode contains two or three validated
hops, rejects loops and duplicates, and owns its permission/multiplier policy.

VLESS is an ingress protocol in this same workflow. One customer gets one
stable URI. Device-group members are internal candidates selected for each new
connection; the platform never expands a ten-member group into ten customer
URIs.

## Runtime acceptance

- Compile structured rules into complete per-node desired configurations.
- Validate with the native Xray or GOST parser before publication.
- Apply atomically and retain last-known-good configuration on failure.
- Report per-rule and per-node activation results without secrets.
- Verify TCP direct, TCP via exit, UDP where supported, VLESS TLS/Vision and
  Reality separately in isolated machines.
- Stop one group member and verify new connections move while existing
  connections require reconnect.
- Revoke a customer and verify every group member rejects new authentication.
- Never use `zf.hongle.work`, `kl.hongle.cc`, or `ht.hongle.work` for mutation,
  load testing, failure injection, or deployment verification.

## Evidence limitations

The current menu and fields were derived from the current public NY static
bundle, official documentation, and prior authorized read-only observations.
The private tunnel implementation, complete JSON export schema, full error-code
set, and every dynamic default remain unknown. Equivalent behavior must be
implemented from public contracts and isolated tests; unknown private behavior
must be reported rather than invented.

NY exposes administrator WebSSH, but this repository forbids arbitrary remote
shell execution. The accepted equivalent is a bounded, allow-listed,
auditable diagnostics and upgrade task system. This is a deliberate security
difference and must remain visible in release notes.
