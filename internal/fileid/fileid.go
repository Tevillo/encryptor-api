// Package fileid creates the random link IDs that identify files. An ID is
// both the secret part of a share link and the encrypted file's name on disk.
package fileid

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// randomBytes = 16 bytes = 128 bits: far too many possibilities to guess.
const randomBytes = 16

// Len is the length of an ID: 16 bytes in unpadded base64url is 22 characters.
const Len = 22

var enc = base64.RawURLEncoding.Strict()

// New returns a fresh random ID. It uses only A-Z a-z 0-9 - _, so it is safe
// in URLs and file names.
func New() (string, error) {
	b := make([]byte, randomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate file id: %w", err)
	}
	return enc.EncodeToString(b), nil
}

// Valid reports whether s has the exact shape of an ID. Checking this before
// touching the database or disk rejects junk such as "../etc/passwd" early.
func Valid(s string) bool {
	if len(s) != Len {
		return false
	}
	b, err := enc.DecodeString(s)
	return err == nil && len(b) == randomBytes
}
