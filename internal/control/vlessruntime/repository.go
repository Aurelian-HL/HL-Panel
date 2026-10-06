package vlessruntime

import (
	"context"

	"github.com/hongle/hl-panel/internal/control/audit"
)

// Repository is intentionally explicit about secret-bearing operations.
// ListPublicRuntimeMaterials is safe for inventory pages; RuntimeMaterialForNode
// is reserved for the node configuration compiler and is never an HTTP route.
type Repository interface {
	ListPublicRuntimeMaterials(context.Context) ([]PublicMaterial, error)
	ListPublicRuntimeMaterialsForAdministrator(context.Context, string) ([]PublicMaterial, error)
	RuntimeMaterialForNode(context.Context, string, string) (Material, error)
	SaveRuntimeMaterial(context.Context, SaveInput, audit.Event) (PublicMaterial, bool, error)
	RevokeRuntimeMaterial(context.Context, RevokeInput, audit.Event) (PublicMaterial, bool, error)
}
