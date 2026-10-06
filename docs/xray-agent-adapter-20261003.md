# Xray Agent Adapter Boundary

`internal/agent/engine.XrayProcessAdapter` is an explicit, opt-in adapter for
one local Xray process. It is not wired into the default edge-agent runtime;
the runtime still uses `DryRunFileAdapter` until a separately reviewed
deployment configuration selects a real engine.

## Apply lifecycle

1. `Prepare` verifies the node-bundle hash and accepts only
   `EngineXray` fragments. It merges `inbounds` and `outbounds` arrays and
   rejects conflicting singleton settings or duplicate tags. The generated
   file is written with mode `0600` below the private staging directory.
2. `Validate` re-reads the staged file and invokes the configured fixed Xray
   binary as `run -test -config <path>`. The runner discards engine output so
   UUIDs and Reality private keys do not reach logs or error responses.
3. `Commit` atomically replaces the active file. When `AutoStart` is enabled,
   the old process is stopped and a new process is started with the active
   file. A start failure restores the previous bytes and attempts to restart
   the last process before returning an error.
4. `Verify` checks the active bytes against the compiled bundle and, when
   process control is enabled, requires the process to still be alive.
5. `Rollback` stops the candidate, restores the previously prepared native
   configuration, and starts it again when process control is enabled.

## Mixed Xray/GOST nodes

Xray and GOST are separate data-plane processes. This adapter deliberately
returns `ErrXrayUnsupportedFragment` for a bundle containing a GOST fragment;
it never drops that fragment or rewrites it as Xray. The current runtime's
dry-run adapter remains the compatible path for mixed bundles. A production
mixed-engine rollout needs a coordinator that partitions one node bundle and
commits independent Xray and GOST adapters with a cross-engine rollback
protocol. That coordinator is not implemented here, so this adapter must not
be presented as mixed-engine activation.

## Verification

Unit tests cover staging tamper detection, fixed-runner validation, conflicts,
atomic replacement, process start failure, and rollback. The optional
`tests/integration/xray/adapter_test.go` starts only a loopback listener and is
skipped unless `NYVP_XRAY_BINARY` points at the pinned Xray 26.3.27 binary:

```text
NYVP_XRAY_BINARY=<absolute path to pinned xray> go test ./tests/integration/xray -run TestXrayProcessAdapterLoopback
```

No production host, DNS name, or protected service is used by this adapter or
its tests.
