package panellicenseissuer

import (
	"context"
	p "github.com/hongle/hl-panel/internal/panellicense"
	"testing"
	"time"
)

func TestPostgresUntilReplayAfterExpiryAndShortLease(t *testing.T) {
	a := testAuthority(t)
	ctx := context.Background()
	input := issueInput("short-term.example.test")
	end := a.clock.now().Add(time.Minute)
	input.Term = p.Term{Kind: "until", ExpiresAt: &end}
	key := testKey(t, "until_")
	issued, err := a.s.Issue(ctx, key, input)
	if err != nil {
		t.Fatal(err)
	}
	i := newInstance(t)
	claims := a.commit(t, i, a.prepare(t, i, issued))
	if !claims.LeaseValidUntil.Equal(end) || !claims.RefreshAfter.After(claims.IssuedAt) || !claims.RefreshAfter.Before(claims.LeaseValidUntil) {
		t.Fatal("short commercial term overran lease", claims)
	}
	a.clock.add(2 * time.Minute)
	again, err := a.s.Issue(ctx, key, input)
	if err != nil || !again.Replayed || again.Code != "" || again.License.ID != issued.License.ID {
		t.Fatal("until retry failed after expiry", err)
	}
	expired, err := a.sync(t, i, input.Domain, issued.License.ID)
	if err != nil || expired.Status != "expired" || !expired.LeaseValidUntil.After(expired.RefreshAfter) {
		t.Fatal("expired instance must retain a signed sync lease", err)
	}
	_, err = a.s.Issue(ctx, testKey(t, "new-expired_"), input)
	requireCode(t, err, "invalid_request")
}

func TestPostgresPendingExtensionAndPerpetualRecovery(t *testing.T) {
	a := testAuthority(t)
	ctx := context.Background()
	issued := a.issue(t, "pending-extend.example.test")
	input := mutation(1)
	input.Term = &p.Term{Kind: "days", Days: 2}
	extended, err := a.s.Mutate(ctx, issued.License.ID, "extend", testKey(t, "pending_"), input)
	if err != nil || extended.License.Term.Days != 3 || extended.License.ActivatedAt != nil || extended.License.LicenseExpiresAt != nil {
		t.Fatal("pending term must start at activation", err)
	}
	// Extending a pending license does not rotate the credential.
	extended.Code = issued.Code
	i := newInstance(t)
	bound := a.commit(t, i, a.prepare(t, i, extended))
	if !bound.LicenseExpiresAt.Equal(a.clock.now().Add(72 * time.Hour)) {
		t.Fatal("pending extension was not applied")
	}
	a.clock.add(96 * time.Hour)
	input = mutation(bound.Revision)
	input.Term = &p.Term{Kind: "perpetual"}
	perpetual, err := a.s.Mutate(ctx, issued.License.ID, "extend", testKey(t, "perpetual_"), input)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := a.sync(t, i, issued.License.Domain, issued.License.ID)
	if err != nil || recovered.Status != "active" || recovered.LicenseExpiresAt != nil || recovered.Term.Kind != "perpetual" || recovered.Revision != perpetual.License.Revision {
		t.Fatal("perpetual conversion failed", err)
	}
	input = mutation(recovered.Revision)
	input.Term = &p.Term{Kind: "days", Days: 1}
	_, err = a.s.Mutate(ctx, issued.License.ID, "extend", testKey(t, "shorten_"), input)
	requireCode(t, err, "operation_not_allowed")
}
