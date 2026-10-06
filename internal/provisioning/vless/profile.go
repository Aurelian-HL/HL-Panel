// Package vless compiles a single customer endpoint and shared identity into
// Xray configuration. It never chooses members or expands a subscription into
// one URI per machine. Native configurations and URIs contain credentials and
// must only be delivered through an authorized secret-distribution path.
package vless

import "io"

type Endpoint struct {
	Hostname string
	Port     int
	Name     string
}

// Identity is supplied by a future credential repository. This compiler does
// not create users, persist secrets, or imply successful node provisioning.
type Identity struct {
	UUID string `json:"-"`
	Flow string
}

type TLSProfile struct {
	ServerName string
}

// RealityProfile contains the public client parameters and the node-local
// private material used by an Xray Reality listener. PrivateKey is deliberately
// excluded from JSON and must never be returned by an inventory or API DTO.
type RealityProfile struct {
	ServerName  string
	PublicKey   string
	PrivateKey  string `json:"-"`
	ShortID     string
	Destination string
	Fingerprint string
	SpiderX     string
}

type Profile struct {
	Endpoint Endpoint
	Identity Identity
	TLS      TLSProfile
	Reality  *RealityProfile
}

// Listener describes one backend's private listener. Public subscribers use
// Profile.Endpoint, which may point at a different, shared L4 ingress.
type Listener struct {
	Address         string
	Port            int
	CertificateFile string
	PrivateKeyFile  string `json:"-"`
}

// GenerateRealityKeyPair creates an X25519 key pair in the unpadded base64url
// representation accepted by Xray. The caller owns both values and is
// responsible for storing the private key in a protected node secret store.
func GenerateRealityKeyPair(random io.Reader) (publicKey, privateKey string, err error) {
	return generateRealityKeyPair(random)
}
