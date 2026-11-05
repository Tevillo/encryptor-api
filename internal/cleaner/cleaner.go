// Package cleaner is the one part of the program that removes files.
//
// It runs a periodic sweep instead of per-file timers. A sweep works the same
// whether a file ran out of time, used up its downloads, or was revoked by a
// CLI command in another process, because it reads the shared database.
//
// Each sweep has two steps, in this order:
//  1. Mark ready files that are expired or used up, with no download in
//     progress, as deleted. From then on they can't be downloaded and no
//     longer count against total storage.
//  2. For deleted files nobody is downloading, remove the encrypted file from
//     disk, then set file_removed.
//
// If the program stops between the steps, the next sweep finishes the job.
package cleaner

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Tevillo/encryptor-api/internal/db"
	"github.com/Tevillo/encryptor-api/internal/fileid"
)

// Cleaner removes expired, used-up, revoked and abandoned files.
type Cleaner struct {
	db       *db.DB
	dir      string
	interval time.Duration
	log      *slog.Logger
	now      func() time.Time // replaceable in tests
}

// New returns a Cleaner for encrypted files stored in dir.
func New(d *db.DB, dir string, interval time.Duration, log *slog.Logger) *Cleaner {
	return &Cleaner{db: d, dir: dir, interval: interval, log: log, now: time.Now}
}

// Result counts what one sweep did.
type Result struct {
	Marked  int // files newly marked deleted
	Removed int // encrypted files removed from disk
}

// short shortens a file ID for logs. The full ID is the secret part of a
// share link and must never be logged.
func short(id string) string {
	if len(id) > 6 {
		return id[:6] + "…"
	}
	return id
}

// Startup runs once when the service starts. No download or upload survives
// a restart, so every in-progress download slot is released and every
// pending upload is abandoned.
func (c *Cleaner) Startup(ctx context.Context) error {
	if err := c.db.ResetSlots(ctx, c.now()); err != nil {
		return err
	}
	ids, err := c.db.AbandonPending(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		c.log.Info("abandoned unfinished upload", "file", short(id))
	}
	return nil
}

// Sweep runs one cleanup pass.
func (c *Cleaner) Sweep(ctx context.Context) (Result, error) {
	var res Result

	marked, err := c.db.MarkFinishedDeleted(ctx, c.now())
	if err != nil {
		return res, err
	}
	res.Marked = len(marked)
	for _, id := range marked {
		c.log.Info("file expired or used up", "file", short(id))
	}

	toRemove, err := c.db.FilesToRemove(ctx)
	if err != nil {
		return res, err
	}
	for _, id := range toRemove {
		if err := c.remove(ctx, id); err != nil {
			// Leave file_removed = 0 so the next sweep tries again.
			c.log.Error("could not remove file", "file", short(id), "err", err)
			continue
		}
		res.Removed++
	}
	return res, nil
}

func (c *Cleaner) remove(ctx context.Context, id string) error {
	// IDs come from our own database, but check the shape anyway before
	// building a path, so a bad row can never point outside the data dir.
	if fileid.Valid(id) {
		err := os.Remove(filepath.Join(c.dir, id))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		// Already missing is fine: maybe an earlier sweep removed it and
		// stopped before recording that.
	} else {
		c.log.Warn("skipping disk removal for malformed file id", "file", short(id))
	}
	if err := c.db.MarkFileRemoved(ctx, id); err != nil {
		return err
	}
	c.log.Info("removed file from disk", "file", short(id))
	return nil
}

// Run does the startup work, then Loop.
func (c *Cleaner) Run(ctx context.Context) error {
	if err := c.Startup(ctx); err != nil {
		return err
	}
	return c.Loop(ctx)
}

// Loop sweeps immediately, then every interval until ctx is cancelled. A
// failed sweep is logged and retried next time. Call Startup first; serve
// does so before accepting HTTP requests, so a new upload can't be mistaken
// for a leftover.
func (c *Cleaner) Loop(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		if res, err := c.Sweep(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.log.Error("sweep failed", "err", err)
		} else if res.Marked > 0 || res.Removed > 0 {
			c.log.Info("sweep done", "marked_deleted", res.Marked, "removed_from_disk", res.Removed)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
