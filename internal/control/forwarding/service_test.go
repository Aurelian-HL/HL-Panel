package forwarding

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"strings"
	"testing"
	"time"
)

type captureRepository struct {
	created CreateInput
	updated UpdateInput
	event   audit.Event
	calls   int
}

func (r *captureRepository) CreateForwardingRule(_ context.Context, input CreateInput, event audit.Event) (Rule, bool, error) {
	r.created = input
	r.event = event
	r.calls++
	return input.Rule, false, nil
}
func (r *captureRepository) UpdateForwardingRule(_ context.Context, input UpdateInput, event audit.Event) (Rule, bool, error) {
	r.updated = input
	r.event = event
	r.calls++
	return input.Rule, false, nil
}
func (r *captureRepository) PreviewForwardingImport(_ context.Context, input []ImportCandidate) ([]ImportEvaluation, error) {
	items := make([]ImportEvaluation, len(input))
	for index, candidate := range input {
		items[index] = ImportEvaluation{Line: candidate.Line, Action: ImportActionCreate, Rule: candidate.Rule}
	}
	return items, nil
}
func (r *captureRepository) ImportForwardingRules(_ context.Context, input ImportInput, _ audit.Event) (ImportResult, bool, error) {
	items := make([]Rule, len(input.Candidates))
	for index, candidate := range input.Candidates {
		items[index] = candidate.Rule
	}
	return ImportResult{Created: len(items), Items: items}, false, nil
}
func (*captureRepository) ListForwardingRules(context.Context) ([]Rule, error) { return []Rule{}, nil }
func (*captureRepository) ForwardingRule(context.Context, string) (Rule, error) {
	return Rule{}, faults.ErrNotFound
}

func validRequest() Request {
	return Request{Name: "客户转发", CustomerID: "customer-1", EntryGroupID: "entry-1", EgressMode: EgressDirect, Protocol: ProtocolTCP, ListenPort: 0, Targets: []Target{{Host: "EXAMPLE.test.", Port: 443}}, SelectionPolicy: SelectionRoundRobin}
}

func TestCreateStableHashAndPrivateAudit(t *testing.T) {
	repo := &captureRepository{}
	now := time.Date(2026, 10, 2, 1, 2, 3, 0, time.UTC)
	service := NewService(repo, func() time.Time { return now })
	request := validRequest()
	request.Description = "private-note"
	rule, replayed, err := service.Create(context.Background(), "admin", request, "create-1")
	if err != nil || replayed || rule.ID == "" || rule.Revision != 1 || !rule.CreatedAt.Equal(now) || rule.Deployed || rule.Status != StatusPendingActivation {
		t.Fatalf("create failed: %+v, %v", rule, err)
	}
	firstHash := repo.created.RequestSHA256
	request.Targets[0].Host = " example.test "
	_, _, err = service.Create(context.Background(), "admin", request, "create-1")
	if err != nil || firstHash != repo.created.RequestSHA256 {
		t.Fatal("canonical equivalent inputs must have the same idempotency digest")
	}
	request.Targets[0].Port = 8443
	_, _, err = service.Create(context.Background(), "admin", request, "create-1")
	if err != nil || firstHash == repo.created.RequestSHA256 {
		t.Fatal("target change must alter request digest")
	}
	serialized, _ := json.Marshal(repo.event)
	if strings.Contains(string(serialized), "example.test") || strings.Contains(string(serialized), "private-note") {
		t.Fatal("audit must not duplicate target or free-form description")
	}
}

func TestRejectUnsafeTargetsWithoutRepositoryCalls(t *testing.T) {
	for _, host := range []string{"socks5://user:secret@host", "user:secret@host", "host/path", "host:443", "host\nother", "[::1]", "fe80::1%eth0"} {
		t.Run(host, func(t *testing.T) {
			repo := &captureRepository{}
			request := validRequest()
			request.Targets[0].Host = host
			_, _, err := NewService(repo, nil).Create(context.Background(), "admin", request, "key")
			if !errors.Is(err, faults.ErrValidation) || repo.calls != 0 {
				t.Fatal("unsafe target reached repository")
			}
		})
	}
	for _, count := range []int{0, 33} {
		request := validRequest()
		request.Targets = make([]Target, count)
		if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("target count %d accepted", count)
		}
	}
}

