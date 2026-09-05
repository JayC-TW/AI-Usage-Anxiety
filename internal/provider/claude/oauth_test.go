package claude_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"aiusage/internal/model"
	"aiusage/internal/provider/claude"
)

func TestFetchUsesAnthropicOAuthUsage(t *testing.T) {
	now := time.Date(2026, 9, 5, 16, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/usage" {
			t.Fatalf("request = %s %s, want GET /api/oauth/usage", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer claude-oauth-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
			t.Fatalf("anthropic-beta = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":100.0,"resets_at":"2026-09-05T21:00:00Z"},"seven_day":{"utilization":40.0,"resets_at":"2026-09-12T00:00:00Z"}}`))
	}))
	defer server.Close()

	root := fstest.MapFS{".claude/projects/session.jsonl": &fstest.MapFile{Data: []byte(`{"timestamp":"2026-09-05T15:00:00Z"}`)}}
	provider := claude.NewWithOAuth(root, "claude-oauth-token", server.URL+"/api/oauth/usage", server.Client())
	provider.Now = func() time.Time { return now }

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("len(usages) = %d, want 2: %+v", len(usages), usages)
	}
	want := map[string]float64{"5h": 100, "7d": 40}
	for _, usage := range usages {
		if usage.Provider != "claude" || usage.Unit != "percent" || usage.Limit != 100 || usage.Source != model.SourceEndpoint {
			t.Errorf("usage metadata = %+v", usage)
		}
		if usage.Used != want[usage.Window] {
			t.Errorf("%s used = %v, want %v", usage.Window, usage.Used, want[usage.Window])
		}
	}
}

func TestFetchUsesOAuthLimitsOnlyForUnscopedWindows(t *testing.T) {
	now := time.Date(2026, 9, 5, 16, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"limits":[{"kind":"session","group":"session","percent":11,"resets_at":"2026-09-05T21:00:00Z"},{"kind":"weekly_all","group":"weekly","percent":22,"resets_at":"2026-09-12T00:00:00Z"},{"kind":"weekly_scoped","group":"weekly","percent":99}]}`))
	}))
	defer server.Close()

	provider := claude.NewWithOAuth(nil, "claude-oauth-token", server.URL, server.Client())
	provider.Now = func() time.Time { return now }
	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 || usages[0].Window != "5h" || usages[1].Window != "7d" {
		t.Fatalf("usages = %+v, want unscoped 5h and 7d", usages)
	}
	if usages[0].Used != 11 || usages[1].Used != 22 {
		t.Fatalf("usages = %+v, want 11%% and 22%%", usages)
	}
}

func TestFetchFallsBackToLocalWhenOAuthIsUnavailable(t *testing.T) {
	now := time.Date(2026, 9, 5, 16, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	root := fstest.MapFS{".claude/projects/session.jsonl": &fstest.MapFile{
		ModTime: now.Add(-time.Hour),
		Data:    []byte(`{"timestamp":"2026-09-05T15:00:00Z","rate_limits":{"five_hour":{"used_percentage":12},"seven_day":{"used_percentage":34}}}`),
	}}
	provider := claude.NewWithOAuth(root, "claude-oauth-token", server.URL, server.Client())
	provider.Now = func() time.Time { return now }

	usages, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usages) != 2 || usages[0].Source != model.SourceLocalFile || usages[1].Source != model.SourceLocalFile {
		t.Fatalf("usages = %+v, want local fallback", usages)
	}
}
