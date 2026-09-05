package auth

import (
	"context"
	"errors"
	"strings"
)

var ErrKeychainUnavailable = errors.New("macOS Keychain unavailable")

const (
	defaultService = "aiusage.opencode-go"
	defaultAccount = "aiusage"
	securityPath   = "/usr/bin/security"
)

type KeychainStore struct {
	service string
	account string
}

func NewKeychainStore() *KeychainStore {
	return &KeychainStore{service: defaultService, account: defaultAccount}
}

func (s *KeychainStore) Load(ctx context.Context) (string, error) {
	if s == nil {
		return "", ErrKeychainUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key, err := loadNativeKey(s.service, s.account)
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ErrNotFound
	}
	return key, nil
}

func (s *KeychainStore) Save(ctx context.Context, key string) error {
	if s == nil || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") || len(key) > 8192 {
		return ErrKeychainUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return saveNativeKey(s.service, s.account, strings.TrimSpace(key))
}
