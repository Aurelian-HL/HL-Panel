# control-api

The control API supports PostgreSQL transactional snapshot storage for a
single-instance trial. Configure exactly one database source:

- `CONTROL_DATABASE_URL_FILE`: path to a protected file containing the URL;
  preferred for deployment, with `root:nyvless` ownership and mode `0640`.
- `CONTROL_DATABASE_URL`: a direct URL, useful for an isolated test environment;
  never print it or place it in command arguments, Git, or ordinary logs.

Keep `CONTROL_ALLOW_VOLATILE_STORE=false` for an Internet-facing deployment.
Without a database URL, startup requires explicit
`CONTROL_ALLOW_VOLATILE_STORE=true`; this local-only fallback loses sessions,
nodes, groups, revisions, and audit events on restart. A configured database
failure never falls back to an empty in-memory store.

The PostgreSQL adapter reads the complete snapshot for each operation and locks
the single row before a transactional write. Its pool allows four open and two
idle connections, operations have a ten-second timeout, and the snapshot is
limited to 16 MiB. It persists identities, sessions, routing configuration,
idempotency records, and audit state, but is not the planned normalized
relational repository or a high-throughput store. Startup creates the
`nyvp_control_snapshots` table when needed; the repository-level `migrations/`
files describe the future relational model and must not be run for this adapter.
Invalid or incompatible stored snapshots fail closed rather than bootstrapping
an empty database. Snapshot bytes and backups contain confidential records.

The usage ledger is a separate PostgreSQL schema and is required whenever a
database URL is configured. Run the release's `bin/usage-migrate` before
starting `control-api` and verify it against the same protected DSN file:

```text
bin/usage-migrate apply -dsn-file /etc/nyvp/database-url
bin/usage-migrate verify -dsn-file /etc/nyvp/database-url
```

If the ledger is missing or drifted, `control-api` refuses to start. The
`usage-migrate` command obtains its own advisory lock and does not restart or
reconfigure any service.

Generate a bootstrap password hash without placing the password in a command
line argument or log:

```powershell
$password = Read-Host -AsSecureString
$plain = [System.Net.NetworkCredential]::new('', $password).Password
$plain | go run ./apps/control-api hash-password
Remove-Variable plain, password
```

The command reads one line from standard input and writes only the versioned
PBKDF2-HMAC-SHA256 hash. Configure exactly one of:

- `CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH`
- `CONTROL_BOOTSTRAP_ADMIN_PASSWORD_FILE`
- `CONTROL_BOOTSTRAP_ADMIN_PASSWORD`

The plain environment-variable option is intended only for disposable local
development. Production should use the versioned hash or a protected password
file. The stored format records its algorithm, version, iteration count, salt,
and digest. A later production hardening phase can add an Argon2id format and
rehash PBKDF2 credentials after successful login.

The bootstrap username and exactly one password source remain required at
startup. Bootstrap creates an administrator only when the database is empty;
changing its environment values does not rotate an administrator already stored
in PostgreSQL. Protect the populated environment file and never include it in a
release archive.

The server listens on `127.0.0.1:8080` by default. Configure
`CONTROL_TLS_CERT_FILE` and `CONTROL_TLS_KEY_FILE` for direct TLS. Binding a
plaintext listener outside loopback requires the explicit, development-only
`CONTROL_ALLOW_INSECURE_HTTP=true` setting.

`GET /healthz` checks process liveness only. It does not query PostgreSQL;
durability verification also requires real login, authenticated reads before and
after a restart, and a backup restored into an isolated database. See the
[deployment manual](../../deploy/README.md) and
[backup and restore procedure](../../deploy/backup-and-restore.md). The current
trial still lacks the complete customer identity, secret distribution,
subscription issuance, and real engine activation workflow.
