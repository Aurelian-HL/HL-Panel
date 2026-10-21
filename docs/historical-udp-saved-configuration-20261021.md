# Historical UDP accounting and saved forwarding configuration

This is a local control-api candidate contract dated 2026-10-21. It is not a
formal release, a production deployment, an Agent upgrade, or an engine upgrade.
The unpublished candidate reports platform version `v0.1.127`; identify its
contents by the full Git commit and the binary SHA256 in its delivery manifest.

## Historical UDP usage

Generation-qualified usage metadata accepts the explicit protocols `tcp` and
`udp`. UDP still requires the original node/generation, configuration hash,
matching apply attempt, successful commit and verify receipts, a real supported
engine mode, unique matching fragment, valid ownership metadata and multipliers,
and a matching native counter. It does not require newly introduced keepalive
fields to be present in an older verified configuration.

GOST counters require a unique service with both handler and listener matching
the historical protocol and boolean `metadata.enableStats=true`. Xray native
counter metadata must match the fragment metadata through the existing tag
validation. The unversioned legacy path continues to permit only its existing
TCP eligibility; this change does not authorize unversioned UDP reports.

The ordinary usage service and PostgreSQL ledger remain responsible for
idempotency and billing. Delayed reports for a paused rule or deleted current
customer still require immutable historical evidence. Existing ACK, queue,
journal and configuration-application behavior is unchanged. No queue is cleared
and no ACK or validation is bypassed.

## Saved administrator configuration endpoint

`GET /api/v1/forwarding-rules/{id}/configuration` requires an authenticated
administrator with management rights for the requested rule. It returns one
saved VLESS client configuration from one authoritative repository operation.
The production PostgreSQL implementation uses a read-only repeatable-read
transaction, validates the authoritative snapshot identity including `xmin`,
and reads availability overlays in that transaction. A database error, invalid
snapshot or failed commit prevents a credential response. There is no fallback
to independent rule, pool and identity transactions.

The rule must have one active, valid binding, an authorized related pool,
matching customer/subject, valid credential, and complete valid Reality client
material. Ambiguous bindings, foreign ownership, revoked credentials or invalid
relationships fail closed. The URI includes the existing client UUID. It must
be treated as a secret and must never be written to logs or list endpoints.
Upstream SOCKS5 credentials and Reality private keys are not returned.

The response contains `uri`, `name`, `endpoint`, `rule_id`, `customer_id`,
`binding_id`, `endpoint_pool_id`, `rule_revision`, `binding_revision`, `paused`,
`status`, `rule_status`, `service_ready` and `live_readiness_checked`.

| Field | Contract |
| --- | --- |
| `status=paused` | The saved rule is paused. |
| `status=configured` | The rule status is active or pending activation; this is not a health assertion. |
| `status=inactive` | The saved rule is otherwise inactive, including availability limits. |
| `service_ready=false` | Always false for this saved configuration operation. |
| `live_readiness_checked=false` | Always false; this endpoint does not grant live readiness. |

No request provisions an identity, rotates a UUID, resumes a rule, or writes a
snapshot, revision, audit mutation or desired node generation. Current changes
to SNI are read from the authority while the saved UUID and endpoint stay
unchanged. Existing `/connection` routes continue to enforce their live gates.

Successful responses use `Cache-Control: no-store`,
`Referrer-Policy: no-referrer`, and `X-Robots-Tag: noindex, nofollow`.
Unauthenticated or invalid sessions return 401; a missing or unmanaged rule
returns 404; inconsistent saved materials or a missing authoritative source
return 409. Internal persistence errors return the existing sanitized 500.
This operation does not create a public subscription or modify HT behavior.

## Verification and deployment acceptance

The candidate's verification report and original logs are delivered beside its
binary. Isolated PostgreSQL tests use a newly initialized loopback cluster and
synthetic schemas only. Required cases include:

- GOST/Xray historical UDP, each with legacy and managed reports: 64 persisted
  sequences, exact rounded charges, new-service replay without historical-source
  availability, no duplicate billing, and rejection of changed replay bytes.
- HTTP authorization and privacy, stable paused URI, and unchanged encoded state.
- 121 synthetic paused expired rules remaining paused and unchanged after reads.
- PostgreSQL cache invalidation at the same application revision, SNI refresh,
  foreign-admin denial, revocation, corrupt authority, and unavailable database.

The 121-rule case is synthetic; it is not a production bulk read. The native
fixtures reproduce historical structures but do not replay the unavailable full
production native bundle. Local tests do not establish production journal drain,
desired/applied convergence, WAN behavior, or uninterrupted customer connections.

After independent deployment approval, HT must verify usage ACK progress,
natural journal consumption, desired/applied convergence, and the authenticated
configuration endpoint. Keep all 121 expired production rules paused. Record
the old binary checksum and retain the encrypted consistent backup before any
rollout. This control-only change contains no schema or data migration; binary
rollback does not undo legitimate ledger entries already acknowledged.

## Transient rule list error

Administrator list and single-rule reads use the same `readForwarding`
snapshot/traffic/health overlay path and do not directly invoke the historical
usage metadata provider. HT reported a transient list HTTP 500 and then recovery
to 200; the first problem body was not retained. Its cause remains unconfirmed.
This candidate does not add a speculative fix. The current generic error body
has `code` and `message`, not `request_id`; panic logs expose only the fixed
message, method and sanitized path. Do not collect raw snapshots, cookies,
subscription URIs or unfiltered application logs for this diagnosis.
