package panellicenseissuer

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

//go:embed schema.sql
var schema string

type Postgres struct{ pool *pgxpool.Pool }

func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid issuer database configuration")
	}
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	store := &Postgres{pool: pool}
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(674486285292)"); err == nil {
		_, err = tx.Exec(ctx, schema)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}
func (s *Postgres) Close() { s.pool.Close() }
func digestLock(key string) int64 {
	h := sha256.Sum256([]byte(key))
	return int64(binary.BigEndian.Uint64(h[:8]))
}

type pgTransaction struct{ tx pgx.Tx }

func checkReplay(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key, digest string) ([]byte, bool, error) {
	var stored string
	var result []byte
	err := db.QueryRow(ctx, "SELECT digest,result FROM hl_license_v2.operations WHERE key=$1", key).Scan(&stored, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored != digest {
		return nil, false, ErrIdempotencyConflict
	}
	return result, true, nil
}
func (s *Postgres) Replay(ctx context.Context, key, digest string) ([]byte, bool, error) {
	return checkReplay(ctx, s.pool, key, digest)
}
func (s *Postgres) Run(ctx context.Context, key, digest string, fn func(transaction) ([]byte, error)) ([]byte, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", digestLock(key)); err != nil {
		return nil, false, err
	}
	if result, found, err := checkReplay(ctx, tx, key, digest); err != nil || found {
		return result, found, err
	}
	result, err := fn(&pgTransaction{tx})
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO hl_license_v2.operations(key,digest,result) VALUES($1,$2,$3)", key, digest, result); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return result, false, nil
}
func decodeLicense(row pgx.Row) (license, error) {
	var raw []byte
	err := row.Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return license{}, ErrNotFound
	}
	if err != nil {
		return license{}, err
	}
	var l license
	err = json.Unmarshal(raw, &l)
	return l, err
}
func (t *pgTransaction) Get(ctx context.Context, id string) (license, error) {
	return decodeLicense(t.tx.QueryRow(ctx, "SELECT document FROM hl_license_v2.licenses WHERE id=$1 FOR UPDATE", id))
}
func (t *pgTransaction) ByCode(ctx context.Context, hash string) (license, error) {
	l, err := decodeLicense(t.tx.QueryRow(ctx, "SELECT document FROM hl_license_v2.licenses WHERE code_hash=$1 FOR UPDATE", hash))
	if errors.Is(err, ErrNotFound) {
		return l, ErrInvalidCode
	}
	return l, err
}
func (t *pgTransaction) ClaimDomain(ctx context.Context, domain, id string) error {
	_, err := t.tx.Exec(ctx, "INSERT INTO hl_license_v2.domains(domain,license_id) VALUES($1,$2) ON CONFLICT(domain) DO NOTHING", domain, id)
	if err != nil {
		return err
	}
	var owner string
	if err = t.tx.QueryRow(ctx, "SELECT license_id FROM hl_license_v2.domains WHERE domain=$1", domain).Scan(&owner); err != nil {
		return err
	}
	if owner != id {
		return ErrDomainConflict
	}
	return nil
}
func (t *pgTransaction) Save(ctx context.Context, l license) error {
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `INSERT INTO hl_license_v2.licenses(id,domain,code_hash,document) VALUES($1,$2,$3,$4)
 ON CONFLICT(id) DO UPDATE SET domain=excluded.domain,code_hash=excluded.code_hash,document=excluded.document`, l.Record.ID, l.Record.Domain, l.CodeHash, raw)
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return ErrDomainConflict
	}
	return err
}
func decodeReservation(row pgx.Row) (reservation, error) {
	var raw []byte
	err := row.Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return reservation{}, ErrChallengeExpired
	}
	if err != nil {
		return reservation{}, err
	}
	var r reservation
	err = json.Unmarshal(raw, &r)
	return r, err
}
func (t *pgTransaction) Reservation(ctx context.Context, id string) (reservation, error) {
	return decodeReservation(t.tx.QueryRow(ctx, "SELECT document FROM hl_license_v2.reservations WHERE id=$1", id))
}
func (s *Postgres) Reservation(ctx context.Context, id string) (reservation, error) {
	return decodeReservation(s.pool.QueryRow(ctx, "SELECT document FROM hl_license_v2.reservations WHERE id=$1", id))
}
func (t *pgTransaction) FindReservation(ctx context.Context, id, instance, key string, now time.Time) (reservation, bool, error) {
	r, err := decodeReservation(t.tx.QueryRow(ctx, `SELECT document FROM hl_license_v2.reservations WHERE license_id=$1 AND instance_id=$2 AND public_key=$3 AND expires_at>$4 ORDER BY expires_at DESC LIMIT 1`, id, instance, key, now))
	if errors.Is(err, ErrChallengeExpired) {
		return r, false, nil
	}
	return r, err == nil, err
}
func (t *pgTransaction) SaveReservation(ctx context.Context, r reservation) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, "INSERT INTO hl_license_v2.reservations(id,license_id,instance_id,public_key,expires_at,document) VALUES($1,$2,$3,$4,$5,$6)", r.ID, r.LicenseID, r.InstanceID, r.PublicKey, r.ExpiresAt, raw)
	return err
}
func (t *pgTransaction) ClearReservations(ctx context.Context, id string) error {
	_, err := t.tx.Exec(ctx, "DELETE FROM hl_license_v2.reservations WHERE license_id=$1", id)
	return err
}
func (t *pgTransaction) Audit(ctx context.Context, e p.AuditEvent) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, "INSERT INTO hl_license_v2.audit(id,license_id,at,document) VALUES($1,$2,$3,$4)", e.ID, e.LicenseID, e.At, raw)
	return err
}
func (s *Postgres) List(ctx context.Context) ([]p.LicenseRecord, error) {
	rows, err := s.pool.Query(ctx, "SELECT document FROM hl_license_v2.licenses ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []p.LicenseRecord{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var l license
		if err = json.Unmarshal(raw, &l); err != nil {
			return nil, err
		}
		items = append(items, l.Record)
	}
	return items, rows.Err()
}
func (s *Postgres) Audit(ctx context.Context, id string) ([]p.AuditEvent, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM hl_license_v2.licenses WHERE id=$1)", id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, "SELECT document FROM hl_license_v2.audit WHERE license_id=$1 ORDER BY at,id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []p.AuditEvent{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e p.AuditEvent
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}
