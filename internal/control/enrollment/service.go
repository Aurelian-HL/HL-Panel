package enrollment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/nodes"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/securetoken"
	"github.com/hongle/hl-panel/internal/serviceaddress"
)

type Service struct {
	repository          Repository
	now                 func() time.Time
	maxTTL              time.Duration
	validateNezhaServer func(context.Context, uint64) error
	idempotencySecret   []byte
}

type Option func(*Service)

func WithNezhaServerValidator(validate func(context.Context, uint64) error) Option {
	return func(service *Service) { service.validateNezhaServer = validate }
}

func WithIdempotencySecret(secret []byte) Option {
	return func(service *Service) { service.idempotencySecret = append([]byte(nil), secret...) }
}

func NewService(repository Repository, now func() time.Time, maxTTL time.Duration, options ...Option) *Service {
	if now == nil {
		now = time.Now
	}
	if maxTTL <= 0 {
		maxTTL = 24 * time.Hour
	}
	service := &Service{repository: repository, now: now, maxTTL: maxTTL}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) Issue(ctx context.Context, adminID, name string, ttl time.Duration) (IssueResult, error) {
	return s.issue(ctx, adminID, name, "", 0, ttl)
}

// IssueForGroup implements the NY group-first enrollment workflow. The first
// successful use atomically joins the enrolled node to the selected group.
func (s *Service) IssueForGroup(ctx context.Context, adminID, name, groupID string, ttl time.Duration) (IssueResult, error) {
	return s.issue(ctx, adminID, name, groupID, 0, ttl)
}

func (s *Service) IssueForGroupWithNezha(ctx context.Context, adminID, name, groupID string, serverID uint64, ttl time.Duration) (IssueResult, error) {
	return s.issue(ctx, adminID, name, groupID, serverID, ttl)
}

func (s *Service) IssueForGroupWithNezhaIdempotent(ctx context.Context, adminID, name, groupID string, serverID uint64, ttl time.Duration, key string) (IssueResult, bool, error) {
	return s.issueIdempotent(ctx, adminID, name, groupID, serverID, ttl, key)
}

func (s *Service) ListPendingForGroup(ctx context.Context, groupID string) ([]PendingToken, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" || len(groupID) > 128 {
		return nil, fmt.Errorf("%w: group_id is required", faults.ErrValidation)
	}
	return s.repository.ListPendingEnrollmentTokens(ctx, groupID, s.now().UTC())
}

func (s *Service) issue(ctx context.Context, adminID, name, groupID string, serverID uint64, ttl time.Duration) (IssueResult, error) {
	result, _, err := s.issueIdempotent(ctx, adminID, name, groupID, serverID, ttl, "")
	return result, err
}

