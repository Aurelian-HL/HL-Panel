# v0.1.154: reserve the rule-create persistence budget

Under continuous traffic or protocol-health writes, a prepared business
transaction can become stale before its commit check. Two complete speculative
builds followed by the serialized fallback can exhaust the existing ten-second
deadline even when the fallback alone could commit.

After a stale attempt, compare its observed duration with the remaining
deadline. Skip the next speculative build when it would consume the time
reserved for the serialized fallback and scheduling variation. Cheap attempts
retain the existing two-attempt limit. Snapshot revision, transaction identity,
traffic and health projection checks remain mandatory.

Persistence failures now include bounded preparation attempt counts and
preparation, commit-check and fallback durations. These diagnostics contain no
request bodies, SQL, credentials or customer identifiers. Public validation
errors and cancellation identity retain their existing behavior. A failed
COMMIT is never treated as permission to replay a business mutation.

The regression test uses a disposable PostgreSQL database, concurrent traffic
and health updates, and an intentionally slow private operation. It verifies
that the fallback fits the request budget, preserves the latest accepted
telemetry and writes exactly one audit entry. Existing commit-failure and
idempotency tests remain part of release acceptance.

The historical production error's final stage does not establish where its
entire request time was spent. This release fixes a reproduced budget hazard;
the new timings and the original operation's result provide further evidence.

No database format, customer endpoint, subscription credential, engine or
agent lifecycle change is included. Deploy the control plane only for this
incident. Shop and redemption-code work is excluded.
