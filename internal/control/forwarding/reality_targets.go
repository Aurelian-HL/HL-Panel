package forwarding

import (
	"crypto/rand"
	"math/big"
)

// DefaultRealityTargets are paired TLS 1.3 / X25519 targets. Selecting a
// candidate never performs a network request in the rule creation path.
func DefaultRealityTargets() []RealityDefaults {
	return []RealityDefaults{
		{ServerName: "www.apple.com", Destination: "www.apple.com:443"},
		{ServerName: "www.icloud.com", Destination: "www.icloud.com:443"},
		{ServerName: "www.cloudflare.com", Destination: "www.cloudflare.com:443"},
		{ServerName: "www.amazon.com", Destination: "www.amazon.com:443"},
	}
}

func WithRealityTargets(targets []RealityDefaults) ServiceOption {
	return func(s *Service) { s.realityTargets = append([]RealityDefaults(nil), targets...) }
}

func (s *Service) RealityTargets() []RealityDefaults {
	return append([]RealityDefaults{}, s.realityTargets...)
}

func (s *Service) selectCreationRealityTarget(request *Request) error {
	if request.EffectiveIngressProtocol() != IngressVLESSReality || request.RealityServerName != "" || request.RealityDestination != "" || len(s.realityTargets) == 0 {
		return nil
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(s.realityTargets))))
	if err != nil {
		return err
	}
	target := s.realityTargets[index.Int64()]
	request.RealityServerName, request.RealityDestination = target.ServerName, target.Destination
	return nil
}
