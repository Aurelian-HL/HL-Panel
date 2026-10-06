# API Contract: Vertical Slice 1

Base path: `/api/v1`. JSON request and response bodies use `snake_case`.

## Administrator API

### `POST /auth/login`

Request:

```json
{"username":"admin","password":"change-me"}
```

Response: `{ "access_token": "...", "expires_at": "...", "user": {...} }`.

### `GET /overview`

Returns node, group, online, syncing, and failed-apply counts.

### `GET /nodes`

Returns non-secret node inventory and desired/applied generation state.

### `POST /enrollment-tokens`

Request: `{ "name": "edge-01", "group_id": "...", "expires_in_seconds": 900, "nezha_server_id": 1 }`.

`group_id` binds this one-use token to a device group. Successful enrollment
adds the node to that group atomically. The raw token is returned once; list
and audit APIs never return it.
`nezha_server_id` is optional. When supplied, it must identify a server visible
through the configured read-only Nezha PAT and not already bound or reserved by
another pending token. The administrator must confirm the intended machine;
IP address and display name are not trusted as identity. Issuance fails closed
if Nezha monitoring is unavailable. The response includes the selected ID and
the raw one-use HL enrollment token. On successful HL Agent enrollment, the
same transaction creates the HL node, joins its device group, and persists the
one-to-one Nezha server ID binding. A Nezha Agent alone is not an HL forwarding
node. Reusing a selected ID or replaying a consumed token is rejected.
The consumed token returns a conflict on retry; the node credential is not
persisted in recoverable form and is never returned a second time.

### `GET /device-groups/{group_id}/enrollment-tokens`

Administrator-only response: `{ "items": [{ "id": "...", "name": "...", "group_id": "...", "nezha_server_id": 1, "expires_at": "...", "created_at": "..." }] }`.
Only unused, unrevoked, unexpired tokens for this group are listed. The bearer
token and its hash are never returned. An unknown group returns `404`.
The selected Nezha ID is non-secret; legacy tokens omit it. Revoking a pending
token releases its reservation, but retiring a group member does not uninstall
either Agent or automatically erase the node-level monitoring binding.

### `POST /enrollment-tokens/{token_id}/revoke`

Administrator-only; requires `Idempotency-Key`. Revokes an unused token by ID.
Used tokens cannot be revoked because their node credential has a separate
lifecycle. This is not equivalent to resetting an NY persistent group token or
rotating an existing node's credential.

Persisted Nezha bindings require control snapshot format 14. Existing format
13 snapshots are read and upgraded on the first write. After that write, an
older control-api binary cannot read the new format. A rollback to the old
release therefore also requires restoring the matching pre-upgrade database
snapshot; switching the binary alone is unsafe.

### `GET /device-groups`

Returns groups and member counts.

### `POST /device-groups`

Request: `{ "name": "gz-entry", "kind": "ENTRY", "selection_policy": "weighted_least_connections", "description": "..." }`.

New groups accept only `ENTRY` and `EXIT`. `EDGE` and `HYBRID` remain readable
only for compatibility with early prototype snapshots; create requests using
either legacy value are rejected.

Device groups are server-side candidate pools. They are not a customer
subscription pool and do not cause ten node URIs to be returned to a client.
Customer-facing services use an `EndpointPool` with one stable endpoint.

The group `selection_policy` is the default for endpoint pools created from the
group; an endpoint pool may override it explicitly.

### Device-group network policy

`GET /group-networks` returns `{ "items": [...] }` and
`GET /group-networks/{group_id}` returns `{ "network": {...} }`.

`PUT /group-networks/{group_id}` replaces the network policy for one device
group. It requires administrator authentication, an `Idempotency-Key` header,
and the current `revision` after the initial write. A typical entry-group
request is:

```json
{
  "connect_host": "entry.example.com",
  "port_start": 10000,
  "port_end": 21999,
  "port_ranges": [{"start": 10000, "end": 19999}, {"start": 21000, "end": 21999}],
  "direct_policy": "OPTIONAL",
  "allow_direct": true,
  "allowed_user_group_ids": ["users_standard"],
  "allowed_entry_group_ids": [],
  "allowed_exit_group_ids": ["group_hk_exit"],
  "fallback_exit_group_id": "group_hk_exit",
  "traffic_multiplier": 1,
  "revision": 0
}
```

