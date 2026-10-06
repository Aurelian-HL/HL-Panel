package customers

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func requestFingerprint(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func customerRequestFingerprint(actorID, id string, input CustomerInput) (string, error) {
	// A fast SHA of a low-entropy password would make the idempotency record an
	// easier password-cracking target than PasswordHash. Use the same password
	// work factor and an actor/key-scoped salt, while remaining stable on retry.
	if input.Password != "" {
		salt := sha256.Sum256([]byte("customer-mutation\x00" + actorID + "\x00" + input.IdempotencyKey))
		digest, err := pbkdf2.Key(sha256.New, input.Password, salt[:], 600_000, 32)
		if err != nil {
			return "", err
		}
		input.Password = hex.EncodeToString(digest)
	}
	return requestFingerprint(struct {
		ID    string        `json:"id"`
		Input CustomerInput `json:"input"`
	}{id, input})
}
