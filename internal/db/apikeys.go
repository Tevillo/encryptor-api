package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrKeyNameTaken is returned when an API key with the same name exists.
var ErrKeyNameTaken = errors.New("an API key with that name already exists")

// ErrKeyNotFound is returned when no API key matches.
var ErrKeyNotFound = errors.New("no API key with that name")

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

// GetAPIKeyByName looks up a key by its name.
func (d *DB) GetAPIKeyByName(ctx context.Context, name string) (APIKey, error) {
	var (
		k       APIKey
		revoked int
		created int64
	)
	err := d.QueryRowContext(ctx,
		`SELECT id, name, revoked, created_at FROM api_keys WHERE name = ?`, name).
		Scan(&k.ID, &k.Name, &revoked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrKeyNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("get api key: %w", err)
	}
	k.Revoked = revoked == 1
	k.CreatedAt = time.Unix(created, 0)
	return k, nil
}

// RevokeAPIKey revokes the named key and, in the same transaction, marks all
// of its files deleted and locked, so they can't be downloaded from this
// moment on and stop counting against storage. The cleaner removes them from
// disk on its next sweep. It returns how many files were affected. Revoking
// an already-revoked key is allowed.
func (d *DB) RevokeAPIKey(ctx context.Context, name string) (files int64, err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() // no-op after Commit

	var id int64
	err = tx.QueryRowContext(ctx, `UPDATE api_keys SET revoked = 1 WHERE name = ? RETURNING id`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrKeyNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("revoke api key: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE files SET status = 'deleted', locked = 1 WHERE api_key_id = ? AND status != 'deleted'`, id)
	if err != nil {
		return 0, fmt.Errorf("delete revoked key's files: %w", err)
	}
	if files, err = res.RowsAffected(); err != nil {
		return 0, err
	}
	return files, tx.Commit()
}

// GetAPIKeyByHash finds the key whose SHA-256 hash matches. Used to
// authenticate requests: the incoming key is hashed and looked up.
func (d *DB) GetAPIKeyByHash(ctx context.Context, keyHash []byte) (APIKey, error) {
	var (
		k       APIKey
		revoked int
		created int64
	)
	err := d.QueryRowContext(ctx,
		`SELECT id, name, revoked, created_at FROM api_keys WHERE key_hash = ?`, keyHash).
		Scan(&k.ID, &k.Name, &revoked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrKeyNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("get api key: %w", err)
	}
	k.Revoked = revoked == 1
	k.CreatedAt = time.Unix(created, 0)
	return k, nil
}