`direct_policy` is authoritative for new writes and has three states:

- `DISABLED`: the entry group cannot be used by a `DIRECT` rule. It may be used
  as a front entry for `EXIT_GROUP` rules whose exit group is authorized.
- `OPTIONAL`: individual rules may choose either `DIRECT` or an authorized
  `EXIT_GROUP` path.
- `FORCED`: every rule using this entry group must use `DIRECT`.
  `allowed_exit_group_ids` must be empty.

`allow_direct` remains in requests and responses only as a compatibility
projection for older clients and version-2 snapshots. When `direct_policy` is
omitted, `allow_direct: false` maps to `DISABLED` and `allow_direct: true` maps
to `OPTIONAL`; the legacy boolean cannot express `FORCED`. Responses expose the
effective `direct_policy`, and `allow_direct` is `true` for `OPTIONAL` and
`FORCED`.

An `ENTRY` group requires a customer connection host and a nonzero listener
port range. `port_ranges` supports at most 32 disjoint ranges and is
authoritative for allocation; `port_start`/`port_end` remain the legacy outer
bounds. Ports in gaps are never allocated or accepted. `allowed_user_group_ids`
is an optional allow-list; empty means unrestricted. `allowed_exit_group_ids`
restricts exits reachable from an entry, while an exit group's
`allowed_entry_group_ids` applies the reverse check. Both sides must authorize
an `EXIT_GROUP` rule when their lists are non-empty. `fallback_exit_group_id`
must be one of the entry's allowed exits. It records intended failover policy;
it does not by itself prove that runtime health-based switching is active.

An `EXIT` group has no entry-forwarding permission: its effective policy must
be `DISABLED`, and its connection host, listener ranges, allowed exits and
fallback exit must be empty. Normal clients send an empty `connect_host` and
zero ports for an exit group.

Changing a network policy is rejected when it would invalidate an existing
rule, including removing an allocated port from all listener ranges, disabling
direct access used by a `DIRECT` rule, forcing direct access while an
`EXIT_GROUP` rule exists, removing a user/exit authorization still in use, or
adding an exit reverse-entry restriction that excludes an existing rule.

### Structured forwarding rules

`GET /forwarding-rules` returns `{ "items": [...] }` and
`GET /forwarding-rules/{id}` returns `{ "rule": {...} }`.
`POST /forwarding-rules` creates a rule and
`PUT /forwarding-rules/{id}` replaces one. Writes require administrator
authentication and an `Idempotency-Key` header; replacements also require the
current `revision`.

```json
{
  "name": "customer service",
  "customer_id": "customer_01",
  "entry_group_id": "group_gz_entry",
  "exit_group_id": "",
  "egress_mode": "DIRECT",
  "protocol": "tcp",
  "listen_port": 0,
  "targets": [{"host": "origin.example.com", "port": 443}],
  "selection_policy": "round_robin",
  "paused": false,
  "description": "",
  "revision": 0
}
```

Every rule, including a direct rule, must select an `ENTRY` device group in
`entry_group_id`. `DIRECT` never means "forward without a group". The two
supported route shapes are:

```text
DIRECT:     customer -> entry/direct-forwarding group -> final target
EXIT_GROUP: customer -> front entry group -> bearer -> landing exit group -> final target
```

The following invariants are enforced when a rule is saved:

- For `DIRECT`, `exit_group_id` must be empty. The customer's user group must
  authorize the entry group and direct forwarding, and the entry network's
  effective `direct_policy` must be `OPTIONAL` or `FORCED`.
- For `EXIT_GROUP`, `exit_group_id` is required and must reference a distinct
  `EXIT` device group. The entry policy must be `DISABLED` or `OPTIONAL`; both
  the customer's user group and the entry network must authorize that exit
  group.
- `targets` always contains final destination addresses. A `DIRECT` path means
  the selected entry-group machine reaches the target. An `EXIT_GROUP` path is
  intended to make the selected landing-group machine reach the target after
  the bearer hop.
