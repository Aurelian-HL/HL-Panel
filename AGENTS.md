# Project Instructions

This repository is a clean-room, standalone control plane. It must not depend on
`ht.hongle.work`, `zf.hongle.work`, NY internals, X-Panel, or 3x-ui at runtime.

## Stack

- Go control plane and edge agent.
- Vue 3 + TypeScript web console. Do not introduce React.
- PostgreSQL is the production system of record.
- Xray-core and upstream gost are separately managed data-plane processes.

## Safety Boundaries

- Protected live services: `zf.hongle.work`, `kl.hongle.cc`, and
  `ht.hongle.work` must remain uninterrupted. They may only be consulted or
  downloaded from as read-only references within authorized access. Never
  modify their data/configuration, restart them, overwrite deployments, switch
  their DNS, take over ports, or run load/failure tests against them.
- Development, protocol tests, import previews, and fault injection must use
  local loopback or a new isolated environment. No production cutover is part
  of the current authorization, and running this project must not affect the
  three protected services.
- Do not add arbitrary remote shell execution.
- Never return enrollment token hashes, credentials, UUID secrets, Reality
  private keys, or subscription tokens from list APIs or logs.
- The agent must preserve last-known-good configuration when the control plane
  is unreachable or a new generation fails validation.
- All mutating control-plane operations need an audit event and idempotency
  semantics before they are considered production-ready.
- Tests must not connect to or mutate production systems.

## Product Scope And Acceptance

The initial four-page control-plane slice is an engineering prototype, not the
requested product. The product must retain the NY business system and extend
its normal rule workflow with VLESS and single-endpoint device-group scheduling.
Use `docs/ny-rebuild-acceptance-20261002.md` as the current product baseline and
`docs/implementation-gap-20261002.md` for the inspected implementation gaps.

Prioritize complete operator workflows: install/register machines, configure
entry and exit groups, authorize users, create structured forwarding rules,
activate real engines, verify traffic, edit/pause/resume, enforce limits, and
import supported NY exports. Users/groups, rules/groups, tunnels, traffic
accounting, and system operations remain in scope; staging implementation does
not remove them from the required NY capability baseline. Commercial and
notification domains are explicitly out of scope: shop, plans, purchases,
renewals, orders, wallet/recharge/payment, redemption codes,
invitations/rebates, and push channels/Telegram. Remove their dependent routes,
settings, metrics, permissions, storage, and dead actions.

Normal workflows must not require raw engine JSON or a separately maintained
endpoint candidate file. VLESS is an ingress option in the same product flow,
not a replacement for existing forwarding functionality or ten client URIs.

A feature is accepted only when its page, authorized API, durable state,
deployment/engine effect where applicable, and real-client result are verified.
Document-only models, placeholder routes, dry-run acknowledgements, screenshots
of empty pages, and isolated compiler tests do not establish feature delivery.
Keep engine activation bounded and dry-run until explicit integration tests
exist; never label a saved configuration file as a verified running engine.

## Device-Group Product Invariants

- A customer receives one stable service endpoint and one protocol URI. Never
  expand a device group into multiple customer-visible node URIs.
- Membership is authored once in the device group. Endpoint candidates are
  projections and must follow additions, retirement, and weight changes.
- Select one eligible member for each new connection. Existing connections
  remain attached to their selected backend; reconnect is required after failure.
- Customer ingress protocol and egress routing are separate domains. VLESS
  can use DIRECT or a configured SOCKS5 landing without changing this distinction.
- A TCP probe can veto an authorized candidate; it cannot grant protocol health,
  renew the control-plane health lease, or override draining/offline status.
- Keep every protocol test and failure injection on loopback or explicitly
  isolated new resources. Do not use any protected live service as a test target.
- Preserve NY business capabilities through explicit models and verified import
  contracts; do not claim compatibility with unknown private tunnel protocols.

## Module And Naming Rules

- Organize Go packages by domain capability, not generic technical buckets.
- HTTP handlers only decode, authorize, call a service, and encode a response.
- Business invariants live in domain services; persistence stays behind explicit
  repository interfaces.
- Keep control-plane, agent, web, protocol schemas, migrations, and deployment
  assets in separate top-level boundaries.
- Use `GroupRevision` for a device group's revision and
  `NodeConfigGeneration` for a node's complete desired/applied configuration.
  Never call both concepts `generation` inside domain code.
- Prefer precise names such as `EnrollmentTokenService` and
  `AtomicStateStore`; do not create catch-all `utils`, `helpers`, or `manager`
  packages.
- Split files when they combine unrelated lifecycle, transport, storage, and
  domain responsibilities. Keep tests next to the owning package.
- Cross-module DTOs are versioned in `schemas/` or an explicit protocol
  package. Frontend types mirror the public JSON contract, not database rows.
