package vless

import (
	"crypto/ecdh"
	"io"
)

func generateX25519PrivateKey(random io.Reader) ([]byte, error) {
	key, err := ecdh.X25519().GenerateKey(random)
	if err != nil {
		return nil, err
	}
	return key.Bytes(), nil
}

func x25519PublicKey(private []byte) []byte {
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil
	}
	return key.PublicKey().Bytes()
}
