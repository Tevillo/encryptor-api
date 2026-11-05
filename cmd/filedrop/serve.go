package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Tevillo/encryptor-api/internal/cleaner"
	"github.com/Tevillo/encryptor-api/internal/config"
	"github.com/Tevillo/encryptor-api/internal/db"
	"github.com/Tevillo/encryptor-api/internal/httpapi"
	"github.com/Tevillo/encryptor-api/internal/masterkey"
	"github.com/Tevillo/encryptor-api/internal/store"
)

// shutdownGrace is how long in-flight requests get to finish on SIGTERM.
const shutdownGrace = 30 * time.Second

// serve runs the service: the HTTP server and the cleaner.
func serve(ctx context.Context, cfg config.Config, args []string) error {
	fs := newFlags("serve")
	dbPath := fs.String("db", cfg.DBPath, "SQLite database file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errUsage
	}

	// Ctrl-C, or SIGTERM from systemd, cancels ctx and starts a clean stop.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Logs go to stderr; under systemd the journal collects them.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	master, err := masterkey.Load(cfg.MasterKeyPath)
	if err != nil {
		return err
	}
	d, err := openDB(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer d.Close()
	st, err := store.New(d, cfg.DataDir, master, db.Limits{
		MaxFileBytes:  cfg.MaxFileBytes,
		MaxTotalBytes: cfg.MaxTotalBytes,
	})
	if err != nil {
		return err
	}

	// Startup cleanup must finish before HTTP accepts requests, so a new
	// upload is never mistaken for a leftover from a previous run.
	cl := cleaner.New(d, cfg.DataDir, cfg.SweepInterval, log)
	if err := cl.Startup(ctx); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	srv := &http.Server{
		Handler:           httpapi.New(d, st, cfg.PublicURL, cfg.MaxFileBytes, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		// No overall read/write timeout: a 2 GiB transfer may take a long
		// time. Stalled transfers are cut off by httpapi's per-chunk deadlines.
	}

	cleanerDone := make(chan struct{})
	go func() {
		defer close(cleanerDone)
		cl.Loop(ctx)
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	log.Info("filedrop serve started", "listen", cfg.Listen, "public_url", cfg.PublicURL,
		"data_dir", cfg.DataDir, "sweep_interval", cfg.SweepInterval.String())

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		stop()
		<-cleanerDone
		return fmt.Errorf("http server: %w", err)
	}

	log.Info("shutting down", "grace", shutdownGrace.String())
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		// Grace period over: cut remaining connections. Their download
		// slots are released on the next startup.
		srv.Close()
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("http server", "err", err)
	}
	<-cleanerDone
	log.Info("filedrop serve stopped")
	return nil
}

func keysRevoke(ctx context.Context, cfg config.Config, args []string) error {
	fs := newFlags("keys revoke")
	dbPath := fs.String("db", cfg.DBPath, "SQLite database file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return errUsage
	}
	name := fs.Arg(0)

	d, err := openDB(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer d.Close()

	n, err := d.RevokeAPIKey(ctx, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Revoked API key %q. %d file(s) are no longer downloadable.\n", name, n)
	if n > 0 {
		fmt.Fprintf(os.Stderr, "Downloads in progress stop within seconds; `filedrop serve` removes the files from disk on its next sweep (every %s).\n", cfg.SweepInterval)
	}
	return nil
}
