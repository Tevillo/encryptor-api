// Package keywrap creates per-file data keys and wraps (encrypts) them with
// the master key, so only wrapped keys are ever stored in the database.
//
// Wrapping uses AES-256-GCM. The file's link ID is passed as "associated
// data": it isn't encrypted, but it is part of the authentication check, so a
// wrapped key copied into another file's row will fail to unwrap.
package keywrap

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// KeySize is the size of both the master key and each data key (AES-256).
const KeySize = 32

// ErrUnwrap means the wrapped key could not be recovered: wrong master key,
// wrong file ID, or the stored bytes were altered.
var ErrUnwrap = errors.New("cannot unwrap data key: wrong master key, wrong file, or tampered data")

// NewDataKey returns a fresh random key for encrypting one file.
func NewDataKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("generate data key: %w", err)
	}
	return k, nil
}

func newGCM(master []byte) (cipher.AEAD, error) {
	if len(master) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", KeySize, len(master))
	}
	block, err := aes.NewCipher(master)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Wrap encrypts dataKey with master, bound to fileID. It returns the wrapped
// key and the random nonce used; both must be stored to unwrap later.
func Wrap(master, dataKey []byte, fileID string) (wrapped, nonce []byte, err error) {
	if len(dataKey) != KeySize {
		return nil, nil, fmt.Errorf("data key must be %d bytes, got %d", KeySize, len(dataKey))
	}
	gcm, err := newGCM(master)
	if err != nil {
		return nil, nil, err
	}
	// A fresh random 12-byte nonce per wrap. Every data key is wrapped once,
	// so the chance of ever repeating a nonce under the master key is negligible.
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	wrapped = gcm.Seal(nil, nonce, dataKey, []byte(fileID))
	return wrapped, nonce, nil
}

// Unwrap recovers the data key. It returns ErrUnwrap if anything doesn't match.
func Unwrap(master, wrapped, nonce []byte, fileID string) ([]byte, error) {
	gcm, err := newGCM(master)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, ErrUnwrap
	}
	dataKey, err := gcm.Open(nil, nonce, wrapped, []byte(fileID))
	if err != nil {
		return nil, ErrUnwrap
	}
	return dataKey, nil
}
