# v0.1.143

New device-group members now receive independently revocable copies of validated
imported VLESS runtime material from active peers. Existing incomplete members
can be repaired by repeating their unchanged membership operation. Conflicting
peer imports stop the mutation; revoked local material is never reactivated.
Existing client identities, Reality parameters and subscription URLs are retained.

Fresh node installation configures the real traffic-control interface and the
limited systemd capability required for per-rule speed limits. Explicit custom
settings remain intact. Capability changes trigger durable configuration
reconciliation, so limited rules are delivered when a node gains TC support.
Existing nodes without TC configuration require the packaged configuration
helper and an Agent restart during maintenance; upgrading the binary alone
does not silently grant network privileges.

Validation covers token enrollment, existing-member repair, persistence,
idempotency, revocation, conflicting imports, transaction rollback and actual
systemd installation. Xray remains pinned to 26.7.28 for client compatibility.
