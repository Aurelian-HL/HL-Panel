package panellicenseissuer

import (
	"context"
	"encoding/json"
	"errors"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"github.com/jackc/pgx/v5"
	"time"
)

type stockBatch struct {
	Record     p.StockBatch `json:"record"`
	Ciphertext []byte       `json:"ciphertext,omitempty"`
}
type stockCode struct {
	Hash       string
	BatchID    string
	Prefix     string
	Redemption *p.RedemptionRecord
}

func (s *Postgres) Transact(ctx context.Context, fn func(transaction) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = fn(&pgTransaction{tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SHARE locks permit different codes in the same batch to redeem concurrently,
// while serializing an administrative disable against in-flight redemptions.
func (t *pgTransaction) StockBatch(ctx context.Context, id string, exclusive bool) (stockBatch, error) {
	var b stockBatch
	var raw []byte
	lock := " FOR SHARE"
	if exclusive {
		lock = " FOR UPDATE"
	}
	err := t.tx.QueryRow(ctx, "SELECT document,export_ciphertext FROM hl_license_v2.stock_batches WHERE id=$1"+lock, id).Scan(&raw, &b.Ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(raw, &b.Record)
	}
	return b, err
}
func (t *pgTransaction) SaveStockBatch(ctx context.Context, b stockBatch) error {
	raw, err := json.Marshal(b.Record)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `INSERT INTO hl_license_v2.stock_batches(id,disabled,export_expires_at,export_ciphertext,document) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(id) DO UPDATE SET disabled=excluded.disabled,export_expires_at=excluded.export_expires_at,export_ciphertext=excluded.export_ciphertext,document=excluded.document`, b.Record.ID, b.Record.Disabled, b.Record.ExportExpiresAt, b.Ciphertext, raw)
	return err
}
func readStockCode(row pgx.Row) (stockCode, error) {
	var c stockCode
	var raw []byte
	err := row.Scan(&c.Hash, &c.BatchID, &c.Prefix, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrInvalidCode
	}
	if err == nil && len(raw) > 0 {
		err = json.Unmarshal(raw, &c.Redemption)
	}
	return c, err
}
func (s *Postgres) StockCode(ctx context.Context, h string) (stockCode, error) {
	return readStockCode(s.pool.QueryRow(ctx, "SELECT code_hash,batch_id,code_prefix,redemption FROM hl_license_v2.stock_codes WHERE code_hash=$1", h))
}
func (t *pgTransaction) StockCode(ctx context.Context, h string) (stockCode, error) {
	return readStockCode(t.tx.QueryRow(ctx, "SELECT code_hash,batch_id,code_prefix,redemption FROM hl_license_v2.stock_codes WHERE code_hash=$1 FOR UPDATE", h))
}
func (t *pgTransaction) SaveStockCode(ctx context.Context, c stockCode) error {
	if c.Redemption == nil {
		_, err := t.tx.Exec(ctx, "INSERT INTO hl_license_v2.stock_codes(code_hash,batch_id,code_prefix) VALUES($1,$2,$3)", c.Hash, c.BatchID, c.Prefix)
		return err
	}
	raw, err := json.Marshal(c.Redemption)
	if err != nil {
		return err
	}
	result, err := t.tx.Exec(ctx, "UPDATE hl_license_v2.stock_codes SET redemption=$1 WHERE code_hash=$2 AND batch_id=$3 AND redemption IS NULL", raw, c.Hash, c.BatchID)
	if err == nil && result.RowsAffected() != 1 {
		return problem("code_already_redeemed", "redemption code already used", 409)
	}
	return err
}
func (t *pgTransaction) StockAudit(ctx context.Context, id, action string, input p.StockActionRequest, now time.Time) error {
	raw, err := json.Marshal(struct {
		Action string `json:"action"`
		p.StockActionRequest
	}{action, input})
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, "INSERT INTO hl_license_v2.stock_audit(batch_id,at,document) VALUES($1,$2,$3)", id, now, raw)
	return err
}

// PurgeExpiredExports removes encrypted delivery material only, never inventory
// hashes, redemption receipts or licenses. Codes still work after export expiry.
func (s *Postgres) PurgeExpiredExports(ctx context.Context, now time.Time) error {
	_, err := s.pool.Exec(ctx, "UPDATE hl_license_v2.stock_batches SET export_ciphertext=NULL WHERE export_expires_at<=$1 AND export_ciphertext IS NOT NULL", now)
	return err
}
func (s *Postgres) StockBatches(ctx context.Context, now time.Time, offset int) ([]p.StockBatch, error) {
	if err := s.PurgeExpiredExports(ctx, now); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT document FROM hl_license_v2.stock_batches ORDER BY (document->>'created_at')::timestamptz DESC,id DESC LIMIT 100 OFFSET $1", offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []p.StockBatch{}
	for rows.Next() {
		var raw []byte
		var b p.StockBatch
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &b)
		}
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}
func (s *Postgres) Redemptions(ctx context.Context, id string) ([]p.RedemptionRecord, error) {
	rows, err := s.pool.Query(ctx, "SELECT redemption FROM hl_license_v2.stock_codes WHERE batch_id=$1 AND redemption IS NOT NULL ORDER BY code_hash LIMIT 1000", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []p.RedemptionRecord{}
	for rows.Next() {
		var raw []byte
		var r p.RedemptionRecord
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &r)
		}
		if err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
