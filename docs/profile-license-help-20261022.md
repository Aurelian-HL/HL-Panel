# Personal center and license help

The domain-license card now lives in the administrator's personal center at
`/userinfo`. Overview and system settings no longer repeat it. As of October 24,
account details, domain licensing, and password settings are three full-width
sections in one vertical stack. Information and password fields use horizontal
rows on wide desktops and collapse independently on smaller screens. See
`profile-layout-20261024.md` for the current layout acceptance.

The former support/renewal link is a local “寻求帮助” control in the card and
expiry reminder. It expands a contact placeholder without navigation or a
network request. License activation, synchronization, expiry enforcement and
the daily reminder dismissal remain available.

Historical October 22 validation: 360 web unit tests, production frontend build, and 51 synthetic
browser checks/screenshots at 1440, 1024 and 390 pixels. Synthetic states cover
activation, free and paid authorization, expiry, loading failures, help and
reminder keyboard behavior. Production acceptance is recorded separately.

HT business creation concurrency is a separate deployment, with no business
API dependency added to this control plane or node agents.
