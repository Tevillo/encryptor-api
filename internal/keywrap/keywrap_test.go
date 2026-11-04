package keywrap

import (
	"bytes"
	"errors"
	"testing"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := NewDataKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestWrapRoundTrip(t *testing.T) {
	master, dataKey := mustKey(t), mustKey(t)

	wrapped, nonce, err := Wrap(master, dataKey, "file-1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wrapped, dataKey) {
		t.Fatal("wrapped key contains the plain data key")
	}
	got, err := Unwrap(master, wrapped, nonce, "file-1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dataKey) {
		t.Fatal("unwrapped key differs from the original")
	}
}

func TestWrapUsesFreshNonce(t *testing.T) {
	master, dataKey := mustKey(t), mustKey(t)
	w1, n1, _ := Wrap(master, dataKey, "f")
	w2, n2, _ := Wrap(master, dataKey, "f")
	if bytes.Equal(n1, n2) || bytes.Equal(w1, w2) {
		t.Fatal("two wraps produced the same nonce or output")
	}
}

func TestUnwrapFailures(t *testing.T) {
	master, dataKey := mustKey(t), mustKey(t)
	wrapped, nonce, err := Wrap(master, dataKey, "file-1")
	if err != nil {
		t.Fatal(err)
	}

	flip := func(b []byte) []byte {
		c := bytes.Clone(b)
		c[0] ^= 1
		return c
	}

	tests := []struct {
		name    string
		master  []byte
		wrapped []byte
		nonce   []byte
		fileID  string
	}{
		{"wrong master key", mustKey(t), wrapped, nonce, "file-1"},
		{"key moved to another file's row", master, wrapped, nonce, "file-2"},
		{"tampered wrapped key", master, flip(wrapped), nonce, "file-1"},
		{"tampered nonce", master, wrapped, flip(nonce), "file-1"},
		{"truncated nonce", master, wrapped, nonce[:5], "file-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Unwrap(tt.master, tt.wrapped, tt.nonce, tt.fileID); !errors.Is(err, ErrUnwrap) {
				t.Fatalf("err = %v, want ErrUnwrap", err)
			}
		})
	}
}

func TestBadKeySizes(t *testing.T) {
	if _, _, err := Wrap(make([]byte, 16), mustKey(t), "f"); err == nil {
		t.Fatal("accepted a 16-byte master key")
	}
	if _, _, err := Wrap(mustKey(t), make([]byte, 16), "f"); err == nil {
		t.Fatal("accepted a 16-byte data key")
	}
}
