package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tevillo/encryptor-api/internal/db"
	"github.com/Tevillo/encryptor-api/internal/keywrap"
)

const chunk = 64 * 1024 // sio's DARE 2.0 package size

type env struct {
	s     *Store
	db    *db.DB
	keyID int64
	dir   string
}

func setup(t *testing.T, lim db.Limits) env {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	keyID, err := d.CreateAPIKey(ctx, "test", []byte("hash"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	master, _ := keywrap.NewDataKey()
	dir := filepath.Join(tmp, "files")
	s, err := New(d, dir, master, lim)
	if err != nil {
		t.Fatal(err)
	}
	return env{s: s, db: d, keyID: keyID, dir: dir}
}

var bigLimits = db.Limits{MaxFileBytes: 1 << 20, MaxTotalBytes: 4 << 20}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func (e env) upload(size int64, downloads int64) Upload {
	return Upload{
		APIKeyID: e.keyID, OriginalName: "f.bin", ContentType: "application/octet-stream",
		Size: size, Downloads: downloads, TTL: time.Hour,
	}
}

// filesOnDisk lists what's in the data directory.
func (e env) filesOnDisk(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, size := range []int{0, 1, chunk - 1, chunk, chunk + 1, 3*chunk + 123} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			e := setup(t, bigLimits)
			plain := randomBytes(t, size)

			id, err := e.s.Save(ctx, bytes.NewReader(plain), e.upload(int64(size), 1))
			if err != nil {
				t.Fatal(err)
			}

			cipher, err := os.ReadFile(filepath.Join(e.dir, id))
			if err != nil {
				t.Fatal(err)
			}
			if size >= 16 && bytes.Contains(cipher, plain[:16]) {
				t.Fatal("encrypted file contains plaintext")
			}
			info, _ := os.Stat(filepath.Join(e.dir, id))
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("encrypted file mode = %#o, want 0600", info.Mode().Perm())
			}
			if f, _ := e.db.GetFile(ctx, id); f.Status != db.StatusReady {
				t.Fatalf("status = %s, want ready", f.Status)
			}

			dl, err := e.s.Open(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if dl.Size != int64(size) {
				t.Fatalf("decrypted size = %d, want %d", dl.Size, size)
			}
			var out bytes.Buffer
			if _, err := dl.WriteTo(&out); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), plain) {
				t.Fatal("decrypted bytes differ from the original")
			}
			exhausted, err := dl.Finish(ctx, true)
			if err != nil || !exhausted {
				t.Fatalf("finish: exhausted=%v err=%v", exhausted, err)
			}
			if _, err := e.s.Open(ctx, id); !errors.Is(err, db.ErrGone) {
				t.Fatalf("second open err = %v, want ErrGone", err)
			}
		})
	}
}

// Input that doesn't match the declared size leaves nothing behind and frees
// the storage reservation.
func TestSaveSizeMismatch(t *testing.T) {
	ctx := context.Background()
	lim := db.Limits{MaxFileBytes: 1000, MaxTotalBytes: 1000}

	for _, tt := range []struct {
		name   string
		actual int
	}{
		{"shorter than declared", 999},
		{"longer than declared", 1001},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, lim)
			_, err := e.s.Save(ctx, bytes.NewReader(randomBytes(t, tt.actual)), e.upload(1000, 1))
			if !errors.Is(err, ErrSizeMismatch) {
				t.Fatalf("err = %v, want ErrSizeMismatch", err)
			}
			if files := e.filesOnDisk(t); len(files) != 0 {
				t.Fatalf("left files behind: %v", files)
			}
			// The full 1000 bytes of storage are free again.
			if _, err := e.s.Save(ctx, bytes.NewReader(randomBytes(t, 1000)), e.upload(1000, 1)); err != nil {
				t.Fatalf("reservation not freed: %v", err)
			}
		})
	}
}

