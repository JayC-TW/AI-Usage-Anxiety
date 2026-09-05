package opencode_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aiusage/internal/model"
	"aiusage/internal/provider/opencode"
)

func TestFetchMapsOpenCodeGoUsageWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/usage" {
			t.Fatalf("request = %s %s, want GET /usage", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want bearer test-key", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q, want application/json", got)
		}
		if got := r.Header.Get("User-Agent"); got != "aiusage/0.1" {
			t.Fatalf("User-Agent = %q, want aiusage/0.1", got)
		}
		if got := r.Header.Get("x-opencode-session"); got != "aiusage" {
			t.Fatalf("x-opencode-session = %q, want aiusage", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
            "usage": {
                "rolling": {"status": "ok", "percent": 25.5, "resetsAt": "2026-09-05T16:10:00Z"},
                "weekly": {"status": "ok", "percent": 20, "resetsAt": "2026-09-08T00:00:00Z"},
                "monthly": {"status": "rate-limited", "percent": 100, "resetsAt": "2026-10-01T00:00:00Z"}
            }
        }`))
	}))
	defer server.Close()

	provider := opencode.NewWithEndpoint("test-key", server.URL+"/usage", server.Client())
	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 3 {
		t.Fatalf("len(usages) = %d, want 3", len(usages))
	}

	want := []struct {
		window string
		used   float64
		reset  string
	}{
		{window: "5h", used: 25.5, reset: "2026-09-05T16:10:00Z"},
		{window: "7d", used: 20, reset: "2026-09-08T00:00:00Z"},
		{window: "monthly", used: 100, reset: "2026-10-01T00:00:00Z"},
	}
	for i, expected := range want {
		got := usages[i]
		if got.Provider != "opencode" || got.Window != expected.window || got.Used != expected.used || got.Limit != 100 || got.Unit != "percent" {
			t.Errorf("usage[%d] = %+v, want provider/window/used/limit/unit %q/%q/%v/100/percent", i, got, "opencode", expected.window, expected.used)
		}
		if got.Source != model.SourceEndpoint {
			t.Errorf("usage[%d].Source = %q, want %q", i, got.Source, model.SourceEndpoint)
		}
		if got.ResetAt == nil || got.ResetAt.Format(time.RFC3339) != expected.reset {
			t.Errorf("usage[%d].ResetAt = %v, want %s", i, got.ResetAt, expected.reset)
		}
	}
}

func TestFetchReturnsUnauthorizedForInvalidKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := opencode.NewWithEndpoint("bad-key", server.URL, server.Client())
	_, err := provider.Fetch(context.Background())
	if !errors.Is(err, opencode.ErrUnauthorized) {
		t.Fatalf("Fetch() error = %v, want ErrUnauthorized", err)
	}
}

func TestFetchReturnsEntitlementErrorForForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	provider := opencode.NewWithEndpoint("valid-but-not-entitled", server.URL, server.Client())
	_, err := provider.Fetch(context.Background())
	if !errors.Is(err, opencode.ErrNotEntitled) {
		t.Fatalf("Fetch() error = %v, want ErrNotEntitled", err)
	}
}

func TestFetchDoesNotCallEndpointWithoutKey(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	provider := opencode.NewWithEndpoint("", server.URL, server.Client())
	_, err := provider.Fetch(context.Background())
	if !errors.Is(err, opencode.ErrMissingKey) {
		t.Fatalf("Fetch() error = %v, want ErrMissingKey", err)
	}
	if called {
		t.Fatal("Fetch() called endpoint without key")
	}
}