func (s *Service) issueIdempotent(ctx context.Context, adminID, name, groupID string, serverID uint64, ttl time.Duration, idempotencyKey string) (IssueResult, bool, error) {
	adminID = strings.TrimSpace(adminID)
	name = strings.TrimSpace(name)
	groupID = strings.TrimSpace(groupID)
	if name == "" || len(name) > 128 {
		return IssueResult{}, false, fmt.Errorf("%w: name must contain 1 to 128 characters", faults.ErrValidation)
	}
	if len(groupID) > 128 {
		return IssueResult{}, false, fmt.Errorf("%w: group_id is too long", faults.ErrValidation)
	}
	if idempotencyKey != "" && !validIdempotencyKey(idempotencyKey) {
		return IssueResult{}, false, fmt.Errorf("%w: invalid Idempotency-Key", faults.ErrValidation)
	}
	if serverID != 0 {
		if groupID == "" || s.validateNezhaServer == nil {
			return IssueResult{}, false, fmt.Errorf("%w: Nezha binding requires a group and monitoring service", faults.ErrValidation)
		}
		if err := s.validateNezhaServer(ctx, serverID); err != nil {
			return IssueResult{}, false, err
		}
	}
	if ttl <= 0 || ttl > s.maxTTL {
		return IssueResult{}, false, fmt.Errorf("%w: expires_in_seconds is outside the allowed range", faults.ErrValidation)
	}
	now := s.now().UTC()
	id, rawToken, err := s.tokenMaterial(adminID, name, groupID, serverID, ttl, idempotencyKey)
	if err != nil {
		return IssueResult{}, false, err
	}
	digest := issueRequestDigest(adminID, name, groupID, serverID, ttl)
	token := Token{
		ID:             id,
		Name:           name,
		GroupID:        groupID,
		NezhaServerID:  serverID,
		TokenHash:      securetoken.Hash(rawToken),
		ExpiresAt:      now.Add(ttl),
		CreatedBy:      adminID,
		CreatedAt:      now,
		IdempotencyKey: idempotencyKey,
		RequestSHA256:  digest,
	}
	event, err := audit.NewEvent(now, "administrator", adminID, "enrollment_token.create", "enrollment_token", id, "succeeded", map[string]any{
		"name":            name,
		"group_id":        groupID,
		"nezha_server_id": serverID,
		"expires_at":      token.ExpiresAt,
	})
	if err != nil {
		return IssueResult{}, false, err
	}
	if idempotencyKey != "" {
		repository, ok := s.repository.(interface {
			CreateEnrollmentTokenIdempotent(context.Context, Token, audit.Event) (Token, bool, error)
		})
		if !ok {
			return IssueResult{}, false, fmt.Errorf("%w: enrollment repository lacks durable idempotency", faults.ErrValidation)
		}
		existing, replayed, err := repository.CreateEnrollmentTokenIdempotent(ctx, token, event)
		if err != nil {
			return IssueResult{}, false, err
		}
		if replayed {
			token = existing
			if token.RequestSHA256 != digest {
				return IssueResult{}, false, faults.ErrIdempotencyConflict
			}
		}
		return IssueResult{ID: token.ID, Name: token.Name, Token: rawToken, ExpiresAt: token.ExpiresAt, NezhaServerID: token.NezhaServerID, Replayed: replayed}, replayed, nil
	}
	if err := s.repository.CreateEnrollmentToken(ctx, token, event); err != nil {
		return IssueResult{}, false, err
	}
	return IssueResult{ID: id, Name: name, Token: rawToken, ExpiresAt: token.ExpiresAt, NezhaServerID: serverID}, false, nil
}

func (s *Service) tokenMaterial(adminID, name, groupID string, serverID uint64, ttl time.Duration, key string) (string, string, error) {
	if key == "" {
		id, err := idgen.New("ent")
		if err != nil {
			return "", "", err
		}
		raw, err := securetoken.Generate("enr")
		return id, raw, err
	}
	if len(s.idempotencySecret) == 0 {
		return "", "", fmt.Errorf("%w: enrollment idempotency secret is not configured", faults.ErrValidation)
	}
	derive := func(label string) []byte {
		mac := hmac.New(sha256.New, s.idempotencySecret)
		mac.Write([]byte("hl-panel/enrollment-token/" + label + "/v1\x00" + adminID + "\x00" + key + "\x00" + name + "\x00" + groupID + fmt.Sprintf("\x00%d\x00%d", serverID, ttl)))
		return mac.Sum(nil)
	}
	return "ent_" + hex.EncodeToString(derive("id")[:16]), "enr_" + base64.RawURLEncoding.EncodeToString(derive("raw")), nil
}
func issueRequestDigest(adminID, name, groupID string, serverID uint64, ttl time.Duration) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("enrollment-token\x00%s\x00%s\x00%s\x00%d\x00%d", adminID, name, groupID, serverID, ttl)))
	return hex.EncodeToString(sum[:])
}
func validIdempotencyKey(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n")
}

