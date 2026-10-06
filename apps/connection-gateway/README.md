# Connection gateway

`connection-gateway` is the standalone single-entry data-plane front door. A
client connects to one stable address. For each new TCP connection the gateway
selects exactly one ready backend from the current device-group snapshot; it
never returns the backend list or ten separate client URIs.

The gateway is protocol-agnostic. VLESS, Reality, or SOCKS5 termination remains
on the selected Xray/GOST backend. Weighted round-robin, weighted
least-connections, and rendezvous selection share the same routing
implementation.

## Safety defaults

- The listener defaults to `127.0.0.1:8443`.
- A non-loopback listener is rejected unless `allow_non_loopback` is explicitly
  set to `true`.
- Membership is a complete, monotonic snapshot. A stale, malformed, or
  same-revision-different-content file is rejected while the last known good
  snapshot stays active.
- `offline`, `draining`, and expired-lease endpoints receive no new
  connections. Existing tracked connections may finish.
- Optional `tcp_reachability` is disabled by default. When enabled, it probes
  only control-plane-authorized `ready` endpoints with a valid lease. Repeated
  failures install a gateway-local veto; repeated successes only remove that
  veto. A TCP success never renews a lease or promotes an offline, draining, or
  expired endpoint.

The example's year-2099 health lease is documentation-only. A real probe or
control-plane publisher must atomically replace the membership file with
short-lived leases and increment `revision`; do not use long-lived leases in
production.

## Local run

```text
go run ./apps/connection-gateway -config /absolute/path/to/config.json
```

This application has no dependency on `ht.hongle.work`, `zf.hongle.work`, NY
panel internals, X-Panel, or 3x-ui.
