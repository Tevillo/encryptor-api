package cleaner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tevillo/encryptor-api/internal/db"
	"github.com/Tevillo/encryptor-api/internal/fileid"
	"github.com/Tevillo/encryptor-api/internal/keywrap"
	"github.com/Tevillo/encryptor-api/internal/store"
)

type env struct {
	db    *db.DB
	store *store.Store
	c     *Cleaner
	dir   string
	keyID int64
}

func setup(t *testing.T) env {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	keyID, err := d.CreateAPIKey(ctx, "alice", []byte("hash-a"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	master, _ := keywrap.NewDataKey()
	dir := filepath.Join(tmp, "files")
	s, err := store.New(d, dir, master, db.Limits{MaxFileBytes: 1 << 20, MaxTotalBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return env{db: d, store: s, c: New(d, dir, time.Hour, log), dir: dir, keyID: keyID}
}

// save stores a small file that expires in 1 hour.
func (e env) save(t *testing.T, keyID, downloads int64) string {
	t.Helper()
	id, err := e.store.Save(context.Background(), bytes.NewReader([]byte("hello")), store.Upload{
		APIKeyID: keyID, OriginalName: "a.txt", ContentType: "text/plain",
		Size: 5, Downloads: downloads, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// at makes the cleaner see the given offset from now.
func (e env) at(d time.Duration) { e.c.now = func() time.Time { return time.Now().Add(d) } }

func (e env) onDisk(id string) bool {
	_, err := os.Stat(filepath.Join(e.dir, id))
	return err == nil
}

func (e env) sweep(t *testing.T) Result {
	t.Helper()
	res, err := e.c.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e env) status(t *testing.T, id string) (status string, removed bool) {
	t.Helper()
	var r int
	err := e.db.QueryRowContext(context.Background(),
		`SELECT status, file_removed FROM files WHERE id = ?`, id).Scan(&status, &r)
	if err != nil {
		t.Fatal(err)
	}
	return status, r == 1
}

// download takes a slot and finishes it with the given outcome.
func (e env) download(t *testing.T, id string, success bool) {
	t.Helper()
	ctx := context.Background()
	dl, err := e.store.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if success {
		if _, err := dl.WriteTo(io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dl.Finish(ctx, success); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredFileIsRemoved(t *testing.T) {
	e := setup(t)
	id := e.save(t, e.keyID, 3)

	e.at(30 * time.Minute) // not yet expired
	if res := e.sweep(t); res != (Result{}) || !e.onDisk(id) {
		t.Fatalf("unexpired file touched: %+v", res)
	}

	e.at(2 * time.Hour)
	if res := e.sweep(t); res != (Result{Marked: 1, Removed: 1}) {
		t.Fatalf("got %+v", res)
	}
	if e.onDisk(id) {
		t.Fatal("expired file still on disk")
	}
	if st, removed := e.status(t, id); st != db.StatusDeleted || !removed {
		t.Fatalf("status=%s removed=%v", st, removed)
	}
	// Nothing left to do on the next sweep.
	if res := e.sweep(t); res != (Result{}) {
		t.Fatalf("second sweep: %+v", res)
	}
}

func TestUsedUpFileIsRemoved(t *testing.T) {
	e := setup(t)
	id := e.save(t, e.keyID, 1)

	e.download(t, id, false) // a failed download doesn't use it up
	if res := e.sweep(t); res != (Result{}) {
		t.Fatalf("after failed download: %+v", res)
	}
	e.download(t, id, true)
	if res := e.sweep(t); res != (Result{Marked: 1, Removed: 1}) || e.onDisk(id) {
		t.Fatalf("after last download: %+v, on disk=%v", res, e.onDisk(id))
	}
}

func TestActiveDownloadIsWaitedFor(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id := e.save(t, e.keyID, 3)

	dl, err := e.store.Open(ctx, id) // download in progress
	if err != nil {
		t.Fatal(err)
	}
	e.at(2 * time.Hour) // the file expires mid-download
	if res := e.sweep(t); res != (Result{}) || !e.onDisk(id) {
		t.Fatalf("file removed during a download: %+v", res)
	}
	if _, err := dl.WriteTo(io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := dl.Finish(ctx, true); err != nil {
		t.Fatal(err)
	}
	if res := e.sweep(t); res != (Result{Marked: 1, Removed: 1}) || e.onDisk(id) {
		t.Fatalf("after download finished: %+v", res)
	}
}

func TestUnlimitedAndAvailableFilesStay(t *testing.T) {
	e := setup(t)
	unlimited := e.save(t, e.keyID, db.Unlimited)
	limited := e.save(t, e.keyID, 2)
	for range 3 {
		e.download(t, unlimited, true)
	}
	e.download(t, limited, true)

	if res := e.sweep(t); res != (Result{}) || !e.onDisk(unlimited) || !e.onDisk(limited) {
		t.Fatalf("available files touched: %+v", res)
	}
}

func TestStartupCleansLeftovers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	// A download that was in progress when the service stopped.
	active := e.save(t, e.keyID, 1)
	if _, err := e.store.Open(ctx, active); err != nil {
		t.Fatal(err)
	}

	// An upload that was interrupted: pending row plus a partial file.
	pendingID, _ := fileid.New()
	err := e.db.CreatePendingFile(ctx, db.NewFile{
		ID: pendingID, APIKeyID: e.keyID, RemainingDownloads: 1,
		WrappedKey: []byte("w"), WrapNonce: []byte("n"), SizeBytes: 5,
		OriginalName: "b.txt", ContentType: "text/plain",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}, db.Limits{MaxFileBytes: 1 << 20, MaxTotalBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, pendingID), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.c.Startup(ctx); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.db.GetFile(ctx, active); f.CurrentDownloads != 0 || f.Locked {
		t.Fatalf("slot not reset: %+v", f)
	}
	if res := e.sweep(t); res != (Result{Removed: 1}) || e.onDisk(pendingID) {
		t.Fatalf("pending upload not cleaned: %+v", res)
	}
	if !e.onDisk(active) {
		t.Fatal("available file removed")
	}
}

func TestRevokedKeyFiles(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	bobID, err := e.db.CreateAPIKey(ctx, "bob", []byte("hash-b"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a1, a2 := e.save(t, e.keyID, 1), e.save(t, e.keyID, db.Unlimited)
	b1 := e.save(t, bobID, 1)

	n, err := e.db.RevokeAPIKey(ctx, "alice")
	if err != nil || n != 2 {
		t.Fatalf("revoke: n=%d err=%v", n, err)
	}
	// Unavailable immediately, before any sweep.
	for _, id := range []string{a1, a2} {
		if _, err := e.store.Open(ctx, id); !errors.Is(err, db.ErrGone) {
			t.Fatalf("revoked file open err = %v, want ErrGone", err)
		}
	}
	if res := e.sweep(t); res != (Result{Removed: 2}) || e.onDisk(a1) || e.onDisk(a2) {
		t.Fatalf("sweep after revoke: %+v", res)
	}
	if !e.onDisk(b1) {
		t.Fatal("another key's file was removed")
	}

	if n, err := e.db.RevokeAPIKey(ctx, "alice"); err != nil || n != 0 {
		t.Fatalf("second revoke: n=%d err=%v", n, err)
	}
	if _, err := e.db.RevokeAPIKey(ctx, "nobody"); !errors.Is(err, db.ErrKeyNotFound) {
		t.Fatalf("unknown key err = %v, want ErrKeyNotFound", err)
	}
}

// Already-missing files, and a crash between marking and removing, are both
// finished by the next sweep.
func TestSweepRecovers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	missing := e.save(t, e.keyID, 1)
	os.Remove(filepath.Join(e.dir, missing))

	halfDone := e.save(t, e.keyID, 1)
	if err := e.db.MarkDeleted(ctx, halfDone); err != nil { // step 1 happened, step 2 didn't
		t.Fatal(err)
	}

	e.at(2 * time.Hour)
	if res := e.sweep(t); res != (Result{Marked: 1, Removed: 2}) {
		t.Fatalf("got %+v", res)
	}
	for _, id := range []string{missing, halfDone} {
		if _, removed := e.status(t, id); !removed || e.onDisk(id) {
			t.Fatalf("%s not finished", id)
		}
	}
}

func TestRunLoop(t *testing.T) {
	e := setup(t)
	e.c.interval = 10 * time.Millisecond
	id := e.save(t, e.keyID, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.c.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	if !e.onDisk(id) {
		t.Fatal("available file removed by the loop")
	}

	e.download(t, id, true) // uses it up while the loop runs
	deadline := time.Now().Add(2 * time.Second)
	for e.onDisk(id) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.onDisk(id) {
		t.Fatal("loop never removed the used-up file")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}
