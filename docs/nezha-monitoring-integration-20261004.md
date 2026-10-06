# Nezha V2 monitoring integration (2026-10-04)

## Current boundary

The HL-panel integration is code-only and disabled by default. It does not install
Nezha Dashboard or Agent, create a PAT, change production Nginx, or alter node
health/forwarding eligibility. NY's customized probe is not assumed compatible
with official Nezha V2. No production monitoring result has been verified.

The inspected xzf host is Debian 12 amd64 with 701 MiB RAM, about 293 MiB
available, no swap, and an existing Nginx, PostgreSQL and HL-panel API stack.
**Do not install Dashboard on this host in its current capacity.** Official
Dashboard guidance cites 512 MiB for most standalone use, not an allowance for
these colocated services. Use an isolated host or first provision measured
headroom, swap and resource limits, then test peak usage without affecting the
existing services. Capacity, backup and rollback gates precede installation.

## Verified upstream project and artifacts

- Dashboard: <https://github.com/nezhahq/nezha>, Apache-2.0, release `v2.3.17`.
- Agent: <https://github.com/nezhahq/agent>, release `v2.3.5`.
- Official deployment/configuration: <https://github.com/nezhahq/nezhahq.github.io>.
- The official `v2.3.17` release has `dashboard-linux-amd64.zip` (31,420,050
  bytes), asset SHA-256
  `9488691fd8619f2a341747bd4d20121ec45a057444fd1d46da5e79f508e218c4`.
  Compare the downloaded archive's `sha256sum` with this GitHub release digest
  before extraction. Do not run a floating `latest` installer unreviewed.
- The official `nezhahq/scripts` installer supports a standalone, non-Docker
  branch: it expands the release archive and runs the Dashboard app under
  `nezha-dashboard.service`. This is a supported route, not an action taken here.

## Network and security gates

Nezha V2 serves Web and Agent gRPC on port 8008. With no new DNS, a narrower
same-hostname arrangement is possible: put only the official
`^~ /proto.NezhaService/` gRPC location in the existing HTTPS vhost, pointing
to a Dashboard bound to `127.0.0.1:8008`. The example is
`deploy/nginx/nezha-agent-grpc.location.example`; it is **not installed**.
Keep every Dashboard Web/API path, including `/api/v1/*`, off that public
vhost. HL-panel accesses the Dashboard over loopback; administrators can reach
its Web UI with an authenticated SSH tunnel. Existing 443 must negotiate
HTTP/2, and Agent must target that hostname on port 443 with `tls: true`.
Back up and inspect the actual vhost and certificates, run `nginx -t`, then
reload only after capacity and a safe deployment window are established.
Verify a real Agent gRPC report and that existing HL `/api` and WebSocket
routes still work. A dedicated hostname/server block is an alternative if
public Dashboard Web access is later required; that broader proxy also needs
the official `/api/v1/ws/` WebSocket and Web routes.
Do not expose an unauthenticated Dashboard, retain default admin credentials,
or enable MCP for this monitoring-only deployment. Set `force_auth: true`.

For monitoring-only Agents, configure `disable_command_execute: true`,
`disable_force_update: true`, `disable_nat: true`, and, when active probes are
not required, `disable_send_query: true`. Confirm the monitored `nic_allowlist`
and traffic units on the actual host; default reporting interval is 3 seconds.
Back up Dashboard config/data and existing Nginx configs before a change.
Rollback is to stop/disable the new service, remove only its dedicated Nginx
server block, validate/reload Nginx, and restore its backed-up data/config if
needed. Never restart protected services as a shortcut.

## HL-panel read-only contract

`GET /api/v1/device-groups/{group_id}/monitoring/nezha` requires an HL-panel
administrator bearer token. It returns `{items:[...],upstream_status}` with
`upstream_status` equal to `disabled`, `ok`, or `unavailable`. An unknown group
returns 404. Only active group members are listed. Every item includes
`node_id` and `link_status` (`linked` or `unlinked`). A linked row optionally
includes `nezha_server_id`, `online`, `name`, `ipv4`, `ipv6`, `country_code`,
`uptime_seconds`, `cpu_percent`, used/total memory and disk bytes, inbound/
outbound speed in bytes per second, inbound/outbound transfer bytes, and
`sampled_at` (Nezha `last_active`). Missing upstream fields are omitted.
`online` is derived only for presentation from a non-future `last_active`
within 30 seconds; it is **not** HL-panel forwarding health. Inbound is
server-received/downlink and outbound is server-sent/uplink.

`GET /api/v1/monitoring/nezha/servers` is also administrator-only. It lists
each Nezha server visible to the PAT once, even if no HL node or device group
exists. Unmapped servers have `link_status: "unmanaged"` and a synthetic
`node_id` of `nezha:<server_id>`; when a server ID is explicitly mapped to an
HL node, the same server appears once as `linked` with the real HL node ID.
The device-group endpoint and its `linked`/`unlinked` contract are unchanged.

The official `GET /api/v1/server` responds with `{success:true,data:[...]}`.
The read-only PAT requires `nezha:inventory:read` and a server-ID whitelist
covering only machines this HL-panel is authorized to monitor, including any
installed Agents awaiting HL node mapping (currently server IDs 1 and 2).
Do not use an unrestricted/all-server PAT. It stays in a protected local file; neither PAT
nor raw upstream response is sent to the browser or logs. No other Nezha
endpoint is called. `geoip.ip` is a host public address, **not** a verified
forwarding egress IP. Nezha provides one `country_code`, not distinct IPv4/
IPv6 regions. Those unsupported values must remain unknown in the UI.

Optional control API environment settings:

```text
CONTROL_NEZHA_DASHBOARD_URL=http://127.0.0.1:8008
CONTROL_NEZHA_PAT_FILE=/etc/nyvp/nezha-pat
CONTROL_NEZHA_NODE_MAP_FILE=/etc/nyvp/nezha-node-map.json
CONTROL_NEZHA_TIMEOUT=3s
```

The URL must be an HTTP loopback IP literal. The PAT file is a bounded regular
file, e.g. root:nyvless `0640`, containing only the PAT. On Unix the loader
rejects files accessible by others, writable by the group, or executable;
`0644` is not accepted. Windows `FileMode` is not an ACL audit, so Windows
deployments must verify PAT-file ACLs separately. The map file is a JSON
object such as `{"nod_example":12}` with an explicit HL node ID to Nezha
server ID pairing; duplicate server IDs are rejected. No hostname guessing.
Leave all settings unset to keep this integration disabled. The current map is
file-based and loaded at API startup; changes require a planned API restart.

Before enabling in a safe environment: snapshot existing configuration and
data; validate the downloaded artifact digest, ports, TLS, credentials and
capacity; create a least-privilege PAT and mapping; start Dashboard/Agent;
verify `/api/v1/server` locally without displaying the PAT; verify the HL
endpoint with an administrator token and inspect each linked machine against
the actual host; test Agent disconnect/reconnect, Dashboard outage, desktop
and mobile views; finally verify existing HL functions and public endpoints.
Production installation and these end-to-end checks remain outstanding.
