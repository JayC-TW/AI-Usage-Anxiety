package auth_test

import (
	"context"
	"errors"
	"testing"

	"aiusage/internal/auth"
)

type memoryStore struct {
	key      string
	loadErr  error
	savedKey string
}

func (s *memoryStore) Load(context.Context) (string, error) {
	if s.loadErr != nil {
		return "", s.loadErr
	}
	if s.key == "" {
		return "", auth.ErrNotFound
	}
	return s.key, nil
}

func (s *memoryStore) Save(_ context.Context, key string) error {
	s.savedKey = key
	return nil
}

func TestResolveKeyUsesStoredKeyWithoutPrompt(t *testing.T) {
	store := &memoryStore{key: "stored-key"}
	prompted := false

	got, err := auth.ResolveKey(context.Background(), store, func(context.Context) (string, error) {
		prompted = true
		return "new-key", nil
	}, false)
	if err != nil {
		t.Fatalf("ResolveKey() error = %v", err)
	}
	if got != "stored-key" {
		t.Fatalf("ResolveKey() = %q, want stored key", got)
	}
	if prompted {
		t.Fatal("ResolveKey() prompted despite stored key")
	}
}

func TestResolveKeyPromptsAndSavesWhenKeyMissing(t *testing.T) {
	store := &memoryStore{}

	got, err := auth.ResolveKey(context.Background(), store, func(context.Context) (string, error) {
		return "entered-key", nil
	}, false)
	if err != nil {
		t.Fatalf("ResolveKey() error = %v", err)
	}
	if got != "entered-key" || store.savedKey != "entered-key" {
		t.Fatalf("ResolveKey() = %q, saved = %q; want entered-key", got, store.savedKey)
	}
}

func TestResolveKeyPromptsWhenStoreCannotLoad(t *testing.T) {
	store := &memoryStore{loadErr: errors.New("keychain unavailable")}

	got, err := auth.ResolveKey(context.Background(), store, func(context.Context) (string, error) {
		return "replacement-key", nil
	}, false)
	if err != nil {
		t.Fatalf("ResolveKey() error = %v", err)
	}
	if got != "replacement-key" || store.savedKey != "replacement-key" {
		t.Fatalf("ResolveKey() = %q, saved = %q; want replacement-key", got, store.savedKey)
	}
}

func TestResolveKeyForcePromptsForReplacement(t *testing.T) {
	store := &memoryStore{key: "old-key"}

	got, err := auth.ResolveKey(context.Background(), store, func(context.Context) (string, error) {
		return "replacement-key", nil
	}, true)
	if err != nil {
		t.Fatalf("ResolveKey() error = %v", err)
	}
	if got != "replacement-key" || store.savedKey != "replacement-key" {
		t.Fatalf("ResolveKey() = %q, saved = %q; want replacement-key", got, store.savedKey)
	}
}

func TestResolveKeyDoesNotSaveEmptyPromptResult(t *testing.T) {
	store := &memoryStore{}

	_, err := auth.ResolveKey(context.Background(), store, func(context.Context) (string, error) {
		return "  ", nil
	}, false)
	if !errors.Is(err, auth.ErrEmptyKey) {
		t.Fatalf("ResolveKey() error = %v, want ErrEmptyKey", err)
	}
	if store.savedKey != "" {
		t.Fatalf("saved empty key %q", store.savedKey)
	}
}
