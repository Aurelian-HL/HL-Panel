package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/forwarding/gostconfig"
	"github.com/hongle/hl-panel/internal/protocol/agentv1"
	"os"
	"path/filepath"
)

func (a *GOSTProcessAdapter) SupportsReliableUDP() bool {
	return a.autoStart && a.EngineVersion() == "3.3.1-nightly.20260922-hl-udp.1"
}

// PEMs are immutable and content addressed. Keep old generations for rollback;
// validation never repairs tampered files or accepts control-plane file paths.
func (a *GOSTProcessAdapter) compileNative(bundle agentv1.ConfigurationBundle, create bool) ([]byte, error) {
	raw, err := CompileGOSTBundle(bundle)
	if err != nil {
		return nil, err
	}
	var c gostconfig.Configuration
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	reliable := len(c.Chains) > 0
	for _, s := range c.Services {
		reliable = reliable || s.Listener.Type != "tcp"
	}
	if reliable && !a.SupportsReliableUDP() {
		return nil, fmt.Errorf("%w: reliable UDP requires the pinned patched GOST engine", ErrInvalidConfiguration)
	}
	dir := filepath.Join(a.stagingDir, "tls")
	materialize := func(t *gostconfig.TLS) error {
		if t == nil {
			return nil
		}
		if create {
			if err := ensurePrivateDir(dir); err != nil {
				return err
			}
		}
		for _, f := range []struct {
			pem  *string
			path *string
		}{{&t.CertPEM, &t.CertFile}, {&t.KeyPEM, &t.KeyFile}, {&t.CAPEM, &t.CAFile}} {
			if *f.pem == "" {
				continue
			}
			expected := []byte(*f.pem)
			path := filepath.Join(dir, fmt.Sprintf("%x.pem", sha256.Sum256(expected)))
			got, err := readRegularFile(path, MaxConfigurationBytes)
			if create && os.IsNotExist(err) {
				err = writeFileAtomic(path, expected, 0o600)
				got = expected
			}
			if err != nil || !bytes.Equal(got, expected) {
				return ErrConfigurationHash
			}
			info, err := os.Lstat(path)
			if err != nil || info.Mode().Perm()&0o077 != 0 {
				return ErrConfigurationHash
			}
			*f.path = path
			*f.pem = ""
		}
		return nil
	}
	for i := range c.Services {
		if err := materialize(c.Services[i].Listener.TLS); err != nil {
			return nil, err
		}
	}
	for i := range c.Chains {
		for j := range c.Chains[i].Hops {
			for k := range c.Chains[i].Hops[j].Nodes {
				if err := materialize(c.Chains[i].Hops[j].Nodes[k].Dialer.TLS); err != nil {
					return nil, err
				}
			}
		}
	}
	native, err := json.Marshal(c)
	if err != nil || a.api == nil {
		return native, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(native, &document); err != nil {
		return nil, err
	}
	document["api"], err = json.Marshal(a.api)
	if err != nil {
		return nil, err
	}
	return json.Marshal(document)
}
