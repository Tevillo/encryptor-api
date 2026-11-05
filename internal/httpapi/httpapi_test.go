package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tevillo/encryptor-api/internal/apikey"
	"github.com/Tevillo/encryptor-api/internal/db"
	"github.com/Tevillo/encryptor-api/internal/keywrap"
	"github.com/Tevillo/encryptor-api/internal/store"
)

// lockedBuffer collects log output safely from many goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	t     *testing.T
	ts    *httptest.Server
	api   *Server
	db    *db.DB
	store *store.Store
	dir   string
	key   string
	logs  *lockedBuffer
}

func setup(t *testing.T, lim db.Limits) *env {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	master, _ := keywrap.NewDataKey()
	dir := filepath.Join(tmp, "files")
	st, err := store.New(d, dir, master, lim)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := apikey.Generate()
	if _, err := d.CreateAPIKey(ctx, "test", apikey.Hash(key), time.Now()); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	api := New(d, st, "https://example.com/filedrop", lim.MaxFileBytes, slog.New(slog.NewTextHandler(logs, nil)))
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, ts: ts, api: api, db: d, store: st, dir: dir, key: key, logs: logs}
}

var big = db.Limits{MaxFileBytes: 8 << 20, MaxTotalBytes: 32 << 20}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// put uploads body as name with the given query string and API key.
func (e *env) put(name, query, key string, body []byte) *http.Response {
	e.t.Helper()
	u := e.ts.URL + "/filedrop/" + url.PathEscape(name)
	if query != "" {
		u += "?" + query
	}
	req, _ := http.NewRequest(http.MethodPut, u, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// upload uploads and returns the file ID from the returned link.
func (e *env) upload(name, query string, body []byte) string {
	e.t.Helper()
	resp := e.put(name, query, e.key, body)
	defer resp.Body.Close()
	link, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("upload: %d %s", resp.StatusCode, link)
	}
	s := strings.TrimSpace(string(link))
	id, ok := strings.CutPrefix(s, "https://example.com/filedrop/")
	if !ok {
		e.t.Fatalf("link %q has the wrong prefix", s)
	}
	return id
}

func (e *env) get(id string) *http.Response {
	e.t.Helper()
	resp, err := http.Get(e.ts.URL + "/filedrop/" + id)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func status(t *testing.T, resp *http.Response) int {
	t.Helper()
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestRoundTrip(t *testing.T) {
	e := setup(t, big)
	data := randomBytes(300_000)
	name := "my photo ü.jpg"
	id := e.upload(name, "", data) // defaults: 24h, 1 download

	resp := e.get(id)
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("download: %d %v", resp.StatusCode, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded bytes differ")
	}
	h := resp.Header
	for k, want := range map[string]string{
		"Content-Type":        "image/jpeg",
		"Content-Length":      "300000",
		"Cache-Control":       "no-store",
		"Content-Disposition": `attachment; filename="my photo _.jpg"; filename*=UTF-8''my%20photo%20%C3%BC.jpg`,
	} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}

	// Default limit is 1: the second download is refused.
	if got := status(t, e.get(id)); got != http.StatusGone {
		t.Fatalf("second download: %d, want 410", got)
	}
}

// The exact request `curl -T "holiday photo.jpg" --url-query expires=2h
// --url-query downloads=3 .../filedrop/` sends.
func TestCurlURLQueryRequest(t *testing.T) {
	e := setup(t, big)
	req, _ := http.NewRequest(http.MethodPut,
		e.ts.URL+"/filedrop/holiday%20photo.jpg?expires=2h&downloads=3", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+e.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	link, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d: %s", resp.StatusCode, link)
	}
	id := strings.TrimPrefix(strings.TrimSpace(string(link)), "https://example.com/filedrop/")
	f, err := e.db.GetFile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if f.OriginalName != "holiday photo.jpg" || f.RemainingDownloads != 3 ||
		f.ExpiresAt.Sub(f.CreatedAt) != 2*time.Hour {
		t.Fatalf("settings not applied: %+v", f)
	}

	// A plain "?" on the bare upload URL makes curl drop the filename.
	req, _ = http.NewRequest(http.MethodPut, e.ts.URL+"/filedrop/?expires=2h", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+e.key)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := status(t, resp); got != http.StatusNotFound {
		t.Fatalf("missing filename: %d, want 404", got)
	}
}

func TestUnlimitedDownloads(t *testing.T) {
	e := setup(t, big)
	id := e.upload("a.txt", "downloads=-1&expires=1h", []byte("hello"))
	for i := range 3 {
		if got := status(t, e.get(id)); got != http.StatusOK {
			t.Fatalf("download %d: %d", i, got)
		}
	}
}

func TestUploadRejections(t *testing.T) {
	e := setup(t, db.Limits{MaxFileBytes: 1000, MaxTotalBytes: 1500})
	revoked, _ := apikey.Generate()
	e.db.CreateAPIKey(context.Background(), "old", apikey.Hash(revoked), time.Now())
	e.db.RevokeAPIKey(context.Background(), "old")

	small := []byte("hello")
	tests := []struct {
		name, file, query, key string
		body                   []byte
		want                   int
	}{
		{"no key", "a.txt", "", "", small, 401},
		{"wrong key", "a.txt", "", "fd_nope", small, 401},
		{"revoked key", "a.txt", "", revoked, small, 401},
		{"too large", "a.txt", "", e.key, randomBytes(1001), 413},
		{"expires too long", "a.txt", "expires=25h", e.key, small, 400},
		{"expires not a duration", "a.txt", "expires=tomorrow", e.key, small, 400},
		{"zero downloads", "a.txt", "downloads=0", e.key, small, 400},
		{"downloads below -1", "a.txt", "downloads=-2", e.key, small, 400},
		{"misspelled setting", "a.txt", "expire=2h", e.key, small, 400},
		{"repeated setting", "a.txt", "downloads=1&downloads=5", e.key, small, 400},
		{"name too long", strings.Repeat("a", 256), "", e.key, small, 400},
		{"name with control char", "a\x01.txt", "", e.key, small, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status(t, e.put(tt.file, tt.query, tt.key, tt.body)); got != tt.want {
				t.Fatalf("status %d, want %d", got, tt.want)
			}
		})
	}

	t.Run("no content length", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPut, e.ts.URL+"/filedrop/a.txt", io.NopCloser(strings.NewReader("hi")))
		req.ContentLength = -1 // sent chunked, like curl -T -
		req.Header.Set("Authorization", "Bearer "+e.key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := status(t, resp); got != http.StatusLengthRequired {
			t.Fatalf("status %d, want 411", got)
		}
	})

	t.Run("storage full", func(t *testing.T) {
		e.upload("a.bin", "", randomBytes(1000))
		if got := status(t, e.put("b.bin", "", e.key, randomBytes(600))); got != http.StatusInsufficientStorage {
			t.Fatalf("status %d, want 507", got)
		}
	})

	if entries, _ := os.ReadDir(e.dir); len(entries) != 1 {
		t.Fatalf("rejected uploads left files: %d entries", len(entries))
	}
}

