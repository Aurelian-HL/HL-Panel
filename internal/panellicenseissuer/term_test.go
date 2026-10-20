package panellicenseissuer

import (
	p "github.com/hongle/hl-panel/internal/panellicense"
	"testing"
	"time"
)

func TestCalendarMonthClampShanghai(t *testing.T) {
	for _, tt := range []struct {
		start  string
		months int
		want   string
	}{
		{"2026-01-31T23:30:00+08:00", 1, "2026-02-28T23:30:00+08:00"},
		{"2024-01-31T01:30:00+08:00", 1, "2024-02-29T01:30:00+08:00"},
		{"2026-12-31T12:00:00+08:00", 2, "2027-02-28T12:00:00+08:00"},
	} {
		t.Run(tt.start, func(t *testing.T) {
			base, _ := time.Parse(time.RFC3339, tt.start)
			want, _ := time.Parse(time.RFC3339, tt.want)
			got, err := addTerm(base, p.Term{Kind: "calendar_months", Months: tt.months})
			if err != nil || !got.Equal(want) {
				t.Fatalf("month clamp: %v %v", got, err)
			}
		})
	}
}
func TestPendingTermsCannotShortenOrOverflow(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	l := p.LicenseRecord{Term: p.Term{Kind: "calendar_months", Months: 119}}
	if err := extendTerm(&l, p.Term{Kind: "calendar_months", Months: 2}, now); err == nil {
		t.Fatal("accepted overflow")
	}
	if l.Term.Months != 119 {
		t.Fatal("failed extension changed term")
	}
	if err := extendTerm(&l, p.Term{Kind: "days", Days: 1}, now); err == nil {
		t.Fatal("accepted incompatible pending term")
	}
	if err := extendTerm(&l, p.Term{Kind: "calendar_months", Months: 1}, now); err != nil || l.Term.Months != 120 {
		t.Fatal("pending extension not accumulated", err)
	}
	if err := extendTerm(&l, p.Term{Kind: "perpetual"}, now); err != nil {
		t.Fatal(err)
	}
	if err := extendTerm(&l, p.Term{Kind: "days", Days: 1}, now); err == nil {
		t.Fatal("shortened perpetual term")
	}
}
