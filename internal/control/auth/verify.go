package auth

// VerifyPassword compares a plaintext password with a stored password hash.
// It intentionally returns only an authorization error for malformed hashes
// and mismatches so callers cannot distinguish credential failure causes.
func VerifyPassword(hash []byte, password string) error {
	return checkPassword(hash, password)
}
