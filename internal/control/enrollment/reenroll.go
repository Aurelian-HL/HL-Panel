package enrollment

import (
	"context"
	"strings"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/securetoken"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

// Reenroll requires both the current private node identity and a group token.
// It keeps the identity/usage history and atomically replaces group membership.
func (s *Service) Reenroll(ctx context.Context, authenticated nodes.Node, input EnrollInput) (nodes.Node, error) {
	if authenticated.ID == "" || authenticated.CredentialHash == "" || strings.TrimSpace(input.RawToken) == "" {
		return nodes.Node{}, faults.ErrUnauthorized
	}
	if err := validateNodeIdentity(input); err != nil {
		return nodes.Node{}, err
	}
	if input.DialHost != "" {
		input.DialHost, _ = serviceaddress.NormalizeHost(input.DialHost)
	}
	now := s.now().UTC()
	node := authenticated
	node.Hostname = strings.TrimSpace(input.Hostname)
	node.Platform = strings.TrimSpace(input.Platform)
	node.Architecture = strings.TrimSpace(input.Architecture)
	node.AgentVersion = strings.TrimSpace(input.AgentVersion)
	if input.DialHost != "" {
		node.DialHost = input.DialHost
	}
	node.UpdatedAt = now
	event, err := audit.NewEvent(now, "node", node.ID, "node.reenroll", "node", node.ID, "succeeded", map[string]any{"identity_preserved": true})
	if err != nil {
		return nodes.Node{}, err
	}
	return s.repository.ConsumeEnrollmentToken(ctx, ConsumeInput{TokenHash: securetoken.Hash(input.RawToken),
		Node: node, CredentialHash: authenticated.CredentialHash, Reenroll: true}, now, event)
}
