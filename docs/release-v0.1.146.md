# v0.1.146

Fix node enrollment for larger device groups. The durable hot snapshot and its
PostgreSQL constraint now share the existing codec's 64 MiB bound. Startup
migrates the legacy 16 MiB constraint under the bootstrap transaction and lock;
it does not delete customer data or configuration history. Regression coverage
recreates the legacy schema and verifies enrollment and restart with 25 MiB of
synthetic state in isolated PostgreSQL.

Official node packages now include geoip.dat and geosite.dat from the same
checksum-pinned Xray archive. Fresh installation and reinstallation provision
the resources next to Xray; incomplete packages are rejected before node files
are installed. Resumed enrollment also verifies the installed resources.

Xray remains fixed at 26.7.28. Existing engines and node identities do not need
replacement for the control-plane capacity migration. This release includes
v0.1.145's customer-date reminder behavior and retains the live-update boundary
without introducing an engine restart fallback.
