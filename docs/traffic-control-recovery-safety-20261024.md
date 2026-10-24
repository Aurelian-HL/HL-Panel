# Traffic-control application and forwarding safety

This change is verified on the isolated verification branch. It does not grant
permission to publish a release, upgrade a production node, or change live TC.

## Application and recovery

- TC and engine activation are serialized under the controller transaction.
  An engine rejection restores the controller's exact previous limits, even
  when the engine's last-known-good bundle is missing.
- Recovery has its own 30-second context and is not canceled with the request.
  It restores only ports actually attempted, including a partially changed
  port; unchanged and not-yet-attempted ports are not rewritten.
- Filter deletion ignores only recognized missing-filter diagnostics. Permission
  errors, a missing interface, and cancellation remain failures.
- A recovery failure persists the affected ports and blocks subsequent normal
  configuration changes. Explicit successful `Restore` repairs those ports and
  clears the marker. This does not kill or restart a live engine. The current
  panel does not expose a dedicated TC recovery action.
- Identical limit maps do not rewrite filters or the TC state file. Engine
  activation still runs, because changes may be unrelated to speed limits.
- Listener ports up to 55535 retain their previous filter identity. Ports
  55536–65535 share priority 10000 with distinct handles. Delete selects the
  exact high-port handle and cannot delete other listeners at that priority.
  The root qdisc and unrelated filters remain outside controller ownership.
- Every deletion, including low ports, selects the exact IPv4 flower handle.
  An unrelated filter sharing a listener's priority is retained. Acceptance
  checks coexistence at both individual and shared high-port priorities.

## Acceptance

`internal/agent/trafficcontrol/rollback_test.go` models both kernel directions
and cancellation. It covers partial updates/deletion, persistence and activation
failure, failed recovery across reload, idempotence, and high listener ports.

`internal/agent/runtime/traffic_control_apply_test.go` exercises actual reconciler
validation/commit/verification failure and missing engine LKG. Failed desired
generations must not become applied; the previous TC and engine files remain.

`TestDisposableLinuxElevenLimitsRestoreAndRemove` uses a random dummy interface
only on an explicitly disposable root Linux runner. It checks actual flower
selectors, police rates, high-port coexistence/deletion, cancellation, activation
failure, partial deletion, missing filters, and preservation of the root qdisc
and an unrelated filter in both directions. Cross compilation alone is not a
pass for this test. CI includes the TC package in race checks.

Real Xray/GOST integration tests remain necessary for unaffected long streams,
PID/process-epoch preservation, mixed engines, rollback, old Reality clients,
and UDP. Simulated TC tests cannot establish those results.

## Limits

Changes to a target listener can affect that listener's own connections. Explicit
administrator stop/restart and node upgrades are also interruption boundaries.
An unsafe live engine change is rejected for maintenance instead of falling back
to a full restart of a healthy process.

Limits are installed before engine activation. A rejected engine change can
briefly change the affected target's limits before recovery. Sudden host/power
failure or an unwritable disk is not an atomic transaction across the engine
state file and the TC state file. Recovery errors are surfaced and further
changes are blocked; no guarantee of zero packet loss under arbitrary host or
kernel failure is made.
