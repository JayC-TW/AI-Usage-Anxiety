//go:build !darwin || !cgo

package auth

func saveNativeKey(service, account, key string) error {
	return ErrKeychainUnavailable
}

func loadNativeKey(service, account string) (string, error) {
	return "", ErrKeychainUnavailable
}
