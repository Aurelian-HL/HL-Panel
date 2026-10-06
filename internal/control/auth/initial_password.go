package auth

// DefaultPassword is a public bootstrap credential, never a final password.
const DefaultPassword = "123456"

func IsDefaultPasswordHash(hash []byte) bool {
	return checkPassword(hash, DefaultPassword) == nil
}
