package subscriptions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/hongle/hl-panel/internal/control/audit"
	"github.com/hongle/hl-panel/internal/control/customers"
	"github.com/hongle/hl-panel/internal/control/faults"
	"github.com/hongle/hl-panel/internal/control/forwarding"
	"github.com/hongle/hl-panel/internal/idgen"
	"github.com/hongle/hl-panel/internal/securetoken"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var tokenPattern = regexp.MustCompile(`^sub_[A-Za-z0-9_-]{43}$`)

type Service struct {
	repository Repository
	native     NativeResolver
	now        func() time.Time
}

func NewService(repository Repository, native NativeResolver, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository, native, now}
}
func (s *Service) List(ctx context.Context, admin string) ([]Item, error) {
	return s.repository.ListSubscriptions(ctx, admin)
}
func (s *Service) Detail(ctx context.Context, admin, id string) (Record, error) {
	return s.repository.Subscription(ctx, admin, id)
}
func validID(v string) bool {
	return v != "" && len(v) <= 128 && !strings.ContainsAny(v, "/\\") && strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
func Normalize(r Request) (Request, error) {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || utf8.RuneCountInString(r.Name) > 120 || strings.IndexFunc(r.Name, unicode.IsControl) >= 0 || !validID(r.CustomerID) || r.Revision < 0 || r.Revision == math.MaxInt64 || len(r.Lines) < 1 || len(r.Lines) > 100 {
		return Request{}, fmt.Errorf("%w: 名称、客户和 1–100 条线路为必填项", faults.ErrValidation)
	}
	r.Lines = append([]Line(nil), r.Lines...)
	names := map[string]bool{}
	sources := map[string]bool{}
	for i, line := range r.Lines {
		line.Name = strings.TrimSpace(line.Name)
		line.URI = strings.TrimSpace(line.URI)
		line.BindingID = strings.TrimSpace(line.BindingID)
		if line.Name == "" || utf8.RuneCountInString(line.Name) > 100 || strings.IndexFunc(line.Name, unicode.IsControl) >= 0 || names[line.Name] || (line.URI == "") == (line.BindingID == "") {
			return Request{}, fmt.Errorf("%w: 每条线路须有唯一名称，并选择一个凭据或填写一个链接", faults.ErrValidation)
		}
		source := line.BindingID
		if line.BindingID != "" {
			if !validID(line.BindingID) {
				return Request{}, faults.ErrValidation
			}
		} else {
			if _, err := ParseProxy(line.URI, line.Name); err != nil {
				return Request{}, err
			}
			source = line.URI
		}
		if sources[source] {
			return Request{}, fmt.Errorf("%w: 请勿重复添加相同线路", faults.ErrValidation)
		}
		names[line.Name] = true
		sources[source] = true
		r.Lines[i] = line
	}
	return r, nil
}
func (s *Service) Mutate(ctx context.Context, admin, id, operation string, r Request, key string) (Item, bool, error) {
	return s.mutate(ctx, admin, id, operation, r, key, "")
}

// Generate publishes a single native rule in the same durable transaction.
// The repository owns uniqueness, so simultaneous clicks share one token.
func (s *Service) Generate(ctx context.Context, admin, ruleID string, r Request, key string) (Item, bool, error) {
	if !validID(ruleID) {
		return Item{}, false, faults.ErrValidation
	}
	return s.mutate(ctx, admin, "", "generate", r, key, ruleID)
}
func (s *Service) mutate(ctx context.Context, admin, id, operation string, r Request, key, ruleID string) (Item, bool, error) {
	if !validID(admin) || !validID(key) {
		return Item{}, false, faults.ErrValidation
	}
	switch operation {
	case "create", "update", "generate":
		var err error
		r, err = Normalize(r)
		if err != nil {
			return Item{}, false, err
		}
		if ((operation == "create" || operation == "generate") && r.Revision != 0) || (operation == "update" && r.Revision < 1) {
			return Item{}, false, faults.ErrValidation
		}
	case "publish", "rotate", "revoke", "restore":
		if r.Revision < 1 || r.Revision == math.MaxInt64 {
			return Item{}, false, faults.ErrValidation
		}
	default:
		return Item{}, false, faults.ErrValidation
	}
	originalID := id
	if operation == "create" || operation == "generate" {
		var err error
		id, err = idgen.New("subscription")
		if err != nil {
			return Item{}, false, err
		}
	} else if !validID(id) {
		return Item{}, false, faults.ErrValidation
	}
	token := ""
	if operation == "create" || operation == "generate" || operation == "rotate" {
		var err error
		token, err = securetoken.Generate("sub")
		if err != nil {
			return Item{}, false, err
		}
	}
	fingerprint, _ := json.Marshal(struct {
		ID, Operation, RuleID string
		Request               Request
	}{originalID, operation, ruleID, r})
	digest := sha256.Sum256(fingerprint)
	now := s.now().UTC()
	event, err := audit.NewEvent(now, "administrator", admin, "subscription."+operation, "subscription", id, "succeeded", map[string]any{"revision": r.Revision + 1, "line_count": len(r.Lines)})
	if err != nil {
		return Item{}, false, err
	}
	return s.repository.MutateSubscription(ctx, Command{ID: id, AdministratorID: admin, Operation: operation, IdempotencyKey: key, RequestSHA256: hex.EncodeToString(digest[:]), Token: token, ForwardingRuleID: ruleID, Request: r, At: now}, event)
}

// Apply is called inside the repository transaction, after replay and ownership
// checks. Publishing never replaces the token; revocation leaves native accounts intact.
func Apply(previous Record, c Command) (Record, error) {
	r := previous
	if c.Operation == "create" || (c.Operation == "generate" && previous.Item.ID == "") {
		r = Record{Item: Item{ID: c.ID, State: "active", CreatedAt: c.At}, OwnerID: c.AdministratorID, Token: c.Token}
	}
	if r.Item.Revision != c.Request.Revision {
		return Record{}, faults.ErrConflict
	}
	if r.Item.State == "revoked" && c.Operation != "restore" {
		return Record{}, fmt.Errorf("%w: 请先恢复订阅", faults.ErrConflict)
	}
	switch c.Operation {
	case "create", "update", "generate":
		r.Item.Name = c.Request.Name
		r.Item.CustomerID = c.Request.CustomerID
		r.Draft = append([]Line(nil), c.Request.Lines...)
		r.Item.PendingUpdate = true
		if c.Operation == "generate" {
			r.Item.ForwardingRuleID = c.ForwardingRuleID
			r.Published = append([]Line(nil), r.Draft...)
			r.Item.PublishedRevision = c.Request.Revision + 1
			r.Item.PendingUpdate = false
		}
	case "publish":
		r.Published = append([]Line(nil), r.Draft...)
		r.Item.PublishedRevision = c.Request.Revision + 1
		r.Item.PendingUpdate = false
	case "rotate":
		r.Token = c.Token
	case "revoke":
		r.Item.State = "revoked"
	case "restore":
		r.Item.State = "active"
	default:
		return Record{}, faults.ErrValidation
	}
	r.Item.Revision++
	r.Item.UpdatedAt = c.At
	r.Item.LineCount = len(r.Draft)
	r.Item.PublishedLineCount = len(r.Published)
	return r, ValidateRecord(r)
}
func ValidateRecord(r Record) error {
	if !validID(r.Item.ID) || !validID(r.OwnerID) || !tokenPattern.MatchString(r.Token) || r.Item.Revision < 1 || r.Item.CreatedAt.IsZero() || r.Item.UpdatedAt.Before(r.Item.CreatedAt) || (r.Item.State != "active" && r.Item.State != "revoked") || r.Item.PublishedRevision < 0 || r.Item.PublishedRevision > r.Item.Revision || r.Item.LineCount != len(r.Draft) || r.Item.PublishedLineCount != len(r.Published) {
		return faults.ErrValidation
	}
	if r.Item.ForwardingRuleID != "" && (!validID(r.Item.ForwardingRuleID) || len(r.Draft) != 1 || r.Draft[0].BindingID == "" || len(r.Published) != 1 || r.Published[0].BindingID != r.Draft[0].BindingID) {
		return faults.ErrValidation
	}
	if _, err := Normalize(Request{Name: r.Item.Name, CustomerID: r.Item.CustomerID, Lines: r.Draft}); err != nil {
		return err
	}
	if len(r.Published) == 0 {
		if r.Item.PublishedRevision != 0 || !r.Item.PendingUpdate {
			return faults.ErrValidation
		}
	} else {
		if r.Item.PublishedRevision == 0 {
			return faults.ErrValidation
		}
		if _, err := Normalize(Request{Name: r.Item.Name, CustomerID: r.Item.CustomerID, Lines: r.Published}); err != nil {
			return err
		}
		if !r.Item.PendingUpdate && !reflect.DeepEqual(r.Draft, r.Published) {
			return faults.ErrValidation
		}
	}
	return nil
}

type Resolved struct {
	Name  string `json:"name"`
	URI   string `json:"uri,omitempty"`
	Error string `json:"error,omitempty"`
}

func (s *Service) Resolve(ctx context.Context, r Record, draft bool) ([]Resolved, error) {
	if !(r.Item.ForwardingRuleID != "" && r.Item.CustomerID == forwarding.AdministratorSubjectID(r.OwnerID)) {
		customer, err := s.repository.Customer(ctx, r.Item.CustomerID)
		if err != nil || customer.EffectiveStatus(s.now()) != customers.StatusActive {
			return nil, faults.ErrNotFound
		}
	}
	lines := r.Published
	if draft {
		lines = r.Draft
	}
	out := make([]Resolved, 0, len(lines))
	for _, line := range lines {
		uri := line.URI
		var resolveErr error
		if line.BindingID != "" {
			if s.native == nil {
				resolveErr = faults.ErrConflict
			} else {
				uri, resolveErr = s.native.ResolveSubscriptionLine(ctx, r.OwnerID, r.Item.CustomerID, line.BindingID)
			}
		}
		if resolveErr != nil {
			out = append(out, Resolved{Name: line.Name, Error: "线路未就绪或凭据已撤销"})
			continue
		}
		proxy, e := ParseProxy(uri, line.Name)
		if e != nil {
			out = append(out, Resolved{Name: line.Name, Error: "线路参数不完整"})
			continue
		}
		out = append(out, Resolved{Name: line.Name, URI: proxy.URI})
	}
	return out, nil
}
func (s *Service) Public(ctx context.Context, token string) (Record, []Resolved, error) {
	if !tokenPattern.MatchString(token) {
		return Record{}, nil, faults.ErrNotFound
	}
	r, err := s.repository.SubscriptionByToken(ctx, securetoken.Hash(token))
	if err != nil || r.Item.State != "active" || len(r.Published) == 0 {
		return Record{}, nil, faults.ErrNotFound
	}
	lines, err := s.Resolve(ctx, r, false)
	return r, lines, err
}
