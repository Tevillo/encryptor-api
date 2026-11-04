// Package masterkey creates and loads the master key that wraps every
// per-file data key.
package masterkey

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Size is the master key length: 32 bytes for AES-256.
const Size = 32

// ErrExists is returned by Create when the key file is already there.
// Overwriting it would make every stored file unreadable, so Create never does.
var ErrExists = errors.New("master key file already exists; refusing to overwrite it")

// Create writes a new random master key to path with mode 0400 (owner read
// only). It fails with ErrExists if anything is already at path.
func Create(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create master key directory: %w", err)
	}

	// O_EXCL makes "create only if absent" a single atomic step, so two runs
	// can't both write a key. The file can be written once on creation even
	// though its mode is read-only.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("create master key file: %w", err)
	}

	key := make([]byte, Size)
	if _, err := rand.Read(key); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("generate master key: %w", err)
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write master key: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write master key: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("write master key: %w", err)
	}
	return nil
}

// Load reads the master key from path. It refuses a file that other users
// can access or that isn't exactly Size bytes.
func Load(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("master key %s has mode %#o; only its owner may access it (expected 0400)", path, perm)
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	if len(key) != Size {
		return nil, fmt.Errorf("master key %s is %d bytes; expected %d", path, len(key), Size)
	}
	return key, nil
}
