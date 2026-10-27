package memoryrepo

import "github.com/hongle/hl-panel/internal/control/vlessruntime"

// compilationRealityKeys memoizes only a pure cryptographic comparison, never
// authorization. It belongs to one synchronous compilation or detached snapshot
// validation and must not be retained by a Store, read view or returned config.
// The pair includes both exact input strings, so a different public key cannot
// inherit another pair's result. All record and relationship checks still run.
type compilationRealityKeys struct {
	results map[[2]string]bool
}

func (keys *compilationRealityKeys) matches(privateKey, publicKey string) bool {
	if keys == nil {
		return vlessruntime.RealityKeyMatchesPublicKey(privateKey, publicKey)
	}
	pair := [2]string{privateKey, publicKey}
	if result, exists := keys.results[pair]; exists {
		return result
	}
	result := vlessruntime.RealityKeyMatchesPublicKey(privateKey, publicKey)
	if keys.results == nil {
		keys.results = make(map[[2]string]bool)
	}
	keys.results[pair] = result
	return result
}
