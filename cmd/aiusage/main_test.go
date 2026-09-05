package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aiusage/internal/auth"
	"aiusage/internal/config"
)

type testKeyStore struct {
	key      string
	savedKey string
}

func (s *testKeyStore) Load(context.Context) (string, error) {
	if s.key == "" {
		return "", auth.ErrNotFound
	}
	return s.key, nil
}

func (s *testKeyStore) Save(_ context.Context, key string) error {
	s.savedKey = key
	s.key = key
	return nil
}

func TestCollectPromptsForReplacementAfterUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer old-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"status":"ok","percent":10,"resetsAt":"2026-09-05T16:00:00Z"},"weekly":{"status":"ok","percent":20,"resetsAt":"2026-09-08T00:00:00Z"},"monthly":{"status":"ok","percent":30,"resetsAt":"2026-10-01T00:00:00Z"}}}`))
	}))
	defer server.Close()

	store := &testKeyStore{key: "old-key"}
	promptCalls := 0
	app := &application{
		home:             t.TempDir(),
		config:           config.Default(),
		store:            store,
		client:           server.Client(),
		opencodeEndpoint: server.URL,
		key:              "old-key",
		prompt: func(context.Context) (string, error) {
			promptCalls++
			return "new-key", nil
		},
	}

	snapshot := app.collect(context.Background(), false)
	if promptCalls != 1 || store.savedKey != "new-key" {
		t.Fatalf("promptCalls = %d, savedKey = %q; want one replacement save", promptCalls, store.savedKey)
	}
	for _, status := range snapshot.Statuses {
		if status.Name == "opencode" {
			if status.Err != nil || len(status.Usages) != 3 {
				t.Fatalf("OpenCode status = %+v, want successful replacement fetch", status)
			}
			return
		}
	}
	t.Fatal("OpenCode status not found")
}

func TestPrepareKeyDoesNotPromptWhenKeychainHasKey(t *testing.T) {
	store := &testKeyStore{key: "stored-key"}
	prompted := false
	app := &application{
		config: config.Default(),
		store:  store,
		prompt: func(context.Context) (string, error) {
			prompted = true
			return "new-key", nil
		},
	}

	app.prepareKey(context.Background())
	if app.key != "stored-key" || prompted {
		t.Fatalf("key = %q, prompted = %v; want stored key without prompt", app.key, prompted)
	}
}

func TestRunTUIUsesAlternateScreen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app := &application{
		home: t.TempDir(),
		config: config.Config{
			Interval:  time.Minute,
			Providers: map[string]bool{},
		},
	}
	var output bytes.Buffer
	if err := app.runTUI(ctx, &output); err != nil {
		t.Fatalf("runTUI() error = %v", err)
	}
	text := output.String()
	if !strings.Contains(text, "\x1b[?1049h") {
		t.Fatalf("runTUI() = %q, want alternate screen entry", text)
	}
	if !strings.Contains(text, "\x1b[?1049l") {
		t.Fatalf("runTUI() = %q, want alternate screen restore", text)
	}
}
