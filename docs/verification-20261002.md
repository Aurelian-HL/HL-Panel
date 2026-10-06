# Verification record: 2026-10-02

This record covers the standalone clean-room platform only. The original local
verification below did not connect to or change `ht.hongle.work`,
`zf.hongle.work`, `kl.hongle.cc`, a production database, DNS, customer accounts,
or production servers. A later, separately scoped deployment validation on the
authorized new host is recorded in the addendum below; it does not change the
protection boundary around those three existing services.

## Verified behavior

- One TCP listener selects one backend per new connection. Clients never receive
  the candidate list.
- A ten-backend loopback socket test completed exact weighted round-robin cycles.
  `offline`, `draining`, and expired-lease members stopped receiving new
  connections. A recovered member with a renewed lease rejoined, and every
  active-connection reservation returned to zero.
- With optional TCP reachability enabled, closing one of ten real loopback
  listeners installed a local veto after two consecutive failures. The other
  nine listeners continued serving through the same gateway address. Restarting
  the failed listener at the same address removed the veto only after two
  consecutive successes. The monitor is disabled by default.
- Reachability verdicts and router membership carry the same snapshot revision.
  Membership replacement and local-veto transitions are serialized, so a late
  same-address verdict from an older revision cannot clear or strand a veto.
- Listener failure closes tracked client and backend sockets, waits for handlers,
  and releases the reservation before `Serve` returns.
- Membership activation is monotonic. Invalid files, lower revisions, and
  changed content at an existing revision preserve the last known good snapshot.
- Device-group members are projected into endpoint pools. Later group member and
  weight changes synchronize without a second manually maintained candidate
  list. Agent heartbeat does not impersonate protocol health.
- The web console presents one stable endpoint and does not submit or display
  backend member URIs.

## Original local commands and results

These counts describe the earlier local source snapshot. They are retained as
historical evidence and must not be read as the current release's test totals.

```text
go test ./...
PASS, 25 packages and 160 tests/subtests, 0 failed, 0 skipped

go vet ./...
PASS

go test ./internal/gateway/... -count=10 -shuffle=on
PASS

go test ./internal/gateway/healthprobe ./internal/gateway/endpointselector -count=20 -shuffle=on
PASS

go test ./internal/gateway/... ./internal/routing/... ./internal/agent/engine ./apps/connection-gateway -count=1
PASS

go vet ./internal/gateway/... ./internal/routing/... ./internal/agent/engine ./apps/connection-gateway
PASS

cd apps/web-admin
npm test
7 test files, 16 tests passed

npm run typecheck
PASS

npm run build
PASS, 1940 modules transformed
```

The local browser/API smoke run covered 1440 px, 980 px, and 390 px viewports
with zero unexpected browser-console errors. The error-flow run observed one
expected duplicate-request HTTP 409 response. Its temporary API, Vite, and
browser processes were stopped after verification.

## Real Xray test

Pinned core: XTLS/Xray-core `v26.3.27`, Windows x64.

- Official ZIP SHA-256:
  `d004c39288ce9ada487c6f398c7c545f7d749e44bdfdd59dbc9f865afba4e1ad`
- Extracted `xray.exe` SHA-256:
  `15c2d007954ac53ba69b80ec91242786b3c0b71d52649165b4ca1d5cc96ef8f1`
- Reported version:
  `Xray 26.3.27 d2758a0 (go1.26.1 windows/amd64)`

The non-skipped integration command was:

```text
NYVP_XRAY_BINARY=<absolute verified xray.exe> \
go test -v ./tests/integration/xray -run TestSingleVLESSURI -count=1
```

Three paths passed: standard VLESS over TLS with DIRECT egress,
`xtls-rprx-vision` with DIRECT egress, and standard VLESS over TLS with an
authenticated SOCKS5 landing egress. For each path, one stable URI reached two
real TLS Xray servers through the gateway; six fresh requests split 3:3. After
one server was stopped, the local TCP monitor reached its failure threshold and
automatically vetoed that backend without changing membership or the client
URI; three further requests succeeded on the remaining server. In the SOCKS5
case, all nine successful requests traversed the authenticated landing proxy;
incorrect landing credentials failed without falling back to DIRECT. Wrong-CA
and wrong-SNI clients were rejected in every path, and a wrong VLESS UUID was
also rejected.

## PostgreSQL and new-host deployment addendum

The control API now supports PostgreSQL transactional snapshots for a
single-instance trial. With `CONTROL_DATABASE_URL_FILE` configured and
`CONTROL_ALLOW_VOLATILE_STORE=false`, successful writes persist in
`nyvp_control_snapshots`. Every operation reads the full state; writes lock the
row and replace the snapshot transactionally. The format is bounded to 16 MiB
and is not the normalized relational repository described by `migrations/`.
Those relational migrations remain unconnected and are not part of this trial
deployment.

In the subsequent release/deployment run on 2026-10-02, the local suite reported
172 passing tests/subtests. Two PostgreSQL integration tests also passed against
an explicitly isolated test database on the new host. These later results are
separate from the original 160-test local run above; they must not be represented
as all having run in that earlier environment.

The deployment executor has verified HTTPS on the authorized new host
`47.236.178.213` / `xzf.hongle.cc`, real administrator login, session persistence
after restarting the API, and database backup/restore. Public-browser acceptance
is still in progress at the time of this addendum. No full deployment acceptance
or customer data-plane readiness is claimed here. `/healthz` alone is not
database evidence because it only reports process liveness.

The [deployment manual](../deploy/README.md) and
[backup/restore procedure](../deploy/backup-and-restore.md) document the required
permissions, release switching, acceptance checks, and rollback boundaries.
Manuals describe procedures; individual execution results must still be kept in
the release acceptance record without credentials or snapshot payloads.

## Not yet verified or production-ready

- The automatic local check proves only that a backend TCP port accepts a
  connection. It does not perform a VLESS handshake, validate authentication,
  test the configured destination or landing proxy, verify the observed exit
  IP, or write protocol/egress health back into control-plane membership.
- The membership file is a tested integration boundary, but the control plane
  does not yet publish gateway snapshots.
- The normalized PostgreSQL repositories, relational migration rollback,
  snapshot growth management, customer identity storage, secret distribution,
  quotas, and production-scale audit retention remain incomplete. Transactional
  snapshot durability and isolated backup/restore have now been verified as
  described in the addendum; this does not establish high availability or a
  production capacity guarantee.
- The gateway is a single local process. Active-active ingress, load testing,
  kernel tuning, data-plane firewall/certificate rollout, and data-plane
  deployment rollback have not been validated. The new control panel's HTTPS
  validation does not cover gateway or node rollout.
- `go test -race` was attempted but did not run because this Windows Go
  environment has `CGO_ENABLED=0`; the tool reported
  `-race requires cgo`. Ordinary concurrency, shutdown, and repeated socket tests
  passed, but this is not a substitute for race-detector coverage.

The current artifact is an independently runnable development slice, not a
production replacement for NY.