// A reader that fails partway (like a dropped upload) is cleaned up.
func TestSaveReadError(t *testing.T) {
	ctx := context.Background()
	e := setup(t, bigLimits)
	r := io.MultiReader(bytes.NewReader(randomBytes(t, chunk)), errReader{})
	if _, err := e.s.Save(ctx, r, e.upload(2*chunk, 1)); err == nil {
		t.Fatal("Save succeeded with a failing reader")
	}
	if files := e.filesOnDisk(t); len(files) != 0 {
		t.Fatalf("left files behind: %v", files)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection dropped") }

func TestSaveLimits(t *testing.T) {
	ctx := context.Background()
	e := setup(t, db.Limits{MaxFileBytes: 100, MaxTotalBytes: 150})

	if _, err := e.s.Save(ctx, bytes.NewReader(randomBytes(t, 101)), e.upload(101, 1)); !errors.Is(err, db.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if _, err := e.s.Save(ctx, bytes.NewReader(randomBytes(t, 100)), e.upload(100, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Save(ctx, bytes.NewReader(randomBytes(t, 100)), e.upload(100, 1)); !errors.Is(err, db.ErrStorageFull) {
		t.Fatalf("err = %v, want ErrStorageFull", err)
	}
	if files := e.filesOnDisk(t); len(files) != 1 {
		t.Fatalf("want exactly 1 file on disk, got %v", files)
	}

	for _, ttl := range []time.Duration{0, -time.Second, MaxTTL + time.Second} {
		u := e.upload(1, 1)
		u.TTL = ttl
		if _, err := e.s.Save(ctx, bytes.NewReader([]byte{1}), u); !errors.Is(err, ErrBadUpload) {
			t.Fatalf("ttl %s: err = %v, want ErrBadUpload", ttl, err)
		}
	}
}

func TestTamperedFile(t *testing.T) {
	ctx := context.Background()
	e := setup(t, bigLimits)
	plain := randomBytes(t, 2*chunk)
	id, err := e.s.Save(ctx, bytes.NewReader(plain), e.upload(int64(len(plain)), 1))
	if err != nil {
		t.Fatal(err)
	}

	// Flip one byte in the second chunk.
	path := filepath.Join(e.dir, id)
	os.Chmod(path, 0o600)
	cipher, _ := os.ReadFile(path)
	cipher[chunk+100] ^= 1
	if err := os.WriteFile(path, cipher, 0o600); err != nil {
		t.Fatal(err)
	}

	dl, err := e.s.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dl.WriteTo(io.Discard); err == nil {
		t.Fatal("tampered file decrypted without error")
	}
	if _, err := dl.Finish(ctx, false); err != nil {
		t.Fatal(err)
	}
	// A failed download doesn't use up the download.
	f, _ := e.db.GetFile(ctx, id)
	if f.RemainingDownloads != 1 || f.CurrentDownloads != 0 || f.Locked {
		t.Fatalf("after failed download: %+v", f)
	}
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	e := setup(t, bigLimits)

	if _, err := e.s.Open(ctx, "../../etc/passwd"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("bad id: err = %v, want ErrNotFound", err)
	}

	id, err := e.s.Save(ctx, bytes.NewReader([]byte("hello")), e.upload(5, 1))
	if err != nil {
		t.Fatal(err)
	}

	// Same database, different master key: unwrapping must fail and the
	// slot must be given back.
	otherMaster, _ := keywrap.NewDataKey()
	other, err := New(e.db, e.dir, otherMaster, bigLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(ctx, id); !errors.Is(err, keywrap.ErrUnwrap) {
		t.Fatalf("wrong master: err = %v, want ErrUnwrap", err)
	}
	if f, _ := e.db.GetFile(ctx, id); f.CurrentDownloads != 0 || f.Locked {
		t.Fatalf("slot not released: %+v", f)
	}

	// Truncated encrypted file (even to zero bytes): refused, slot released.
	path := filepath.Join(e.dir, id)
	os.Chmod(path, 0o600)
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Open(ctx, id); err == nil {
		t.Fatal("opened a truncated file")
	}
	if f, _ := e.db.GetFile(ctx, id); f.CurrentDownloads != 0 {
		t.Fatalf("slot not released: %+v", f)
	}

	// Missing encrypted file: error, slot released.
	os.Remove(filepath.Join(e.dir, id))
	if _, err := e.s.Open(ctx, id); err == nil {
		t.Fatal("opened a file that isn't on disk")
	}
	if f, _ := e.db.GetFile(ctx, id); f.CurrentDownloads != 0 {
		t.Fatalf("slot not released: %+v", f)
	}
}