- `listen_port: 0` requests atomic allocation from the selected entry group's
  port range. An explicit port must be inside that range and must not conflict
  on the same protocol with another listener on the same or a shared machine.

`EXIT_GROUP` identifies the desired front and landing groups; it does not prove
that a dedicated line exists. The current rule schema has no bearer or transit
line identifier and therefore cannot distinguish a private line from a public
tunnel or another transport. An authorized entry-to-exit relationship is a
control-plane policy, not evidence that the inter-group path is configured,
healthy, or deployed.

There are two independent selection domains. A device group's
`selection_policy` chooses a machine from that group's eligible members for a
new connection. A forwarding rule's `selection_policy` (`round_robin`,
`random`, or `ip_hash`) chooses among the rule's `targets` after the route has
reached the machine responsible for final egress. Target selection must not be
presented as group-member load balancing.

Persisting a rule is not activation. In the current control-plane repository,
an otherwise enabled rule remains `status: "pending_activation"` with
`deployed: false`. The verified GOST compiler subset accepts TCP `DIRECT`
rules only; UDP and `EXIT_GROUP` are rejected by that compiler, and structured
rules are not yet included in the Agent's compiled node bundle. A saved rule,
an allocated port, or an entry-to-exit authorization must therefore never be
reported as a running customer path.

For a VLESS Reality rule whose public SNI, public key, and Short ID are
complete, the administrator projection also includes the non-secret
`activation_reason: "vless_runtime_material_pending"` until the UUID and
node-local Reality private-key path are integrated with the Agent. Such a rule
is deliberately absent from executable node fragments; it must never be
silently emitted as a GOST or plain TCP DIRECT listener. Rules missing the
public Reality fields continue to use `ingress_status:
"pending_reality_parameters"` instead.

### Endpoint pool semantics

The first supported endpoint mode is `SINGLE_SERVICE_ENDPOINT`. A customer
receives one VLESS URI (or one SOCKS5 endpoint); the server-side scheduler picks
one healthy member for each new connection. Supported selection policies are:

- `weighted_round_robin`
- `weighted_least_connections`
- `rendezvous_hash`

Weights are applied by the scheduler at connection admission. Ordinary DNS
multiple-A records are not treated as weighted load balancing. A member is
eligible only when its routing state is `eligible` and its latest successful
health observation is within the configured health TTL. `draining` members may
finish existing connections but receive no new ones; `quarantined` and
`disabled` members receive none. The endpoint pool and its member state must be
updated transactionally with an idempotent operation before a member is exposed
again.

`group_id` is the source of endpoint candidates. Creating an endpoint pool
transactionally projects every current non-retired device-group member into the
pool with the group's weight and priority. A later device-group member add or
weight/priority change updates every endpoint pool for that group; operators do
not maintain a second manual candidate list. The endpoint pool may override the
group's selection policy, but the web console defaults to the group policy.

Node heartbeat proves only that the agent is reachable. It does not make an
endpoint candidate healthy. `healthy_candidate_count` includes only eligible
members with a successful, unexpired protocol health observation; an
unobserved candidate is counted as zero even when its agent is online.

### Internal endpoint-pool compatibility API

Endpoint pools are not exposed as a normal console page. These routes remain
for internal scheduler integration and compatibility while stable VLESS access
is moved into the ordinary rule workflow.

`GET /endpoint-pools` returns `{ "items": [...] }`. Each item contains one
stable `hostname`/`port` pair, its `protocol`, `selection_policy`, total
`member_count`, and the current `healthy_candidate_count`. Member addresses,
credentials, and customer URI lists are never included.

`POST /endpoint-pools` accepts:

```json
{
  "name": "广州统一入口",
  "group_id": "group_gz_edge",
  "mode": "SINGLE_SERVICE_ENDPOINT",
  "protocol": "vless",
  "hostname": "entry.example.com",
  "port": 443,
  "selection_policy": "weighted_least_connections",
  "idempotency_key": "client-generated-key"
}
```

