package panellicenseissuer

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"time"
)

func validateProducts(items []p.ShopProduct) error {
	return p.ValidateShopProducts(items)
}
func (s *Service) exportCipher() (cipher.AEAD, error) {
	if len(s.config.ExportKey) != 32 {
		return nil, problem("inventory_unavailable", "inventory export key is not configured", 503)
	}
	block, err := aes.NewCipher(s.config.ExportKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (s *Service) CreateStockBatch(ctx context.Context, key string, input p.StockBatchRequest) (p.StockBatch, error) {
	var result p.StockBatch
	if err := validateKey(key); err != nil {
		return result, err
	}
	if err := validateActor(input.Actor, input.Reason); err != nil {
		return result, err
	}
	if input.Count < 1 || input.Count > 1000 {
		return result, invalid("batch size must be between 1 and 1000")
	}
	var product *p.ShopProduct
	for _, item := range s.config.Products {
		if item.ID == input.ProductID {
			product = &item
			break
		}
	}
	if product == nil {
		return result, invalid("unknown shop product")
	}
	aead, err := s.exportCipher()
	if err != nil {
		return result, err
	}
	digest, err := fingerprint("stock-create", input)
	if err != nil {
		return result, err
	}
	raw, _, err := s.store.Run(ctx, "stock-admin:"+key, digest, func(tx transaction) ([]byte, error) {
		id, err := randomID("batch_")
		if err != nil {
			return nil, err
		}
		now := s.now()
		record := p.StockBatch{ID: id, ProductID: product.ID, Term: product.Term, Count: input.Count, CreatedAt: now, ExportExpiresAt: now.Add(24 * time.Hour), Actor: input.Actor, Reason: input.Reason}
		codes := make([]string, input.Count)
		for index := range codes {
			codes[index], err = randomID("hlr1_")
			if err != nil {
				return nil, err
			}
		}
		plaintext, err := json.Marshal(codes)
		if err != nil {
			return nil, err
		}
		encrypted := aead.Seal(nil, nil, plaintext, []byte(id))
		clear(plaintext)
		if err = tx.SaveStockBatch(ctx, stockBatch{Record: record, Ciphertext: encrypted}); err != nil {
			return nil, err
		}
		for _, code := range codes {
			if err = tx.SaveStockCode(ctx, stockCode{Hash: hash(code), BatchID: id, Prefix: code[:10]}); err != nil {
				return nil, err
			}
		}
		if err = tx.StockAudit(ctx, id, "create", p.StockActionRequest{Actor: input.Actor, Reason: input.Reason}, now); err != nil {
			return nil, err
		}
		return json.Marshal(record)
	})
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}

// Export is repeatable for 24 hours, so a lost download never causes a new batch
// to be minted. Neither the replay journal nor audit rows contain plaintext.
func (s *Service) ExportStockBatch(ctx context.Context, id string, input p.StockActionRequest) ([]string, error) {
	if err := validateActor(input.Actor, input.Reason); err != nil {
		return nil, err
	}
	aead, err := s.exportCipher()
	if err != nil {
		return nil, err
	}
	var codes []string
	err = s.store.Transact(ctx, func(tx transaction) error {
		batch, err := tx.StockBatch(ctx, id, true)
		if err != nil {
			return err
		}
		if batch.Record.Disabled || batch.Record.ExportConfirmed || !batch.Record.ExportExpiresAt.After(s.now()) || len(batch.Ciphertext) == 0 {
			return problem("export_closed", "inventory export has expired or was confirmed", 409)
		}
		plaintext, err := aead.Open(nil, nil, batch.Ciphertext, []byte(id))
		if err != nil {
			return errors.New("cannot decrypt inventory export")
		}
		defer clear(plaintext)
		if err = json.Unmarshal(plaintext, &codes); err != nil {
			return err
		}
		return tx.StockAudit(ctx, id, "export", input, s.now())
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}
func (s *Service) StockAction(ctx context.Context, id, action, key string, input p.StockActionRequest) (p.StockBatch, error) {
	var result p.StockBatch
	if action != "disable" && action != "confirm-export" {
		return result, invalid("unknown inventory action")
	}
	if err := validateKey(key); err != nil {
		return result, err
	}
	if err := validateActor(input.Actor, input.Reason); err != nil {
		return result, err
	}
	digest, err := fingerprint("stock-"+id+"/"+action, input)
	if err != nil {
		return result, err
	}
	raw, _, err := s.store.Run(ctx, "stock-admin:"+key, digest, func(tx transaction) ([]byte, error) {
		batch, err := tx.StockBatch(ctx, id, true)
		if err != nil {
			return nil, err
		}
		if action == "disable" {
			batch.Record.Disabled = true
		} else {
			batch.Record.ExportConfirmed = true
		}
		batch.Ciphertext = nil
		if err = tx.SaveStockBatch(ctx, batch); err != nil {
			return nil, err
		}
		if err = tx.StockAudit(ctx, id, action, input, s.now()); err != nil {
			return nil, err
		}
		return json.Marshal(batch.Record)
	})
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
