# NY current static UI parity evidence

Date: 2026-10-03

This file freezes observable UI contracts from the current public static bundle
served by `zf.hongle.work`. The bundle was downloaded with unauthenticated GET
requests only. No business API mutation, login, node synchronization, service
restart, DNS change, or failure test was performed against the protected site.

Local evidence copies:

- `work/ny-static-reference-20261003/index-DkqF5UhK.js`
  - SHA-256: `030E3E08B29135F4531E25EC757045493FD7F78A4E2177351941AB29CB51A9AF`
- `work/ny-static-reference-20261003/index-DVfozmhv.css`
  - SHA-256: `627578E4C222521F284FAA3F872F5D41500CB2FE0914F364009B2AD564EB5DDA`

The static bundle is reference evidence, not source code for this clean-room
implementation. Private implementation details and unknown server behavior must
not be inferred from minified code.

## Product scope fixed by the operator

The rebuilt product must retain the following NY surfaces and their working
behavior:

- Home and customer profile.
- Forwarding rules and rule grouping.
- Customer-owned single-ended exits.
- LookingGlass.
- Node status, enrollment, diagnostics, and version visibility.
- Administrator dashboard.
- Site settings and announcements.
- User and user-group administration.
- Device-group administration.

The following product domains and their dependent UI, routes, permissions,
storage, metrics, and actions are deliberately excluded:

- Shop.
- Push channels, Telegram binding, and offline push subscriptions.
- Orders.
- Plans, purchases, renewals, and auto-renew.
- Wallet, recharge, payment channels, and commercial accounting.
- Redemption codes.
- Invitation records, invitation codes, rebates, and commissions.

The exclusion is domain-wide. For example, the user profile must not retain a
dead renew button or Telegram card after the backing domains are removed.

## Observed routes

| Current NY route | Label | Rebuild decision |
| --- | --- | --- |
| `/` | Home | Required |
| `/login` | Login or registration | Required; registration is settings-driven |
| `/userinfo` | Profile | Required, with excluded commercial cards removed |
| `/forward_rules` | Forwarding rules | Required |
| `/device_group` | Single-ended tunnel | Required as customer-owned exit |
| `/looking_glass` | LookingGlass | Required |
| `/admin/main` | Dashboard | Required |
| `/admin/settings` | Site settings | Required, excluding payment/push/invitation settings |
| `/admin/users` | User administration | Required |
| `/admin/user_group` | User-group administration | Required |
| `/admin/device_group` | Device-group administration | Required |
| `/shop` | Shop | Excluded |
| `/orders`, `/admin/orders` | Orders | Excluded |
| `/admin/plans` | Plans | Excluded |
| `/admin/push_settings` | Push settings | Excluded |
| `/admin/afflog` | Invitation records | Excluded |
| `/admin/redeem` | Redemption codes | Excluded |

The current bundle also exposes old and new node-status links outside the SPA.
The rebuild must provide one integrated node-status page rather than preserve
two legacy links.

## Exact business vocabulary

Device-group types visible in the current bundle:

- Monitor only.
- Entry.
- Site-owned exit.
- Customer-owned single-ended exit.
- Chain exit.

Forwarding rule synchronization states are independent from operator pause:

- Unsynchronized.
- Normal.
- Synchronization failed.
- Disabled.

Target selection policies visible in the bundle:

- Random.
- Round robin.
- IP hash.
- Least connections/load.
- Failover.

Entry direct policy has three states:

- Direct disabled.
- Direct optional.
- Direct forced.

## Required forwarding-rule workflow

The normal editor order and labels must remain familiar:

1. Name.
2. Entry device group and entry connection information.
3. Listen port; empty requests an allocated port.
4. Target addresses, one per line.
5. Advanced options.

The rebuild adds an explicit path selector so operators cannot confuse entry
direct with an entry-to-exit path. Both modes still require an entry group.

Advanced options observed in the current bundle and therefore required:

- Target load-balancing policy.
- Receive Proxy Protocol: off or TCP.
- Send Proxy Protocol: off, v1 TCP, v2 TCP+UDP, or v2 TCP.
- Rule speed limit.
- Rule IP limit.
- Rule connection limit.
- TLS inbound mode and SNI settings.

TLS editing includes no TLS, parent rule, shared child rule, shared parent port,
SNI list, empty-SNI policy, and origin-fetch TLS behavior. These fields need a
validated structured model. They must not be stored as arbitrary engine JSON.

Rule list actions observed and required:

- Add, edit, copy, delete, pause, and resume.
- Search by rule name, exact listen port, target host/port, entry, and exit.
- Batch import, import-as-update, and JSON export.
- Batch pause, resume, delete, move group, change entry/exit, and update advanced
  limits.
- Reset traffic.
- Diagnose entry and exits, with per-group failure details.
- Show actual traffic and independent synchronization/pause state.

## Required device-group workflow

The administrator table exposes group ID, name, authorized user-group IDs,
type, connection address, multiplier, online devices, and operations.

Required group configuration includes:

- Multiple allowed listen-port ranges for an entry.
- Authorized user groups.
- Entry allowed-exit restrictions.
- Exit allowed-entry restrictions.
- Three-state direct policy.
- Traffic multiplier.
- Fallback exit group.
- Entry policy: UDP, inbound filtering, TLS requirements, IPv6 selection, and
  failover thresholds.
- Site-owned exit, customer-owned exit, monitor-only, and two-to-three-hop chain
  semantics with duplicate/loop rejection.
- Per-group node offline grace and retention overrides.

Enrollment must keep NY's group-first workflow: select a group, obtain an online
installer or offline package, enroll a distinct machine credential, and show
the result in node status. A shared long-lived group secret is not acceptable.

## Required customer and user-group workflow

User information and administrator editing must cover:

- UID, username, role/administrator state, user group, banned state, and note.
- Expiry, actual traffic, charged quota, maximum rules, used rules.
- Speed, IP, and connection limits.
- Password change/reset without returning hashes or plaintext.
- Rule management shortcut and customer-owned exit permission.

Commercial fields such as plan, balance, renewal price, auto-renew, Telegram,
recharge, and invitation settings are excluded.

User-group zero remains unable to use forwarding. User-group maintenance must
support name/description, stable numeric or mapped identity, ordering, resource
authorization, customer count, and safe reference checks on deletion.

## Required operations and settings

Dashboard data retained from the current NY surface:

- Total users.
- Today/yesterday actual traffic.
- Today/yesterday user and node traffic rankings.
- Traffic ingestion queue state.
- Node online/synchronization status.

Commercial recharge metrics are excluded.

Site settings retained:

- Site name and announcement.
- Registration and challenge policy.
- Theme policy and separate landscape/portrait background URLs.
- LookingGlass policy.
- Diagnostic IP hiding.
- Customer-owned exit policy.
- API rate limits.
- Global and per-group node offline grace/retention policy.

LookingGlass accepts only validated hostnames or IP addresses and bounded ping
tasks. Node diagnostics and upgrades use an allow-listed task model with audit,
timeouts, and redaction. NY's arbitrary WebSSH behavior is intentionally not
copied because it conflicts with this repository's security boundary.

## Acceptance rule

A parity item is complete only when its responsive UI, authorized API, durable
state, audit event, runtime effect where applicable, and browser or real-client
verification all pass. A copied label, empty page, saved form, generated config,
or successful build alone is not completion evidence.
