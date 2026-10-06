# edge-agent

`edge-agent` is the local, least-privilege runtime for one enrolled node. It
does not run remote shell commands and it does not author business or routing
decisions. The first slice uses the dry-run file engine adapter: desired node
bundles are strictly validated, staged, atomically activated, read back, and
verified before the applied generation advances.

## First run

1. Copy `config.example.json` to a private local path and set an absolute
   `data_dir`.
2. Put the one-time enrollment token in the environment named by
   `enrollment_token_env`.
3. Run `go run ./apps/edge-agent -config /path/to/config.json`.

The token is consumed only during enrollment. The resulting node credential is
written once with private permissions and is never logged. Subsequent runs use
that credential and preserve the last-known-good configuration while the
control plane is unreachable.

## One-command installation on a dedicated node

Use the complete command generated in the device group's node integration
dialog. It already contains the one-use group token and panel HTTPS origin.
The official `install-node.sh` installs the Agent, native host probe, pinned
Xray and GOST binaries, enrolls into that group, and enables the systemd
service. No separate engine or probe installation is required. See
[the operator manual](../../deploy/README.md#节点一键安装) for offline installation
and explicit CA trust when using a self-signed IP endpoint.

The installed configuration uses `mixed` with both auto-start flags enabled.
Valid rules from the panel are required before engine processes start; no
business rules are created by the installer. The low-privilege service uses
ports above 1023 and does not change firewall rules or other services.
The command contains a one-use secret: do not share it and clear its shell
history entry after running it. Enrollment deletes the temporary token file;
subsequent starts use the private persisted identity. An existing enrolled
identity is never overwritten.

The native Linux probe reads host CPU, memory, root filesystem, network,
uptime and connection counts without an external dashboard. Unknown metrics
remain absent. CPU and network speed become available after two samples.
Host traffic is separate from per-customer engine accounting.

## Low-level developer installation

Build `./apps/edge-agent` for the node architecture and transfer the binary,
`deploy/systemd/hl-panel-edge-agent.service`, and the two scripts in
`deploy/edge-agent/` to that node using a trusted channel. On the node run:

```sh
sh install.sh ./edge-agent ./hl-panel-edge-agent.service https://panel.example.com
sh enroll.sh
```

The installer accepts an optional explicit engine configuration:

```sh
sh install.sh ./edge-agent ./hl-panel-edge-agent.service https://panel.example.com \
  xray /usr/local/bin/xray '' false
```

The fourth argument is `dry_run`, `xray`, `gost`, or `mixed`; the remaining
arguments are the absolute Xray path, the absolute GOST path, and an explicit
`true`/`false` process-ownership flag. Binary paths must already be executable
regular files. `xray` and `gost` modes collect usage from the corresponding
loopback metrics service even when process ownership is `false`. `mixed` mode
requires both paths and `true` because the runtime validates that both local
processes are owned and running. Omitting the optional arguments keeps the
safe `dry_run` default and therefore does not collect traffic usage.

The installer refuses to overwrite an existing HL agent installation and does
not touch Nezha or data-plane services. `enroll.sh` prompts for one short-lived,
one-use **device-group** token without putting it in a command argument. The
token is held in a root-only environment file only until
`credentials.json` is persisted. After successful registration the script
removes that file and restarts the service without it. A failed or timed-out
registration stops and disables the service and removes the token file; inspect
the journal and issue a new token before retrying. Never
reuse an enrolled node's credentials to join a different group.

The installed default is `dry_run`: a heartbeat and group membership are not
proof of a working forwarding engine. Engine ownership and reachable dial
address must be configured and verified separately. A Nezha monitoring Agent
is a different service and cannot consume the HL group token.

The control plane URL must use HTTPS. For local development only, set
`allow_insecure_loopback` to `true` and use `http://127.0.0.1`, `http://[::1]`,
or `http://localhost`; non-loopback HTTP is always rejected.

The default `engine_mode` is `dry_run`, which only stages and verifies the
node-bundle file. A dedicated, isolated VLESS Reality node can opt into the
fixed Xray adapter with `engine_mode: "xray"` and an absolute
`xray_binary_path`. The Agent reads the loopback Xray StatsService whenever
Xray mode is enabled, including when Xray is managed by an external systemd
unit. With external ownership, the service must expose StatsService at
`xray_stats_api_address`; accounting remains read-only and does not restart or
rewrite that process. `xray_auto_start` must remain `false` unless that node is
explicitly authorized to own the local Xray process. The standalone Xray
adapter rejects mixed Xray/GOST bundles; it never silently drops the other
engine's fragments.

For a dedicated direct TCP forwarding node, set `engine_mode: "gost"` and an
absolute `gost_binary_path` pointing to the verified GOST 3.3.1 executable.
`gost_auto_start` defaults to `false`; enabling it explicitly lets this agent
own one local GOST process. The adapter accepts only structured GOST direct
TCP fragments and rejects Xray or mixed bundles. Staging a configuration with
auto-start disabled does not make forwarding live.

For a node carrying both VLESS Reality and direct TCP forwarding, set
`engine_mode: "mixed"`, provide absolute `xray_binary_path` and
`gost_binary_path`, and enable both `xray_auto_start` and `gost_auto_start`.
The agent validates both components, starts the two owned processes, and
reports a verified mixed generation only while both are running. If either
activation fails, it restores the previous complete generation. This is
two-process reconciliation, not an atomic switch or a substitute for an
external process supervisor; use a dedicated node and preserve a rollback
path when enabling it.
