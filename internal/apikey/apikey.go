// Package apikey generates API keys and hashes them for storage.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// Prefix marks a string as one of this service's API keys, so a leaked key is
// easy to recognise in logs or pastes.
const Prefix = "fd_"

// randomBytes is the key's entropy: 32 bytes = 256 bits, far beyond guessing.
const randomBytes = 32

// Generate returns a new key: "fd_" followed by 32 random bytes in base64url.
func Generate() (string, error) {
	b := make([]byte, randomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash returns the SHA-256 of key, which is what the database stores. A fast
// hash is enough because the key is long and random, unlike a password.
func Hash(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}
