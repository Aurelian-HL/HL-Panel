# v0.1.144

Node updates provision missing traffic-control capability for existing installs
without overwriting explicit settings. Recovery and rollback change only owned
filter handles, retain unrelated filters at the same priority and restore the
verified generation's limits before activating its listeners. Invalid or stale
desired configurations are rejected before changing limits or usage state.
Windows packaging and package verification include the required TC configurator,
matching the Linux release workflow and node installer.

Agent stop/restart commands are durably claimed before execution. Repeated IDs
report the saved result; interrupted or unrecordable execution is rejected
instead of automatically repeating a destructive action. This guarantee applies
to the single Agent instance and its state store. The bounded journal never
silently evicts IDs; a full or invalid journal requires maintenance while
existing forwarding and status requests remain available.

A full apply-receipt queue rejects new generations before engine or TC changes.
Delivery frees capacity for a later retry. Legacy pending transactions can
recover after all owned processes have exited; a live or partly live engine
keeps its receipts and serving components. Crash recovery aligns TC state with
the validated pending or last-known-good engine generation.

Panel subaccounts use the existing login form, with role-appropriate routing
and working personal-center access. Probe presentation and installation
acceptance retain the configured egress interface.

Client UUIDs, Reality parameters and subscription URLs are retained. Xray stays
pinned to 26.7.28, with 25.10.15 client compatibility acceptance. Normal rule
changes retain the existing live-update boundary; this release does not add a
whole-engine restart fallback for unsupported changes. A software upgrade is
still maintenance and can interrupt the node's current connections.

Release acceptance includes real loopback Xray/GOST sessions, reliable UDP,
durable command replay, receipt backpressure, crash recovery, Linux TC,
fresh-install and upgrade/rollback matrices. Publishing is gated by the release
workflow; local Windows checks alone do not establish Linux or production
acceptance.
