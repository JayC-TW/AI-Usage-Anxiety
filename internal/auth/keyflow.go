package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNotFound = errors.New("key not found")
	ErrEmptyKey = errors.New("empty API key")
)

type Store interface {
	Load(context.Context) (string, error)
	Save(context.Context, string) error
}

type PromptFunc func(context.Context) (string, error)

// ResolveKey returns the stored key unless force is true or the stored key
// cannot be loaded. In those cases it prompts once and stores the replacement.
func ResolveKey(ctx context.Context, store Store, prompt PromptFunc, force bool) (string, error) {
	if store == nil {
		return "", errors.New("key store is nil")
	}
	if prompt == nil {
		return "", errors.New("key prompt is nil")
	}

	if !force {
		if key, err := store.Load(ctx); err == nil && strings.TrimSpace(key) != "" {
			return strings.TrimSpace(key), nil
		}
	}

	key, err := prompt(ctx)
	if err != nil {
		return "", fmt.Errorf("prompt for OpenCode Go API key: %w", err)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ErrEmptyKey
	}
	if strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("%w: key contains a line break", ErrEmptyKey)
	}
	if err := store.Save(ctx, key); err != nil {
		return "", fmt.Errorf("save OpenCode Go API key: %w", err)
	}
	return key, nil
}
