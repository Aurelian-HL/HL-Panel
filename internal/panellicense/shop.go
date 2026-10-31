package panellicense

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const RedemptionPurpose = "license-redemption"
const StockPath = "/api/v2/license/redemption-batches"

// ShopProduct describes an external card-store offer. Payment and delivery never
// run inside the forwarding panel. Empty links are valid for an unopened store.
type ShopProduct struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Term        Term   `json:"term"`
	PriceLabel  string `json:"price_label,omitempty"`
	PurchaseURL string `json:"purchase_url,omitempty"`
}

func DefaultShopProducts() []ShopProduct {
	return []ShopProduct{
		{ID: "monthly", Name: "月度授权", Term: Term{Kind: "calendar_months", Months: 1}},
		{ID: "quarterly", Name: "季度授权", Term: Term{Kind: "calendar_months", Months: 3}},
		{ID: "half-year", Name: "半年授权", Term: Term{Kind: "calendar_months", Months: 6}},
		{ID: "yearly", Name: "年度授权", Term: Term{Kind: "calendar_months", Months: 12}},
		{ID: "perpetual", Name: "永久授权", Term: Term{Kind: "perpetual"}},
	}
}

// SafePurchaseURL rejects executable, credential-bearing and relative URLs.
func SafePurchaseURL(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && !strings.ContainsAny(value, "\r\n\t")
}

func ValidateShopProducts(items []ShopProduct) error {
	if len(items) > 20 {
		return errors.New("too many shop products")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.ID == "" || len(item.ID) > 40 || seen[item.ID] || strings.TrimSpace(item.Name) == "" || !utf8.ValidString(item.Name+item.PriceLabel) || utf8.RuneCountInString(item.Name) > 50 || utf8.RuneCountInString(item.PriceLabel) > 40 || !SafePurchaseURL(item.PurchaseURL) || item.Term.Validate() != nil || item.Term.Kind == "until" {
			return errors.New("invalid shop product")
		}
		for _, c := range item.ID {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("invalid shop product id")
			}
		}
		for _, c := range item.Name + item.PriceLabel {
			if c < 32 || c == 127 {
				return errors.New("invalid shop product text")
			}
		}
		seen[item.ID] = true
	}
	return nil
}

type ShopCatalog struct {
	Purpose   string        `json:"purpose"`
	Version   int           `json:"version"`
	Issuer    string        `json:"issuer"`
	Product   string        `json:"product"`
	Audience  string        `json:"audience"`
	RequestID string        `json:"request_id"`
	IssuedAt  time.Time     `json:"issued_at"`
	ExpiresAt time.Time     `json:"expires_at"`
	Products  []ShopProduct `json:"products"`
}

// A signed attempt is safe to persist before commit: it contains a random proof
// and a one-way code hash, never the purchased card secret. It is also the
// recovery receipt when the first successful response is lost.
type RedemptionAttempt struct {
	Challenge
	CodeHash string `json:"code_hash"`
}

func ValidRedemptionProof(a RedemptionAttempt) bool {
	nonce := func(value string) bool {
		raw, err := base64.RawURLEncoding.DecodeString(value)
		return err == nil && len(raw) == 24 && base64.RawURLEncoding.EncodeToString(raw) == value
	}
	digest, err := hex.DecodeString(a.CodeHash)
	return a.Purpose == RedemptionPurpose && a.VerificationMethod == "https-01" &&
		a.DNSName == "" && a.DNSValue == "" && strings.HasPrefix(a.ReservationID, "redeem_") &&
		nonce(strings.TrimPrefix(a.ReservationID, "redeem_")) &&
		a.HTTPPath == InstallProofPath+a.ReservationID && strings.HasPrefix(a.HTTPValue, "hl-license-v2=") &&
		nonce(strings.TrimPrefix(a.HTTPValue, "hl-license-v2=")) && err == nil && len(digest) == 32 &&
		hex.EncodeToString(digest) == a.CodeHash
}

type StockBatchRequest struct {
	ProductID string `json:"product_id"`
	Count     int    `json:"count"`
	Actor     Actor  `json:"actor"`
	Reason    string `json:"reason"`
}
type StockActionRequest struct {
	Actor  Actor  `json:"actor"`
	Reason string `json:"reason"`
}
type StockBatch struct {
	ID              string    `json:"id"`
	ProductID       string    `json:"product_id"`
	Term            Term      `json:"term"`
	Count           int       `json:"count"`
	Disabled        bool      `json:"disabled"`
	CreatedAt       time.Time `json:"created_at"`
	ExportExpiresAt time.Time `json:"export_expires_at"`
	ExportConfirmed bool      `json:"export_confirmed"`
	Actor           Actor     `json:"actor"`
	Reason          string    `json:"reason"`
}
type RedemptionRecord struct {
	ID                string    `json:"id"`
	BatchID           string    `json:"batch_id"`
	CodePrefix        string    `json:"code_prefix"`
	ProductID         string    `json:"product_id"`
	LicenseID         string    `json:"license_id"`
	Domain            string    `json:"domain"`
	InstanceID        string    `json:"instance_id"`
	InstancePublicKey string    `json:"instance_public_key"`
	RedeemedAt        time.Time `json:"redeemed_at"`
	VerifiedIPs       []string  `json:"verified_ips"`
	IPSource          string    `json:"ip_source"`
}