func TestVLESSSOCKS5CredentialsMustBePaired(t *testing.T) {
	for _, test := range []struct {
		name     string
		username string
		password string
	}{
		{name: "username only", username: "landing-user"},
		{name: "password only", password: "landing-secret"},
		{name: "both missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest()
			request.IngressProtocol = IngressVLESSReality
			request.VLESSOutboundMode = VLESSOutboundSOCKS5
			request.VLESSSOCKS5Host = "landing.example.test"
			request.VLESSSOCKS5Port = 1080
			request.VLESSSOCKS5Username = test.username
			request.VLESSSOCKS5Password = test.password
			request.RealityServerName = "www.example.com"
			request.RealityPublicKey = "AbCdEf0123456789AbCdEf0123456789AbCdEf01234"
			request.RealityShortID = "0123456789abcdef"
			if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
				t.Fatalf("unpaired SOCKS5 credentials accepted: %v", err)
			}
		})
	}
}

func TestVLESSDefaultsToAuthenticatedSOCKS5AndRejectsDirect(t *testing.T) {
	request := validRequest()
	request.IngressProtocol = IngressVLESSReality
	request.VLESSFlow = "xtls-rprx-vision"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = strings.Repeat("A", 43)
	request.RealityShortID = "0123456789abcdef"
	request.VLESSSOCKS5Host = "landing.example.com"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "landing-user"
	request.VLESSSOCKS5Password = "landing-secret"
	request.VLESSOutboundMode = ""
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatalf("VLESS with complete landing credentials was rejected: %v", err)
	}
	if normalized.VLESSOutboundMode != VLESSOutboundSOCKS5 {
		t.Fatalf("VLESS omitted outbound mode normalized to %q, want SOCKS5", normalized.VLESSOutboundMode)
	}

	request.VLESSOutboundMode = VLESSOutboundDirect
	if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) || !strings.Contains(err.Error(), "requires SOCKS5 outbound") {
		t.Fatalf("VLESS DIRECT outbound was accepted: %v", err)
	}
}

func TestRuleValidationAndCASContract(t *testing.T) {
	repo := &captureRepository{}
	service := NewService(repo, nil)
	request := validRequest()
	if _, _, err := service.Update(context.Background(), "admin", "fwd-1", request, "update-1"); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("missing revision accepted")
	}
	request.Revision = 4
	request.Paused = true
	if _, _, err := service.Update(context.Background(), "admin", "fwd-1", request, "update-1"); err != nil {
		t.Fatal(err)
	}
	if repo.updated.ExpectedRevision != 4 || repo.updated.Rule.Revision != 5 || !repo.updated.Rule.CreatedAt.IsZero() || repo.updated.IdempotencyKey != "update-1" || repo.updated.RequestSHA256 == "" {
		t.Fatal("missing atomic replacement metadata")
	}
	request.ExitGroupID = "exit-1"
	if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("DIRECT route with exit group accepted")
	}
	request.EgressMode = EgressExitGroup
	if _, err := NormalizeRequest(request); err != nil {
		t.Fatal(err)
	}
	request.Targets = append(request.Targets, Target{Host: "example.test", Port: 443})
	if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("canonical duplicate target accepted")
	}
}

func TestAdvancedRuleOptionsAndUDPLimitBoundary(t *testing.T) {
	request := validRequest()
	request.SelectionPolicy = SelectionFailover
	request.AcceptProxyProtocol = true
	request.SendProxyProtocol = SendProxyV2TCP
	request.SpeedLimitMbps = 200
	request.IPLimit = 5
	request.ConnectionLimit = 20
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SelectionPolicy != SelectionFailover || normalized.SendProxyProtocol != SendProxyV2TCP || normalized.SpeedLimitMbps != 200 || normalized.IPLimit != 5 || normalized.ConnectionLimit != 20 {
		t.Fatalf("advanced options were not preserved: %+v", normalized)
	}

	request.Protocol = ProtocolUDP
	if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("UDP rule accepted unsupported TCP limits")
	}
	request.AcceptProxyProtocol = false
	request.SpeedLimitMbps = 0
	request.IPLimit = 0
	request.ConnectionLimit = 0
	request.SendProxyProtocol = SendProxyV2TCPUDP
	if _, err := NormalizeRequest(request); err != nil {
		t.Fatalf("UDP rule rejected supported send proxy mode: %v", err)
	}
}

