package security

import "regexp"

var (
	bearerPattern = regexp.MustCompile(`(?i)(Bearer\s+)[^\s,;]+`)
	secretPattern = regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9._~-]+`)
	base64Pattern = regexp.MustCompile(`\b[A-Za-z0-9+/=_-]{33,}\b`)
)

func Mask(message string) string {
	message = bearerPattern.ReplaceAllString(message, `${1}[REDACTED]`)
	message = secretPattern.ReplaceAllString(message, `[REDACTED]`)
	return base64Pattern.ReplaceAllString(message, `[REDACTED]`)
}
