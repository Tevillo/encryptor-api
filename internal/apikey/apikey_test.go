package apikey

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateFormat(t *testing.T) {
	key, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, Prefix) {
		t.Fatalf("key %q lacks prefix %q", key, Prefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, Prefix))
	if err != nil {
		t.Fatalf("key body is not base64url: %v", err)
	}
	if len(raw) != randomBytes {
		t.Fatalf("key has %d random bytes, want %d", len(raw), randomBytes)
	}
}

func TestGenerateIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		key, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if seen[key] {
			t.Fatalf("duplicate key %q", key)
		}
		seen[key] = true
	}
}

func TestHash(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		wantSame bool
	}{
		{"same key hashes the same", "fd_abc", "fd_abc", true},
		{"different keys hash differently", "fd_abc", "fd_abd", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ha, hb := Hash(tt.a), Hash(tt.b)
			if len(ha) != 32 {
				t.Fatalf("hash is %d bytes, want 32", len(ha))
			}
			if got := bytes.Equal(ha, hb); got != tt.wantSame {
				t.Fatalf("hashes equal = %v, want %v", got, tt.wantSame)
			}
		})
	}
}
