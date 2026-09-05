package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiusage/internal/config"
)

func TestBridgeMissingKeyNeverPrompts(t *testing.T) {
	a := &application{home: t.TempDir(), config: config.Default(), prompt: func(context.Context) (string, error) { t.Fatal("prompt called"); return "", nil }}
	var out bytes.Buffer
	if code := a.runBridge(context.Background(), strings.NewReader(`{"schemaVersion":1,"opencodeKey":null}`), &out); code != 1 {
		t.Fatalf("code %d", code)
	}
	var result bridgeResponse
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Statuses) != 3 || result.Statuses[2].ErrorCode != "missing_key" {
		t.Fatalf("bad response %+v", result)
	}
}

func TestBridgeErrorsAndDeferred(t *testing.T) {
	for _, test := range []struct {
		status int
		want   string
	}{{401, "unauthorized"}, {403, "not_entitled"}, {429, "rate_limited"}} {
		t.Run(test.want, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer fake-secret" {
					t.Error("bad auth")
				}
				w.Header().Set("Retry-After", "300")
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			a := &application{home: t.TempDir(), config: config.Default(), client: server.Client(), opencodeEndpoint: server.URL}
			var out bytes.Buffer
			code := a.runBridge(context.Background(), strings.NewReader(`{"schemaVersion":1,"opencodeKey":"fake-secret"}`), &out)
			var result bridgeResponse
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if code != 1 || result.Statuses[2].ErrorCode != test.want || strings.Contains(out.String(), "fake-secret") {
				t.Fatal("invalid or unsafe response")
			}
			if test.status == 429 && result.Statuses[2].RetryAfter == nil {
				t.Fatal("missing retry time")
			}
			out.Reset()
			a.runBridge(context.Background(), strings.NewReader(`{"schemaVersion":1,"opencodeKey":"fake-secret","skipProviders":["opencode"]}`), &out)
			if calls != 1 || !strings.Contains(out.String(), `"deferred":true`) {
				t.Fatal("deferred provider fetched")
			}
		})
	}
}

func TestBridgePassesClaudeOAuthTokenToUsageEndpoint(t *testing.T) {
	const token = "fake-claude-oauth-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/usage" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
			t.Errorf("anthropic-beta = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":12,"resets_at":"2026-09-05T21:00:00Z"},"seven_day":{"utilization":34,"resets_at":"2026-09-12T00:00:00Z"}}`))
	}))
	defer server.Close()

	a := &application{
		home:           t.TempDir(),
		config:         config.Default(),
		client:         server.Client(),
		claudeEndpoint: server.URL + "/api/oauth/usage",
	}
	var out bytes.Buffer
	request := `{"schemaVersion":1,"opencodeKey":null,"claudeOAuthToken":"` + token + `","skipProviders":["opencode"]}`
	if code := a.runBridge(context.Background(), strings.NewReader(request), &out); code != 0 {
		t.Fatalf("code = %d, response = %s", code, out.String())
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("Claude OAuth token leaked into bridge response")
	}
	var result bridgeResponse
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, status := range result.Statuses {
		if status.Name != "claude" {
			continue
		}
		if status.ErrorCode != "" || len(status.Usages) != 2 || status.Usages[0].Source != "official-endpoint" {
			t.Fatalf("Claude status = %+v", status)
		}
		return
	}
	t.Fatal("Claude status not found")
}

func TestBridgeRejectsInvalidRequests(t *testing.T) {
	for _, request := range []string{`{`, `{"schemaVersion":2}`, `{"schemaVersion":1} {}`, strings.Repeat(" ", 16385)} {
		a := &application{home: t.TempDir(), config: config.Default()}
		var out bytes.Buffer
		if code := a.runBridge(context.Background(), strings.NewReader(request), &out); code != 2 || out.Len() != 0 {
			t.Fatal("invalid request accepted")
		}
	}
}