func (s *Service) Enroll(ctx context.Context, input EnrollInput) (EnrollResult, error) {
	if strings.TrimSpace(input.RawToken) == "" {
		return EnrollResult{}, faults.ErrUnauthorized
	}
	if err := validateNodeIdentity(input); err != nil {
		return EnrollResult{}, err
	}
	if input.DialHost != "" {
		input.DialHost, _ = serviceaddress.NormalizeHost(input.DialHost)
	}
	now := s.now().UTC()
	nodeID, err := idgen.New("nod")
	if err != nil {
		return EnrollResult{}, err
	}
	rawCredential, err := securetoken.Generate("node")
	if err != nil {
		return EnrollResult{}, err
	}
	if input.EnrollmentSecret != "" {
		secret, decodeErr := hex.DecodeString(input.EnrollmentSecret)
		if decodeErr != nil || len(secret) != 32 || hex.EncodeToString(secret) != input.EnrollmentSecret {
			return EnrollResult{}, fmt.Errorf("%w: invalid enrollment recovery secret", faults.ErrValidation)
		}
		// Domain-separated identities let the same private attempt recover a lost
		// response. The server retains only the ordinary credential hash.
		derive := func(domain string) []byte {
			mac := hmac.New(sha256.New, secret)
			mac.Write([]byte("hl-panel/enrollment/" + domain + "/v1\x00" + input.RawToken))
			return mac.Sum(nil)
		}
		nodeID = "nod_" + hex.EncodeToString(derive("node-id")[:16])
		rawCredential = "node_" + base64.RawURLEncoding.EncodeToString(derive("credential"))
	}
	node := nodes.Node{
		ID:             nodeID,
		Hostname:       strings.TrimSpace(input.Hostname),
		DialHost:       strings.TrimSpace(input.DialHost),
		Platform:       strings.TrimSpace(input.Platform),
		Architecture:   strings.TrimSpace(input.Architecture),
		AgentVersion:   strings.TrimSpace(input.AgentVersion),
		Capabilities:   append([]string(nil), input.Capabilities...),
		EngineVersions: map[string]string{},
		Resources:      map[string]any{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	event, err := audit.NewEvent(now, "enrollment_token", "", "node.enroll", "node", nodeID, "succeeded", map[string]any{
		"hostname": node.Hostname,
	})
	if err != nil {
		return EnrollResult{}, err
	}
	consumed, err := s.repository.ConsumeEnrollmentToken(ctx, ConsumeInput{
		TokenHash:      securetoken.Hash(input.RawToken),
		Node:           node,
		CredentialHash: securetoken.Hash(rawCredential),
		AllowReplay:    input.EnrollmentSecret != "",
	}, now, event)
	if err != nil {
		return EnrollResult{}, err
	}
	return EnrollResult{NodeID: consumed.ID, NodeCredential: rawCredential}, nil
}

// Revoke invalidates an unused enrollment token. The raw token is never
// accepted by this operation and therefore cannot be echoed or recovered.
func (s *Service) Revoke(ctx context.Context, administratorID, tokenID, idempotencyKey string) (RevokeResult, bool, error) {
	administratorID = strings.TrimSpace(administratorID)
	tokenID = strings.TrimSpace(tokenID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if administratorID == "" || tokenID == "" || idempotencyKey == "" || len(administratorID) > 128 || len(tokenID) > 128 || len(idempotencyKey) > 128 {
		return RevokeResult{}, false, fmt.Errorf("%w: token, administrator, and Idempotency-Key are required", faults.ErrValidation)
	}
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", administratorID, "enrollment_token.revoke", "enrollment_token", tokenID, "succeeded", map[string]any{"token_invalidated": true})
	if err != nil {
		return RevokeResult{}, false, err
	}
	return s.repository.RevokeEnrollmentToken(ctx, RevokeInput{
		ID: tokenID, RevokedBy: administratorID, IdempotencyKey: idempotencyKey,
		RequestSHA256: tokenRevocationDigest(tokenID), RevokedAt: now,
	}, event)
}

func validateNodeIdentity(input EnrollInput) error {
	fields := map[string]string{
		"hostname":      input.Hostname,
		"platform":      input.Platform,
		"architecture":  input.Architecture,
		"agent_version": input.AgentVersion,
	}
	for name, value := range fields {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || len(trimmed) > 255 {
			return fmt.Errorf("%w: %s must contain 1 to 255 characters", faults.ErrValidation, name)
		}
	}
	if input.DialHost != "" {
		if _, err := serviceaddress.NormalizeHost(input.DialHost); err != nil {
			return fmt.Errorf("%w: invalid dial_host: %v", faults.ErrValidation, err)
		}
	}
	if len(input.Capabilities) > 128 {
		return fmt.Errorf("%w: too many capabilities", faults.ErrValidation)
	}
	return nil
}

func tokenRevocationDigest(tokenID string) string {
	hash := sha256.Sum256([]byte("enrollment_token.revoke\x00" + tokenID))
	return hex.EncodeToString(hash[:])
}
