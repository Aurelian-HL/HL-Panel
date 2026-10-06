package engine

import "github.com/hongle/hl-panel/internal/routing/endpointrouter"

// Compatibility aliases keep existing agent-side callers source-compatible.
// Connection admission itself belongs to the shared routing boundary.
type SelectionPolicy = endpointrouter.SelectionPolicy
type EndpointStatus = endpointrouter.EndpointStatus
type Endpoint = endpointrouter.Endpoint
type EndpointRouter = endpointrouter.Router

const (
	SelectionPolicyWeightedRoundRobin       = endpointrouter.SelectionPolicyWeightedRoundRobin
	SelectionPolicyWeightedLeastConnections = endpointrouter.SelectionPolicyWeightedLeastConnections
	SelectionPolicyRendezvousHash           = endpointrouter.SelectionPolicyRendezvousHash

	EndpointReady    = endpointrouter.EndpointReady
	EndpointDraining = endpointrouter.EndpointDraining
	EndpointOffline  = endpointrouter.EndpointOffline
)

var (
	ErrNoHealthyEndpoint       = endpointrouter.ErrNoHealthyEndpoint
	ErrUnknownEndpoint         = endpointrouter.ErrUnknownEndpoint
	ErrStaleReachabilityResult = endpointrouter.ErrStaleReachabilityResult
	ErrMembershipConflict      = endpointrouter.ErrMembershipConflict
)

func NewEndpointRouter(policy SelectionPolicy) (*EndpointRouter, error) {
	return endpointrouter.New(policy)
}
