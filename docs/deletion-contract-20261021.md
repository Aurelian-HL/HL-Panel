# Deletion compatibility contract

Updated 2026-10-24. Panel login accounts, customer service owners and VLESS
identities are separate resources. Removing a login does not remove a service
owner, forwarding rule or subscription. This contract does not authorize any
production deletion or an automatic retry of a previously interrupted deletion.

## Rule deletion

Use `POST /api/v1/forwarding-rules/batch` with administrator authentication,
an `Idempotency-Key`, and the current revision of every selected rule:

```json
{
  "operation": "delete",
  "rule_ids": ["rule-example"],
  "expected_revisions": {"rule-example": 1}
}
```

An owned active VLESS identity or runtime no longer blocks deletion by itself.
The same transaction revokes those records, clears their credentials and native
configuration, deletes the rule and its endpoint pools, and compiles the updated
desired node configuration. Audit events and revoked history remain available.
The node must still receive and apply the new desired configuration; an HTTP
success alone does not establish that a listener has disappeared on a node.

Foreign references or ownership, invalid revisions, and stale rule revisions
still reject deletion. Validation happens before mutation.
Compilation or database commit failure rolls back the mutation. Deleting one
rule preserves the customer and its other rules. Exact request replay does not
recompile configuration or duplicate business audit events.

The new Agent applies safe handler/service changes without restarting a live
engine, and refuses unsafe changes while preserving that process. Existing
Agents require a separate upgrade; updating only the panel does not change their
behavior. See [live-update acceptance](rule-live-update-acceptance-20261024.md).

`DELETE /api/v1/forwarding-rules/{id}` remains unsupported (`405`).

## Panel login deletion

`DELETE /api/v1/customers/{id}` revokes only an existing panel login and all its
sessions. It retains the service owner, revision, quotas, forwarding rules,
subscriptions, credentials and usage. It emits `customer.delete` and publishes
no node configuration generation. An ID without a panel login returns `404`
unless replaying an already successful request.

## Service owner deletion

```http
DELETE /api/v1/service-owners/{id}
Authorization: Bearer <administrator-session>
Content-Type: application/json
Idempotency-Key: delete-service-owner-example-1
```

```json
{"revision": 1}
```

Read the exact owner's current ID, account kind, revision and references first.
An owner with a panel login returns `409`: revoke that login separately through
`DELETE /api/v1/customers/{id}`, then reread and verify the same owner before
requesting its deletion. A forwarding rule referencing either `customer_id` or
`owner_id`, a subscription referencing either owner field, or a non-revoked
VLESS identity/runtime also returns `409`. Resolve these references explicitly;
this endpoint never cascades into rule or subscription deletion.

Success removes only the unreferenced service owner and residual sessions,
retaining audit, usage and revoked VLESS history. Other customers and their
sessions remain unchanged. The operation emits `service_owner.delete` and
does not recompile or publish any node configuration generation. Removal, audit
and idempotency persistence share one PostgreSQL transaction. A commit failure
leaves the owner, sessions, replay record and published read cache unchanged.

## Shared request and response semantics

Both deletion endpoints require a JSON body containing the current positive
`revision`; `If-Match` alone is insufficient. Idempotency keys contain 8 to 128
visible ASCII characters without spaces and are scoped to actor and operation.

| Result | HTTP status | Response |
| --- | --- | --- |
| First successful deletion | 200 | `{"replayed":false}` |
| Exact replay by the same administrator | 200 | `{"replayed":true}` |
| Missing or invalid administrator session, including a customer session | 401 | `unauthorized` problem |
| Invalid ID, key, or revision | 400 | `validation_error` problem |
| Resource absent with a new key | 404 | `not_found` problem |
| Stale revision, remaining login or service references | 409 | `conflict` problem with the reason |
| Same actor/operation/key with a different ID or revision | 409 | `idempotency_conflict` problem |

A rejected attempt consumes no key. After resolving references, retry with the
same request only if the owner revision still matches. Successful replay remains
available after process restart and does not duplicate audit or mutate state.

## User groups and interrupted external deletion

`DELETE /api/v1/user-groups/{id}` is a separate administrator operation using the
same revision/key request pattern. Customer/service-owner, device-group and
network-policy references prevent deletion; it does not cascade.

For an external workflow that already deleted its rules but received `404` from
the previously missing service-owner endpoint, first deploy the panel fix and
reread the exact remaining owner. Verify identity, revision and remaining
references before resuming only the unfinished owner step. Do not recreate or
repeat rule deletion, revoke unrelated identities, or treat every `404` as
proof of a successful deletion.
