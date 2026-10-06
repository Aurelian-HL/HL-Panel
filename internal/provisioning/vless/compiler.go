package vless

import (
	"encoding/json"
	"errors"
	"net"
	"strconv"

	"github.com/hongle/hl-panel/internal/serviceaddress"
)

// CompileDirect builds one TLS-protected VLESS listener with DIRECT egress.
// Each candidate uses the same Profile.Identity and TLS identity. Per-machine
// binding and certificate paths are explicit, separate from the public URI.
// Callers must run the pinned Xray validator before activating these bytes.
func CompileDirect(profile Profile, listener Listener) ([]byte, error) {
	return Compile(profile, listener, Egress{Mode: EgressDirect})
}

// Compile keeps inbound protocol and outbound path as separate decisions.
// Unsupported egress modes fail closed instead of silently using DIRECT.
func Compile(profile Profile, listener Listener, egress Egress) ([]byte, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	var listenerErr error
	if profile.Reality == nil {
		listenerErr = listener.validate()
	} else {
		listenerErr = listener.validateReality()
	}
	if listenerErr != nil {
		return nil, listenerErr
	}
	outbound, err := egress.nativeOutbound()
	if err != nil {
		return nil, err
	}
	client := map[string]any{"id": profile.Identity.UUID}
	if profile.Identity.Flow != "" {
		client["flow"] = profile.Identity.Flow
	}
	streamSettings, err := inboundStreamSettings(profile, listener)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "warning", "access": "none"},
		"inbounds": []any{map[string]any{
			"tag": "vless-service", "listen": listener.Address, "port": listener.Port, "protocol": "vless",
			"settings":       map[string]any{"clients": []any{client}, "decryption": "none"},
			"streamSettings": streamSettings,
		}},
		"outbounds": []any{outbound},
	})
}

func inboundStreamSettings(profile Profile, listener Listener) (map[string]any, error) {
	if profile.Reality == nil {
		return map[string]any{
			"network": "raw", "security": "tls",
			"tlsSettings": map[string]any{
				"minVersion": "1.3", "alpn": []string{"http/1.1"},
				"certificates": []any{map[string]any{
					"certificateFile": listener.CertificateFile, "keyFile": listener.PrivateKeyFile,
				}},
			},
		}, nil
	}
	reality := profile.Reality
	if reality.PrivateKey == "" {
		return nil, errors.New("Reality server private key is required for compilation")
	}
	if reality.PublicKey == "" {
		return nil, errors.New("Reality public key is required for client distribution")
	}
	return map[string]any{
		"network": "tcp", "security": "reality",
		"realitySettings": map[string]any{
			"show": false, "dest": reality.Destination, "xver": 0,
			"serverNames": []string{reality.ServerName}, "privateKey": reality.PrivateKey,
			"shortIds": []string{reality.ShortID},
		},
	}, nil
}

// ForwardTarget is a fixed final destination for a VLESS DIRECT service. The
// first safe Reality slice deliberately accepts one target; target pools and
// EXIT_GROUP bearer compilation remain separate engine work.
type ForwardTarget struct {
	Host string
	Port int
}

// CompileDirectForward builds a VLESS Reality/Vision listener that forwards
// every authenticated connection to one explicit TCP target. It never embeds
// credentials in errors or logs and rejects ambiguous multi-target routes.
func CompileDirectForward(profile Profile, listener Listener, target ForwardTarget) ([]byte, error) {
	if profile.Reality == nil {
		return nil, errors.New("VLESS direct forwarding requires Reality")
	}
	host, err := serviceaddress.NormalizeHost(target.Host)
	if err != nil || target.Port < 1 || target.Port > 65535 {
		return nil, errors.New("VLESS direct forwarding target is invalid")
	}
	data, err := Compile(profile, listener, Egress{Mode: EgressDirect})
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	outbounds, ok := document["outbounds"].([]any)
	if !ok || len(outbounds) != 1 {
		return nil, errors.New("VLESS direct forwarding generated an invalid outbound set")
	}
	outbound, ok := outbounds[0].(map[string]any)
	if !ok {
		return nil, errors.New("VLESS direct forwarding generated an invalid outbound")
	}
	outbound["settings"] = map[string]any{"redirect": net.JoinHostPort(host, strconv.Itoa(target.Port))}
	return json.Marshal(document)
}