The response is `{ "pool": {...}, "replayed": false }`. The request creates
one stable service endpoint and immediately returns the projected
`member_count`; a new pool's `healthy_candidate_count` remains zero until
protocol health is observed. It does not create or return a list of member
URIs. The endpoint pool and its candidate membership are controlled by the new
control plane only and have no runtime dependency on the legacy Hongle
websites.

`POST /endpoint-pools/{pool_id}/members` remains only as a compatibility,
idempotent confirmation path. The node must already be an active member of the
pool's device group, and the supplied weight/priority must match that group
membership. It never adds a node outside the group or creates a second source
of candidate truth. A replay returns `200` with `replayed: true` only while the
group member is still active, its current weight/priority still match the
request, and the projected candidate still exists. Replaying stale values after
a group weight/priority change, or replaying after member retirement, returns
`409 conflict`; it never returns a zero-value member.

### `POST /device-groups/{group_id}/members`

Request: `{ "node_id": "...", "dial_host": "node.example.test", "weight": 100, "priority": 0 }`.
This is an upsert of the group membership. It does not prove that the node has
applied its newly compiled configuration or passed a protocol health check.

### `GET /device-groups/{group_id}/members`

Administrator-only response: `{ "items": [...] }`, including retired members
with their `retired_at` timestamps. Returns `404` for an unknown group.

### `POST /device-groups/{group_id}/members/{node_id}/retire`

Administrator-only response: `{ "member": {...}, "assignments": [...], "replayed": false }`.
Retirement preserves the member history, removes it from endpoint candidate
projections, and recompiles the affected node's complete desired bundle. If
compilation fails, membership and candidate projections remain unchanged.
Repeating the call while the member is retired returns `replayed: true` with
no new assignments or audit event. The API does not yet persist the optional
`Idempotency-Key` header across a later re-addition of the same member;
operators must not treat it as a production-grade request replay guarantee.
Assignments mean desired configuration was generated, not applied or verified.

### `POST /device-groups/{group_id}/generations`

Request:

```json
{
  "engine": "xray",
  "config": {"schema_version":1,"services":[]},
  "idempotency_key":"client-generated-key"
}
```

The server canonicalizes the payload, calculates its SHA-256 hash, and creates
one monotonic group revision. It then recompiles a complete node bundle for
every affected non-retired member and assigns each node its own next monotonic
desired generation. A node may belong to more than one group.

## Agent API

The first slice uses a bearer node credential over TLS. It is deliberately
isolated behind an interface that will be replaced by node certificates and
mTLS before production deployment.

### `POST /agent/enroll`

Request includes the one-time token, hostname, platform, architecture, agent
version, and declared capabilities. Response returns `node_id` and the raw node
credential once.

### `POST /agent/heartbeat`

Authenticated by node credential. Reports boot ID, versions, resources,
capabilities, current applied generation, and last apply status.

### `GET /agent/desired`

Authenticated by node credential. Returns `204` when the node is current;
otherwise returns the assigned generation, engine, canonical configuration,
and expected SHA-256 hash.

The first slice uses `engine: "node-bundle"` and this configuration envelope:

```json
{
  "schema_version": 1,
  "fragments": [
    {
      "group_id": "...",
      "group_generation": 1,
      "engine": "xray",
      "config": {"schema_version":1,"services":[]}
    }
  ]
}
```

Fragments are sorted deterministically before hashing. Future compilers replace
the fragments with validated engine-native configuration, without changing the
agent's single node-level desired/applied generation invariant.

### `POST /agent/apply-results`

Authenticated by node credential. Request contains generation, phase, status,
config hash, and a bounded sanitized message. Accepted phases are `prepare`,
`validate`, `commit`, `verify`, and `rollback`; accepted final statuses are
`succeeded`, `failed`, and `rolled_back`.

## Required Semantics

- Enrollment tokens are random, expire, are single-use, and are stored hashed.
- Node credentials are random and stored hashed.
- Group revisions and node configuration generations are different sequences.
- A lower node generation can never replace a higher desired generation.
- A node only advances `applied_generation` after a successful commit and local
  verification with the expected hash.
- Repeating a generation request with the same idempotency key returns the same
  generation; using that key with different content returns a conflict.
- Offline agents keep their last-known-good files and processes.
