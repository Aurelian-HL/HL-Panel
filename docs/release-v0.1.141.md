# v0.1.141

Fix the first rule deletion after upgrading a node with legacy Xray outbound
tags. Previously, content-versioned tags caused otherwise identical outbounds
to be removed and added in bulk. On a node with roughly 440 listeners and 1,200
outbounds, one deletion could schedule more than a thousand unrelated handler
replacements and leave the live transaction uncertain after an API failure.

The agent now retains installed leaf tags when their settings and accounting
identity are unchanged, and rewrites their routing references consistently.
Actual credential, destination, or accounting changes still receive new tags
and use the existing make-before-break path. Unknown outbound extensions and
balancers are not rewritten.

Handler API changes and compensation are bounded to batches of 64. Failure
messages identify the operation and batch range without exposing configuration,
credentials, or CLI output. An uncertain RPC still blocks further mutations and
preserves the running core; it never silently falls back to a full restart.

Validation covers partial batch application and compensation, alias collisions,
unchanged accounting, and a real Xray 26.7.28 process with 440 listeners and
1,200 outbounds. The migration/deletion/addition test verifies an unchanged PID
and an established TCP connection throughout ordinary rule operations.

Xray remains pinned to 26.7.28. Existing subscriptions, UUIDs, and Reality
identities are unchanged. Installing this release uses the normal system
updater and can briefly interrupt connections during the version upgrade.
An already uncertain process requires this explicit maintenance step; this
release does not pretend that clearing a flag establishes its runtime state.

This release does not change HT customer deletion responses or include the
separate, unfinished hourly/daily traffic-ranking feature.
