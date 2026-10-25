# Protocol health renewal capacity

Health proofs remain scoped to the rule and endpoint member. The 90-second
lease is renewed only after a fresh VLESS/Reality handshake and random echo.
Node heartbeat, TCP reachability, client readiness and cached successes cannot
grant protocol health.

The control plane now maintains a dedicated loopback Xray probe client instead
of creating one process for each challenge. Password-authenticated SOCKS users
select isolated per-target outbounds; the default outbound blocks traffic.
The process configuration is reused while the target set and binary metadata
are unchanged. Changes prepare a replacement before swapping it in; in-flight
checks retain their original client until completion. Preparation failure keeps
the previous client, and process failure is recovered on the next refresh.
Probe configuration files are private and removed when their client retires.

There are at most 64 concurrent network challenges, each with a ten-second
network deadline. Successful targets are scheduled again after 45 seconds.
The scheduler retains its independent eight-dispatch fairness burst so repeated
failures do not monopolize recovery. This is bounded capacity, not a guarantee
for arbitrary inventory sizes or widespread ten-second timeouts.

Local acceptance with the pinned Xray 26.7.28 prepared 2,561 routes in about
0.36 seconds. Two cycles of 2,304 successful targets mixed with 256 slow failed
targets completed in about 28.4 and 29.9 seconds. Invalid credentials could not
borrow another target's successful route. Lifecycle tests cover cancellation,
failed replacement, process crash, credential cleanup and concurrency limits.

This change owns only the panel probe process. It does not upgrade, restart or
reconfigure node forwarding engines. Production acceptance must compare node
process identities and subscription identities, and observe active deployable
targets over multiple renewal cycles. Historical proofs for paused rules are
not expected to renew and must not be counted as failed active checks.
