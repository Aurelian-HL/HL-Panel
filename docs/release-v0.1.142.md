# v0.1.142

Includes the v0.1.141 rule-deletion recovery fixes, with compatibility for legal
Xray configurations that use an unnamed default outbound. Such configurations
remain byte-for-byte intact; staged, active and running-process hash checks are
still enforced. The release retains the pinned Xray 26.7.28 engine.

The v0.1.141 candidate failed real-engine recovery/exit integration checks and
was never published or deployed. This version must pass the full release gates
before production upgrade.
