package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"
)

func BearerMatches(header, expected string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || expected == "" {
		return false
	}
	actualHash := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(actualHash[:], expectedHash[:]) == 1
}