func TestSOCKS5IngressIsIndependentFromRawTCP(t *testing.T) {
	request := validRequest()
	request.IngressProtocol = IngressSOCKS5
	request.Protocol = ProtocolTCP
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatalf("valid SOCKS5 ingress was rejected: %v", err)
	}
	if normalized.EffectiveIngressProtocol() != IngressSOCKS5 {
		t.Fatalf("SOCKS5 ingress was normalized to %q", normalized.EffectiveIngressProtocol())
	}
	request.Protocol = ProtocolUDP
	if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("SOCKS5 ingress accepted a UDP target")
	}
	request = validRequest()
	request.IngressProtocol = IngressSOCKS5
	request.RealityServerName = "www.example.com"
	if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
		t.Fatal("SOCKS5 ingress accepted Reality-only parameters")
	}
}

func TestRealityPublicKeyMustBeAnX25519Base64URLKey(t *testing.T) {
	valid := validRequest()
	valid.IngressProtocol = IngressVLESSReality
	valid.VLESSOutboundMode = VLESSOutboundSOCKS5
	valid.VLESSSOCKS5Host = "landing.example.com"
	valid.VLESSSOCKS5Port = 1080
	valid.VLESSSOCKS5Username = "landing-user"
	valid.VLESSSOCKS5Password = "landing-secret"
	valid.VLESSFlow = "xtls-rprx-vision"
	valid.RealityServerName = "www.example.com"
	valid.RealityPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	valid.RealityShortID = "0123456789abcdef"
	if _, err := NormalizeRequest(valid); err != nil {
		t.Fatalf("valid 32-byte raw Base64URL key was rejected: %v", err)
	}
	valid.RealityPublicKey += "="
	normalized, err := NormalizeRequest(valid)
	if err != nil {
		t.Fatalf("valid padded Base64URL key was rejected: %v", err)
	}
	if normalized.RealityPublicKey != strings.Repeat("A", 43) {
		t.Fatalf("padded Reality key was not canonicalized: %q", normalized.RealityPublicKey)
	}
	for _, key := range []string{strings.Repeat("A", 42), strings.Repeat("A", 44), strings.Repeat("A", 42) + "+"} {
		request := valid
		request.RealityPublicKey = key
		if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("invalid Reality public key %q was accepted: %v", key, err)
		}
	}
}

func TestVLESSRealityRejectsDirectOutbound(t *testing.T) {
	request := validRequest()
	request.IngressProtocol = IngressVLESSReality
	request.VLESSOutboundMode = VLESSOutboundDirect
	request.VLESSFlow = "xtls-rprx-vision"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = strings.Repeat("A", 43)
	request.RealityShortID = "0123456789abcdef"
	if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) || !strings.Contains(err.Error(), "requires SOCKS5 outbound") {
		t.Fatalf("direct VLESS rule was not rejected: %v", err)
	}
}

func TestVLESSSOCKS5AllowsClientRequestedTarget(t *testing.T) {
	request := validRequest()
	request.IngressProtocol = IngressVLESSReality
	request.VLESSOutboundMode = VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.com"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "landing-user"
	request.VLESSSOCKS5Password = "landing-secret"
	request.VLESSFlow = "xtls-rprx-vision"
	request.RealityServerName = "www.example.com"
	request.RealityPublicKey = strings.Repeat("A", 43)
	request.RealityShortID = "0123456789abcdef"
	request.Targets = nil
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatalf("dynamic VLESS SOCKS5 rule was rejected: %v", err)
	}
	if len(normalized.Targets) != 0 || normalized.VLESSOutboundMode != VLESSOutboundSOCKS5 {
		t.Fatalf("dynamic VLESS SOCKS5 target semantics were not preserved: %+v", normalized)
	}
	request.Targets = []Target{{Host: "first.example.com", Port: 443}, {Host: "second.example.com", Port: 443}}
	if _, err := NormalizeRequest(request); !errors.Is(err, faults.ErrValidation) || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("dynamic VLESS SOCKS5 accepted multiple compatibility targets: %v", err)
	}
}

