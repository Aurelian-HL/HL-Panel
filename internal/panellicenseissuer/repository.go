package panellicenseissuer

import (
	"context"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"time"
)

type license struct {
	Record            p.LicenseRecord `json:"record"`
	CodeHash          string          `json:"code_hash"`
	InstancePublicKey string          `json:"instance_public_key,omitempty"`
}
type reservation struct {
	ID         string    `json:"id"`
	LicenseID  string    `json:"license_id"`
	Domain     string    `json:"domain"`
	InstanceID string    `json:"instance_id"`
	PublicKey  string    `json:"public_key"`
	CodeHash   string    `json:"code_hash"`
	DNSValue   string    `json:"dns_value"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Every callback executes in one transaction together with audit and replay data.
type transaction interface {
	Get(context.Context, string) (license, error)
	ByCode(context.Context, string) (license, error)
	ByDomain(context.Context, string) (license, error)
	ClaimDomain(context.Context, string, string) error
	Save(context.Context, license) error
	Reservation(context.Context, string) (reservation, error)
	FindReservation(context.Context, string, string, string, time.Time) (reservation, bool, error)
	SaveReservation(context.Context, reservation) error
	ClearReservations(context.Context, string) error
	Audit(context.Context, p.AuditEvent) error
}
type repository interface {
	Run(context.Context, string, string, func(transaction) ([]byte, error)) ([]byte, bool, error)
	Replay(context.Context, string, string) ([]byte, bool, error)
	Reservation(context.Context, string) (reservation, error)
	List(context.Context) ([]p.LicenseRecord, error)
	Audit(context.Context, string) ([]p.AuditEvent, error)
}
