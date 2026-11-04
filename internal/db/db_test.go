package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

func TestOpenAppliesMigrations(t *testing.T) {
	d, path := openTemp(t)
	ctx := context.Background()

	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	v, err := d.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != len(ms) {
		t.Fatalf("schema version = %d, want %d", v, len(ms))
	}

	for _, table := range []string{"api_keys", "files"} {
		var n int
		err := d.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if err != nil || n != 1 {
			t.Fatalf("table %s missing (n=%d, err=%v)", table, n, err)
		}
	}

	// Opening an up-to-date database again must not re-run migrations.
	d.Close()
	d2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	d2.Close()
}

func TestConnectionSettings(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()

	var fk int
	if err := d.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d (err %v), want 1", fk, err)
	}
	var mode string
	if err := d.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q (err %v), want wal", mode, err)
	}
}

func TestAPIKeyRoundTrip(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	id, err := d.CreateAPIKey(ctx, "laptop", []byte("hash-1"), now)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := d.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	k := keys[0]
	if k.ID != id || k.Name != "laptop" || k.Revoked || !k.CreatedAt.Equal(now) {
		t.Fatalf("got %+v", k)
	}
}

func TestAPIKeyUniqueness(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	now := time.Now()

	if _, err := d.CreateAPIKey(ctx, "laptop", []byte("hash-1"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateAPIKey(ctx, "laptop", []byte("hash-2"), now); !errors.Is(err, ErrKeyNameTaken) {
		t.Fatalf("duplicate name error = %v, want ErrKeyNameTaken", err)
	}
	if _, err := d.CreateAPIKey(ctx, "phone", []byte("hash-1"), now); err == nil {
		t.Fatal("duplicate hash was accepted")
	}
}

// The schema itself must reject invalid file rows.
func TestFilesConstraints(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	keyID, err := d.CreateAPIKey(ctx, "laptop", []byte("hash-1"), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		apiKeyID  int64
		status    string
		remaining int64
		current   int64
		created   int64
		expires   int64
	}
	valid := row{keyID, "pending", 1, 0, 100, 200}

	insert := func(r row) error {
		_, err := d.ExecContext(ctx, `
			INSERT INTO files (id, api_key_id, status, remaining_downloads, current_downloads,
			                   wrapped_key, wrap_nonce, size_bytes, original_name, content_type,
			                   created_at, expires_at)
			VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, x'00', x'00', 10, 'a.txt', 'text/plain', ?, ?)`,
			r.apiKeyID, r.status, r.remaining, r.current, r.created, r.expires)
		return err
	}

	if err := insert(valid); err != nil {
		t.Fatalf("valid row rejected: %v", err)
	}
	unlimited := valid
	unlimited.remaining = -1
	if err := insert(unlimited); err != nil {
		t.Fatalf("unlimited (-1) row rejected: %v", err)
	}

	tests := []struct {
		name   string
		modify func(r *row)
	}{
		{"unknown api key", func(r *row) { r.apiKeyID = 999 }},
		{"bad status", func(r *row) { r.status = "gone" }},
		{"remaining below -1", func(r *row) { r.remaining = -2 }},
		{"negative current downloads", func(r *row) { r.current = -1 }},
		{"expires before created", func(r *row) { r.expires = r.created }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := valid
			tt.modify(&r)
			if err := insert(r); err == nil {
				t.Fatal("invalid row was accepted")
			}
		})
	}
}