func TestAutomaticRealityDefaultsAndSecretBoundary(t *testing.T) {
	for _, defaults := range []RealityDefaults{
		{ServerName: "example.com"},
		{Destination: "example.com:443"},
		{ServerName: "bad name", Destination: "example.com:443"},
		{ServerName: "example.com", Destination: "example.com:0"},
		{ServerName: "127.0.0.1", Destination: "example.com:443"},
	} {
		if _, err := NormalizeRealityDefaults(defaults); !errors.Is(err, faults.ErrValidation) {
			t.Fatalf("invalid automatic Reality defaults accepted: %+v, %v", defaults, err)
		}
	}
	defaults, err := NormalizeRealityDefaults(RealityDefaults{ServerName: " example.com ", Destination: "example.com:443"})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	request.IngressProtocol = IngressVLESSReality
	request.VLESSOutboundMode = VLESSOutboundSOCKS5
	request.VLESSSOCKS5Host = "landing.example.com"
	request.VLESSSOCKS5Port = 1080
	request.VLESSSOCKS5Username = "landing-user"
	request.VLESSSOCKS5Password = "landing-secret"
	request.VLESSFlow = "xtls-rprx-vision"
	repo := &captureRepository{}
	if _, _, err := NewService(repo, nil).Create(context.Background(), "admin", request, "auto-missing"); err != nil || !repo.created.AutoReality || repo.created.RealityPrivateKey != "" {
		t.Fatalf("unconfigured automatic target was not passed to replay-aware repository: %v", err)
	}
	service := NewService(repo, nil, WithRealityDefaults(defaults))
	rule, _, err := service.Create(context.Background(), "admin", request, "auto-create")
	if err != nil {
		t.Fatal(err)
	}
	if rule.RealityServerName != defaults.ServerName || rule.RealityDestination != defaults.Destination || rule.RealityPublicKey == "" || rule.RealityShortID == "" || repo.created.RealityPrivateKey == "" {
		t.Fatalf("incomplete generated Reality public parameters: %+v", rule)
	}
	firstHash := repo.created.RequestSHA256
	firstPrivateKey := repo.created.RealityPrivateKey
	_, _, err = service.Create(context.Background(), "admin", request, "auto-create")
	if err != nil || repo.created.RequestSHA256 != firstHash || repo.created.RealityPrivateKey == firstPrivateKey {
		t.Fatal("idempotency digest must precede fresh secret generation")
	}
	public, _ := json.Marshal(rule)
	auditData, _ := json.Marshal(repo.event)
	if strings.Contains(string(public), firstPrivateKey) || strings.Contains(string(auditData), firstPrivateKey) {
		t.Fatal("generated Reality private key leaked into public rule or audit")
	}
}

func TestAvailabilityStatusBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Second)
	cases := []struct {
		name     string
		rule     Rule
		customer CustomerAvailability
		want     Status
	}{
		{"paused", Rule{Paused: true}, CustomerAvailability{}, StatusPaused},
		{"disabled", Rule{}, CustomerAvailability{}, StatusCustomerDisabled},
		{"expiry boundary", Rule{}, CustomerAvailability{Enabled: true, ExpiresAt: &now}, StatusCustomerExpired},
		{"quota boundary", Rule{}, CustomerAvailability{Enabled: true, TrafficLimitBytes: 10, UsedTrafficBytes: 10}, StatusQuotaExhausted},
		{"enabled remains pending", Rule{}, CustomerAvailability{Enabled: true, ExpiresAt: &future, TrafficLimitBytes: 10, UsedTrafficBytes: 9}, StatusPendingActivation},
		{"unlimited", Rule{}, CustomerAvailability{Enabled: true, UsedTrafficBytes: 100}, StatusPendingActivation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveStatus(tc.rule, tc.customer, now); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
