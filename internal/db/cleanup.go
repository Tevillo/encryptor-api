package db

import (
	"context"
	"fmt"
	"time"
)

// These queries are used only by the cleaner, which is the one part of the
// program that removes files from disk.

func (d *DB) collectIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MarkFinishedDeleted marks every ready file that has expired or used up its
// downloads, and has no download in progress, as deleted. It returns their IDs.
// A file with an active download is left alone; a later sweep picks it up.
func (d *DB) MarkFinishedDeleted(ctx context.Context, now time.Time) ([]string, error) {
	ids, err := d.collectIDs(ctx, `
		UPDATE files SET status = 'deleted', locked = 1
		WHERE status = 'ready'
		  AND current_downloads = 0
		  AND (expires_at <= ? OR remaining_downloads = 0)
		RETURNING id`, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("mark finished files deleted: %w", err)
	}
	return ids, nil
}

// AbandonPending marks every pending upload as deleted. Run at startup: no
// upload survives a restart, so any pending row is a leftover.
func (d *DB) AbandonPending(ctx context.Context) ([]string, error) {
	ids, err := d.collectIDs(ctx, `
		UPDATE files SET status = 'deleted', locked = 1
		WHERE status = 'pending'
		RETURNING id`)
	if err != nil {
		return nil, fmt.Errorf("abandon pending uploads: %w", err)
	}
	return ids, nil
}

// FilesToRemove lists deleted files whose encrypted data may still be on disk
// and that nobody is downloading.
func (d *DB) FilesToRemove(ctx context.Context) ([]string, error) {
	ids, err := d.collectIDs(ctx, `
		SELECT id FROM files
		WHERE status = 'deleted' AND file_removed = 0 AND current_downloads = 0`)
	if err != nil {
		return nil, fmt.Errorf("list files to remove: %w", err)
	}
	return ids, nil
}

// MarkFileRemoved records that a deleted file's data is gone from disk.
func (d *DB) MarkFileRemoved(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE files SET file_removed = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mark file removed: %w", err)
	}
	return nil
}
