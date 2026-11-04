package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrKeyNameTaken is returned when an API key with the same name exists.
var ErrKeyNameTaken = errors.New("an API key with that name already exists")

// APIKey is an api_keys row. It never contains the key itself.
type APIKey struct {
	ID        int64
	Name      string
	Revoked   bool
	CreatedAt time.Time
}

// CreateAPIKey stores a new key's name and hash and returns its row ID.
func (d *DB) CreateAPIKey(ctx context.Context, name string, keyHash []byte, now time.Time) (int64, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO api_keys (name, key_hash, created_at) VALUES (?, ?, ?)`,
		name, keyHash, now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: api_keys.name") {
			return 0, ErrKeyNameTaken
		}
		return 0, fmt.Errorf("insert api key: %w", err)
	}
	return res.LastInsertId()
}

// ListAPIKeys returns every key, oldest first.
func (d *DB) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, name, revoked, created_at FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var (
			k       APIKey
			revoked int
			created int64
		)
		if err := rows.Scan(&k.ID, &k.Name, &revoked, &created); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		k.Revoked = revoked == 1
		k.CreatedAt = time.Unix(created, 0)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
