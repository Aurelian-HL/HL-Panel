# TCP front door

`tcpproxy` is a protocol-agnostic single-entry gateway. For every accepted TCP
connection it asks a server-side `Selector` for exactly one healthy backend,
dials that backend, proxies bytes in both directions, and releases the
reservation after both directions close.

The package does not return a backend list or multiple subscription URIs to a
client. VLESS/Reality and SOCKS5 framing remains the responsibility of the
selected Xray/GOST process. The shared scheduler supplies weighted round-robin,
weighted least-connections, and rendezvous policies; health, draining state,
priority, and lease expiry must be filtered before selection.

TCP half-close is preserved. When the client calls `CloseWrite`, the gateway
propagates a write half-close to the backend and continues copying the backend
response to the client. Context cancellation or `Server.Close` closes the
listener and all tracked client/backend connections, then the selected backend
reservation is released.

## Local verification

```text
go test ./internal/gateway/tcpproxy ./internal/routing/scheduler
go test ./...
go vet ./...
```

Tests use loopback listeners only. This package has no deployment or production
configuration side effects.
