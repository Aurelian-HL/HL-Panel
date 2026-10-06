# Usage ledger migration command

This command applies only `000003_usage_ledger`. It does not start, stop, or
reconfigure the control plane or any data-plane process.

Prefer a protected DSN file. A configured file source always wins over a direct
DSN source, and the command never prints the DSN:

```text
set USAGE_MIGRATION_DSN_FILE=C:\protected\nyvp-postgres-url
go run ./apps/usage-migrate status
go run ./apps/usage-migrate apply
go run ./apps/usage-migrate verify
go run ./apps/usage-migrate rollback
```

`CONTROL_DATABASE_URL_FILE` and `DSN_FILE` are also accepted as file-source
fallbacks. `USAGE_MIGRATION_DSN` and `CONTROL_DATABASE_URL` are direct-string
fallbacks for isolated development only.

`rollback` refuses to remove a ledger containing events, enforcement decisions,
or enforcement results. `rollback -force` is the explicit destructive override.
Before applying or rolling back, the command obtains a PostgreSQL advisory lock,
validates the embedded migration checksum and state row, and executes the schema
and state changes in one transaction.
