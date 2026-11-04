package masterkey

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateWritesOwnerOnlyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "master.key")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o400 {
		t.Fatalf("mode = %#o, want 0400", perm)
	}
	key, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != Size {
		t.Fatalf("key is %d bytes, want %d", len(key), Size)
	}
}

func TestCreateRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	before, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := Create(path); !errors.Is(err, ErrExists) {
		t.Fatalf("second Create error = %v, want ErrExists", err)
	}
	after, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("second Create changed the existing key")
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{
		{"readable by group and others", make([]byte, Size), 0o644},
		{"too short", make([]byte, Size-1), 0o400},
		{"too long", make([]byte, Size+1), 0o400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "master.key")
			if err := os.WriteFile(path, tt.content, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load succeeded, want an error")
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("Load succeeded on a missing file")
	}
}
