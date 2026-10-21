# Deletion compatibility contract

This candidate contains rule-deletion lifecycle handling and an administrator
customer-deletion API. It does not add a user-group deletion API, change the web
console, upgrade an agent or an engine, or authorize production deletion.

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

Active connections, foreign references or ownership, invalid revisions, and
stale rule revisions still reject deletion. Validation happens before mutation.
Compilation or database commit failure rolls back the mutation. Deleting one
rule preserves the customer and its other rules. Exact request replay does not
recompile configuration or duplicate business audit events.

`DELETE /api/v1/forwarding-rules/{id}` remains unsupported (`405`).

## Customer deletion

```http
DELETE /api/v1/customers/{id}
Authorization: Bearer <administrator-session>
Content-Type: application/json
Idempotency-Key: delete-customer-example-1
```

```json
{"revision": 1}
```

Read the current customer revision before submitting. The JSON body is required;
an `If-Match` header does not replace it. Keys must contain 8 to 128 visible ASCII
characters without spaces.

| Result | HTTP status | Response |
| --- | --- | --- |
| First successful deletion | 200 | `{"replayed":false}` |
| Exact replay by the same administrator | 200 | `{"replayed":true}` |
| Missing or invalid administrator session, including a customer session | 401 | `unauthorized` problem |
| Invalid ID, key, or revision | 400 | `validation_error` problem |
| Customer absent with a new key | 404 | `not_found` problem |
| Stale revision or remaining references | 409 | `conflict` problem with the reason |
| Same administrator/key with a different customer or revision | 409 | `idempotency_conflict` problem |

A remaining forwarding rule (customer or owner reference), subscription, or
non-revoked VLESS identity/runtime rejects deletion. Resolve those references
explicitly first. No rule or subscription is deleted implicitly. A rejected
attempt does not consume the key, so it can be retried after resolving the
reference with the same customer revision and request body.

Successful deletion removes only the customer and all of its login sessions.
It retains audit, usage history, and already-revoked VLESS history, and preserves
other customers and their sessions. Customer removal, session invalidation,
audit, and idempotency persistence share one PostgreSQL transaction. A commit
failure must leave the customer, sessions, and published read cache intact.
