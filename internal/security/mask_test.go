package security_test

import (
	"strings"
	"testing"

	"aiusage/internal/security"
)

func TestMaskRemovesCredentialLikeText(t *testing.T) {
	input := "Authorization: Bearer secret-value sk-abcdefghijklmnopqrstuvwxyz ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz1234567890"
	masked := security.Mask(input)

	for _, secret := range []string{"secret-value", "sk-abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz1234567890"} {
		if strings.Contains(masked, secret) {
			t.Fatalf("Mask() = %q, contains secret %q", masked, secret)
		}
	}
}
