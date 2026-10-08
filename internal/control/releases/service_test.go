package releases

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReleasePolicyAndFiltering(t *testing.T) {
	for behind := 0; behind <= 4; behind++ {
		t.Run(fmt.Sprint(behind), func(t *testing.T) {
			body := "["
			for n := 0; n <= behind; n++ {
				if n > 0 {
					body += ","
				}
				body += fmt.Sprintf(`{"tag_name":"v0.1.%d","published_at":"2026-01-01T00:00:00Z"}`, n)
			}
			body += `,{"tag_name":"v99.0.0","draft":true,"published_at":"2026-01-01T00:00:00Z"},{"tag_name":"v98.0.0","prerelease":true,"published_at":"2026-01-01T00:00:00Z"},{"tag_name":"v97.0.0"},{"tag_name":"v0.1.0","published_at":"2026-01-01T00:00:00Z"},{"tag_name":"bad","published_at":"2026-01-01T00:00:00Z"}]`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
					t.Error("private request information")
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			service := New("v0.1.0")
			service.endpoint = server.URL
			result := service.Check(context.Background())
			if result.Behind != behind || result.Latest != fmt.Sprintf("v0.1.%d", behind) || result.Attention != (behind >= 3) || result.CanDefer != (behind >= 1 && behind <= 2) {
				t.Fatalf("incorrect policy: %+v", result)
			}
			if behind >= 3 && result.State != "attention_required" {
				t.Fatal("urgent state missing")
			}
			if len(result.Versions) != behind+1 {
				t.Fatalf("release list not filtered: %+v", result.Versions)
			}
			for i, v := range result.Versions {
				if v.Tag != fmt.Sprintf("v0.1.%d", behind-i) || v.Current != (i == behind) || v.CanUpdate != (i < behind) || v.ReleaseURL != RepositoryURL+"/releases/tag/"+v.Tag {
					t.Fatalf("incorrect version row: %+v", v)
				}
			}
		})
	}
}

func TestCacheFailureAndRecovery(t *testing.T) {
	calls := 0
	failing := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if failing {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"v1.0.0","published_at":"2026-01-01T00:00:00Z"}]`)
	}))
	defer server.Close()
	now := time.Now()
	service := New("v1.0.0")
	service.endpoint = server.URL
	service.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if service.Check(context.Background()).State != "check_failed" {
			t.Fatal("network failure claimed latest")
		}
	}
	if calls != 1 {
		t.Fatal("failure cache missing")
	}
	failing = false
	now = now.Add(time.Minute)
	if service.Check(context.Background()).State != "up_to_date" {
		t.Fatal("recovery failed")
	}
	now = now.Add(14 * time.Minute)
	service.Check(context.Background())
	if calls != 2 {
		t.Fatal("success cache missing")
	}
	now = now.Add(time.Minute)
	service.Check(context.Background())
	if calls != 3 {
		t.Fatal("refresh missing")
	}
}

func TestSemverAndUnpublishedBuilds(t *testing.T) {
	for _, tag := range []string{"development", "v01.0.0", "v1.0", "v1.0.0-beta", "v18446744073709551616.0.0"} {
		if _, ok := parseVersion(tag); ok {
			t.Fatalf("accepted %s", tag)
		}
	}
	a, _ := parseVersion("v0.1.10")
	b, _ := parseVersion("v0.1.9")
	if !newer(a, b) {
		t.Fatal("lexical instead of semantic comparison")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v0.1.9","published_at":"2026-01-01T00:00:00Z"}]`)
	}))
	defer server.Close()
	service := New("v0.1.10")
	service.endpoint = server.URL
	if service.Check(context.Background()).State != "unpublished" {
		t.Fatal("unreleased build claimed verified")
	}
	service = New("development")
	service.endpoint = "http://invalid.invalid"
	if service.Check(context.Background()).State != "unpublished" {
		t.Fatal("development build not recognized")
	}
}

func TestReleaseResponseMustBePublishedAndValid(t *testing.T) {
	for _, body := range []string{"not json", "[]", `[{"tag_name":"v9.0.0","draft":true}]`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		service := New("v0.1.0")
		service.endpoint = server.URL
		if service.Check(context.Background()).State != "check_failed" {
			t.Fatal("invalid releases claimed current")
		}
		server.Close()
	}
}
