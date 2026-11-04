package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	t0      = time.Unix(1_700_000_000, 0)
	limits  = Limits{MaxFileBytes: 100, MaxTotalBytes: 250}
	idCount atomic.Int64
)

// setup opens a temp database with one API key and returns it with the key ID.
func setup(t *testing.T) (*DB, int64) {
	t.Helper()
	d, _ := openTemp(t)
	keyID, err := d.CreateAPIKey(context.Background(), "test", []byte("hash"), t0)
	if err != nil {
		t.Fatal(err)
	}
	return d, keyID
}

func newFile(keyID int64, size, remaining int64) NewFile {
	return NewFile{
		ID:                 fmt.Sprintf("file-%d", idCount.Add(1)),
		APIKeyID:           keyID,
		RemainingDownloads: remaining,
		WrappedKey:         []byte("wrapped"),
		WrapNonce:          []byte("nonce"),
		SizeBytes:          size,
		OriginalName:       "a.txt",
		ContentType:        "text/plain",
		CreatedAt:          t0,
		ExpiresAt:          t0.Add(24 * time.Hour),
	}
}

// readyFile creates a file and marks it ready.
func readyFile(t *testing.T, d *DB, keyID, remaining int64) string {
	t.Helper()
	ctx := context.Background()
	f := newFile(keyID, 10, remaining)
	if err := d.CreatePendingFile(ctx, f, limits); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkReady(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	return f.ID
}

func TestCreateAndGet(t *testing.T) {
	d, keyID := setup(t)
	ctx := context.Background()
	f := newFile(keyID, 42, 3)

	if err := d.CreatePendingFile(ctx, f, limits); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetFile(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPending || got.SizeBytes != 42 || got.RemainingDownloads != 3 ||
		got.CurrentDownloads != 0 || got.Locked || string(got.WrappedKey) != "wrapped" ||
		!got.ExpiresAt.Equal(f.ExpiresAt) {
		t.Fatalf("got %+v", got)
	}

	if _, err := d.GetFile(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file err = %v, want ErrNotFound", err)
	}
}

func TestCreateLimits(t *testing.T) {
	d, keyID := setup(t)
	ctx := context.Background()

	if err := d.CreatePendingFile(ctx, newFile(keyID, 101, 1), limits); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over per-file cap: err = %v, want ErrTooLarge", err)
	}
	// 100 + 100 = 200 fits in 250; another 100 doesn't; 50 exactly fills it.
	for _, size := range []int64{100, 100} {
		if err := d.CreatePendingFile(ctx, newFile(keyID, size, 1), limits); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.CreatePendingFile(ctx, newFile(keyID, 100, 1), limits); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("over total: err = %v, want ErrStorageFull", err)
	}
	last := newFile(keyID, 50, 1)
	if err := d.CreatePendingFile(ctx, last, limits); err != nil {
		t.Fatalf("exactly at total: %v", err)
	}
	// Deleted files give their space back.
	if err := d.MarkDeleted(ctx, last.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.CreatePendingFile(ctx, newFile(keyID, 50, 1), limits); err != nil {
		t.Fatalf("after delete: %v", err)
	}

	for _, bad := range []int64{0, -2} {
		if err := d.CreatePendingFile(ctx, newFile(keyID, 1, bad), limits); err == nil {
			t.Fatalf("remaining=%d accepted", bad)
		}
	}
}

// Many uploads racing for the same space: exactly as many as fit succeed.
func TestCreateReservationRace(t *testing.T) {
	d, keyID := setup(t)
	ctx := context.Background()
	lim := Limits{MaxFileBytes: 10, MaxTotalBytes: 50} // room for 5

	var ok, full atomic.Int64
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			err := d.CreatePendingFile(ctx, newFile(keyID, 10, 1), lim)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrStorageFull):
				full.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 5 || full.Load() != 15 {
		t.Fatalf("succeeded=%d full=%d, want 5 and 15", ok.Load(), full.Load())
	}
}

func TestMarkReady(t *testing.T) {
	d, keyID := setup(t)
	ctx := context.Background()
	f := newFile(keyID, 10, 1)
	if err := d.CreatePendingFile(ctx, f, limits); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkReady(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkReady(ctx, f.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second MarkReady err = %v, want ErrNotPending", err)
	}
}

func TestAcquireSlotReasons(t *testing.T) {
	d, keyID := setup(t)
	ctx := context.Background()

	pending := newFile(keyID, 10, 1)
	if err := d.CreatePendingFile(ctx, pending, limits); err != nil {
		t.Fatal(err)
	}
	deleted := readyFile(t, d, keyID, 1)
	if err := d.MarkDeleted(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	busy := readyFile(t, d, keyID, 1)
	if _, err := d.AcquireSlot(ctx, busy, t0); err != nil {
		t.Fatal(err)
	}
	expired := readyFile(t, d, keyID, 1)

	tests := []struct {
		name string
		id   string
		now  time.Time
		want error
	}{
		{"unknown", "nope", t0, ErrNotFound},
		{"still uploading", pending.ID, t0, ErrNotFound},
		{"deleted", deleted, t0, ErrGone},
		{"all slots taken", busy, t0, ErrBusy},
		{"expired", expired, t0.Add(24 * time.Hour), ErrExpired},
		{"expired beats busy", busy, t0.Add(25 * time.Hour), ErrExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := d.AcquireSlot(ctx, tt.id, tt.now); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// The scenario from the design: limit 1, two people open the link at once.
func TestAcquireSlotRace(t *testing.T) {
	d, keyID := setup(t)
	ctx := context.Background()
	id := readyFile(t, d, keyID, 1)

	var ok, busy atomic.Int64
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, err := d.AcquireSlot(ctx, id, t0)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrBusy):
				busy.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 1 || busy.Load() != 19 {
		t.Fatalf("acquired=%d busy=%d, want 1 and 19", ok.Load(), busy.Load())
	}
}

func TestSlotLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("limit 1: failure frees the slot, success uses it up", func(t *testing.T) {
		d, keyID := setup(t)
		id := readyFile(t, d, keyID, 1)

		f, err := d.AcquireSlot(ctx, id, t0)
		if err != nil {
			t.Fatal(err)
		}
		if !f.Locked || f.CurrentDownloads != 1 {
			t.Fatalf("after acquire: %+v", f)
		}
		exhausted, err := d.ReleaseSlot(ctx, id, false, t0)
		if err != nil || exhausted {
			t.Fatalf("failed release: exhausted=%v err=%v", exhausted, err)
		}
		f, _ = d.GetFile(ctx, id)
		if f.Locked || f.CurrentDownloads != 0 || f.RemainingDownloads != 1 {
			t.Fatalf("after failed download: %+v", f)
		}

		if _, err := d.AcquireSlot(ctx, id, t0); err != nil {
			t.Fatal(err)
		}
		exhausted, err = d.ReleaseSlot(ctx, id, true, t0)
		if err != nil || !exhausted {
			t.Fatalf("successful release: exhausted=%v err=%v", exhausted, err)
		}
		if _, err := d.AcquireSlot(ctx, id, t0); !errors.Is(err, ErrGone) {
			t.Fatalf("after last download: err = %v, want ErrGone", err)
		}
	})

	t.Run("limit 3: concurrent slots and lock", func(t *testing.T) {
		d, keyID := setup(t)
		id := readyFile(t, d, keyID, 3)

		for i := range 3 {
			if _, err := d.AcquireSlot(ctx, id, t0); err != nil {
				t.Fatalf("acquire %d: %v", i, err)
			}
		}
		if _, err := d.AcquireSlot(ctx, id, t0); !errors.Is(err, ErrBusy) {
			t.Fatalf("4th acquire err = %v, want ErrBusy", err)
		}
		// One finishes: remaining 2, current 2 -> still no free slot.
		if _, err := d.ReleaseSlot(ctx, id, true, t0); err != nil {
			t.Fatal(err)
		}
		if f, _ := d.GetFile(ctx, id); !f.Locked {
			t.Fatalf("should stay locked with remaining=current: %+v", f)
		}
		// One fails: remaining 2, current 1 -> a slot opens.
		if _, err := d.ReleaseSlot(ctx, id, false, t0); err != nil {
			t.Fatal(err)
		}
		if f, _ := d.GetFile(ctx, id); f.Locked || f.RemainingDownloads != 2 || f.CurrentDownloads != 1 {
			t.Fatalf("after failure: %+v", f)
		}
	})

	t.Run("unlimited never locks or decrements", func(t *testing.T) {
		d, keyID := setup(t)
		id := readyFile(t, d, keyID, Unlimited)

		for range 5 {
			f, err := d.AcquireSlot(ctx, id, t0)
			if err != nil {
				t.Fatal(err)
			}
			if f.Locked {
				t.Fatal("unlimited file got locked")
			}
		}
		for range 5 {
			exhausted, err := d.ReleaseSlot(ctx, id, true, t0)
			if err != nil || exhausted {
				t.Fatalf("exhausted=%v err=%v", exhausted, err)
			}
		}
		f, _ := d.GetFile(ctx, id)
		if f.RemainingDownloads != Unlimited || f.CurrentDownloads != 0 || f.Locked {
			t.Fatalf("got %+v", f)
		}
	})

	t.Run("release without a slot", func(t *testing.T) {
		d, keyID := setup(t)
		id := readyFile(t, d, keyID, 1)
		if _, err := d.ReleaseSlot(ctx, id, true, t0); !errors.Is(err, ErrNoActiveSlot) {
			t.Fatalf("err = %v, want ErrNoActiveSlot", err)
		}
		if _, err := d.ReleaseSlot(ctx, "nope", true, t0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("release after expiry keeps it locked", func(t *testing.T) {
		d, keyID := setup(t)
		id := readyFile(t, d, keyID, 3)
		if _, err := d.AcquireSlot(ctx, id, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := d.ReleaseSlot(ctx, id, false, t0.Add(25*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if f, _ := d.GetFile(ctx, id); !f.Locked {
			t.Fatal("expired file was unlocked")
		}
	})
}

func TestMarkDeleted(t *testing.T) {
	d, keyID := setup(t)
	ctx := context.Background()
	id := readyFile(t, d, keyID, 1)

	if err := d.MarkDeleted(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkDeleted(ctx, id); err != nil {
		t.Fatalf("second delete should be a no-op, got %v", err)
	}
	f, _ := d.GetFile(ctx, id)
	if f.Status != StatusDeleted || !f.Locked {
		t.Fatalf("got %+v", f)
	}
	if err := d.MarkDeleted(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestResetSlots(t *testing.T) {
	d, keyID := setup(t)
	ctx := context.Background()

	active := readyFile(t, d, keyID, 1) // locked by an in-progress download
	if _, err := d.AcquireSlot(ctx, active, t0); err != nil {
		t.Fatal(err)
	}
	expired := readyFile(t, d, keyID, 2)
	if _, err := d.AcquireSlot(ctx, expired, t0); err != nil {
		t.Fatal(err)
	}

	if err := d.ResetSlots(ctx, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f, _ := d.GetFile(ctx, active); f.CurrentDownloads != 0 || f.Locked {
		t.Fatalf("active file after reset: %+v", f)
	}

	if err := d.ResetSlots(ctx, t0.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f, _ := d.GetFile(ctx, expired); f.CurrentDownloads != 0 || !f.Locked {
		t.Fatalf("expired file after reset: %+v", f)
	}
}