func TestDownloadRejections(t *testing.T) {
	e := setup(t, big)
	id := e.upload("a.txt", "downloads=1", []byte("hello"))

	if got := status(t, e.get("AAAAAAAAAAAAAAAAAAAAAA")); got != http.StatusNotFound {
		t.Fatalf("unknown id: %d", got)
	}
	if got := status(t, e.get("not-an-id")); got != http.StatusNotFound {
		t.Fatalf("malformed id: %d", got)
	}

	resp, err := http.Head(e.ts.URL + "/filedrop/" + id)
	if err != nil {
		t.Fatal(err)
	}
	if got := status(t, resp); got != http.StatusMethodNotAllowed {
		t.Fatalf("HEAD: %d, want 405", got)
	}

	// Someone else is mid-download: busy, and HEAD didn't use anything up.
	dl, err := e.store.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got := status(t, e.get(id)); got != http.StatusConflict {
		t.Fatalf("busy: %d, want 409", got)
	}
	dl.Finish(context.Background(), false)
	if got := status(t, e.get(id)); got != http.StatusOK {
		t.Fatalf("after other download failed: %d, want 200", got)
	}
}

// An upload that stops partway is cut off by the stall timeout and leaves
// nothing behind.
func TestStalledUpload(t *testing.T) {
	e := setup(t, big)
	e.api.StallTimeout = 200 * time.Millisecond

	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPut, e.ts.URL+"/filedrop/a.bin", pr)
	req.ContentLength = 100_000
	req.Header.Set("Authorization", "Bearer "+e.key)
	go func() {
		pw.Write(randomBytes(1000)) // then go silent
		time.Sleep(2 * time.Second)
		pw.CloseWithError(io.ErrClosedPipe)
	}()
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	var n int
	e.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM files WHERE status != 'deleted'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d stalled upload(s) not cleaned up", n)
	}
	if entries, _ := os.ReadDir(e.dir); len(entries) != 0 {
		t.Fatalf("stalled upload left %d file(s)", len(entries))
	}
}

// Revoking the key stops a download that's already running.
func TestRevokeStopsDownload(t *testing.T) {
	e := setup(t, big)
	e.api.RevokeCheck = time.Millisecond
	id := e.upload("big.bin", "", randomBytes(6<<20))

	resp := e.get(id)
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	// The server is now blocked writing into full network buffers.
	if _, err := e.db.RevokeAPIKey(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("download completed after the key was revoked")
	}
}

func TestLogsNeverContainIDs(t *testing.T) {
	e := setup(t, big)
	id := e.upload("a.txt", "", []byte("hello"))
	status(t, e.get(id))
	status(t, e.get(id)) // 410

	logs := e.logs.String()
	if strings.Contains(logs, id) {
		t.Fatalf("logs contain the full file id:\n%s", logs)
	}
	for _, want := range []string{`route="PUT /filedrop/{name}"`, `route="GET /filedrop/{id}"`, "file=" + id[:6]} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q:\n%s", want, logs)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	tests := map[string]string{
		"report.pdf":   `attachment; filename="report.pdf"`,
		`say "hi".txt`: `attachment; filename="say _hi_.txt"; filename*=UTF-8''say%20%22hi%22.txt`,
		"日本.txt":       `attachment; filename="__.txt"; filename*=UTF-8''%E6%97%A5%E6%9C%AC.txt`,
	}
	for in, want := range tests {
		if got := contentDisposition(in); got != want {
			t.Errorf("contentDisposition(%q) = %q, want %q", in, got, want)
		}
	}
}
