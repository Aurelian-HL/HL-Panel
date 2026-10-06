# Module Boundaries And Vocabulary

## Deployable Applications

| Application | Owns | Must not own |
|---|---|---|
| `control-api` | HTTP API, authentication, commands and queries | Engine process control |
| `worker` | Durable reconciliation and asynchronous operations | Public HTTP sessions |
| `agent-gateway` | Long-lived authenticated agent transport | Business configuration authoring |
| `edge-agent` | Local validation, atomic apply, health and usage | Tenant/business decisions |
| `connection-gateway` | Stable TCP ingress, per-connection admission and membership snapshot activation | Customer identity authoring or engine process lifecycle |
| `web-admin` | Administrative workflows | Direct node or engine access |
| `web-user` | Customer self-service | Administrative topology and secrets |
| `probe` | External protocol and egress observations | Configuration mutation |
| `ny-import-preview` | Read-only local snapshot inspection and redacted preflight reports | Panel network access, database writes, or execution of imports |

The first vertical slice can host control API, worker scheduling, and agent
HTTP endpoints in one Go process, but their packages and interfaces remain
separate so deployables can split without rewriting domain logic.

## Control-Plane Domains

- `auth`: administrator identities, password verification, sessions and RBAC.
- `enrollment`: one-time enrollment token issuance and consumption.
- `nodes`: inventory, credentials, heartbeats and lifecycle.
- `groups`: device groups and memberships.
- `generations`: group revisions, node config compilation and apply state.
- `endpoints`: stable customer endpoints, server-side candidate pools and
  selection-policy/state vocabulary. Pool membership is a materialized
  projection of active device-group membership, never a separately authored
  customer subscription list.
- `scheduling`: health-TTL/priority filtering and the adapter to the shared
  routing selector; it must not implement a second copy of selection math.
- `audit`: append-only security and operator events.
- `httpapi`: versioned transport adapters only.

## Agent Domains

- `controlclient`: typed enrollment, heartbeat, desired and result calls.
- `state`: credential, desired, applied and last-known-good persistence.
- `reconciler`: monotonic generation state machine.
- `engine`: bounded adapters for `node-bundle`, Xray, and GOST.
- `health`: local engine and egress observations.

## Shared Data-Plane And Import Modules

- `routing/scheduler`: selection math shared across adapters.
- `routing/endpointrouter`: candidate eligibility and connection reservations.
- `gateway/tcpproxy`: opaque TCP forwarding with connection lifecycle cleanup.
- `gateway/membership` and `gateway/runtime`: versioned snapshots and last-known-good refresh.
- `gateway/healthprobe`: bounded TCP reachability checks; may veto an authorized
  candidate but may never grant protocol readiness or extend a health lease.
- `provisioning/vless`: explicit TLS inbound and DIRECT/SOCKS5 egress compilation,
  with a separate single-URI renderer. Does not own customer storage or secret distribution.
- `importers/ny`: immutable offline snapshots, structural inspection, and exact-schema
  adapter contracts. Unknown source fields never become business objects implicitly.
- `serviceaddress`: canonical service hostname/IP validation for API and protocol compilers.

## Vocabulary

| Term | Exact meaning |
|---|---|
| `GroupRevision` | Monotonic authoring revision for one device group |
| `NodeConfigGeneration` | Monotonic complete desired configuration for one node |
| `DesiredNodeConfig` | Canonical node bundle assigned by the control plane |
| `AppliedNodeConfig` | Locally verified generation and hash |
| `ConfigurationFragment` | One group's contribution to a node bundle |
| `EnrollmentToken` | Single-use short-lived bootstrap secret, stored hashed |
| `NodeCredential` | Post-enrollment credential, stored hashed and returned once |

An agent heartbeat and an endpoint protocol health observation are different
facts. Only the latter can make a projected endpoint candidate eligible for a
new customer connection.

Avoid ambiguous nouns such as `config version`, `manager`, or `status` without
the resource name. State enums use explicit values and transitions.
