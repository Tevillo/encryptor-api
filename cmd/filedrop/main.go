// Command filedrop is the encrypted file-drop service and its admin CLI.
//
//	filedrop masterkey init [--master-key PATH]
//	filedrop keys create --name NAME [--db PATH]
//	filedrop keys list [--db PATH]
//	filedrop keys revoke [--db PATH] NAME
//	filedrop serve [--db PATH]
//
// Settings come from FILEDROP_* environment variables (see internal/config
// and .env.example); --db and --master-key override the paths.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Tevillo/encryptor-api/internal/apikey"
	"github.com/Tevillo/encryptor-api/internal/config"
	"github.com/Tevillo/encryptor-api/internal/db"
	"github.com/Tevillo/encryptor-api/internal/masterkey"
)

const usageText = `usage:
  filedrop masterkey init [--master-key PATH]
  filedrop keys create --name NAME [--db PATH]
  filedrop keys list [--db PATH]
  filedrop keys revoke [--db PATH] NAME
  filedrop serve [--db PATH]
`

// errUsage means the command line was wrong; main exits with status 2.
var errUsage = errors.New("bad usage")

func main() {
	err := run(context.Background(), os.Args[1:])
	switch {
	case errors.Is(err, errUsage):
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	case err != nil:
		fmt.Fprintln(os.Stderr, "filedrop:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errUsage
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if args[0] == "serve" {
		return serve(ctx, cfg, args[1:])
	}
	if len(args) < 2 {
		return errUsage
	}
	switch args[0] + " " + args[1] {
	case "masterkey init":
		return masterKeyInit(cfg, args[2:])
	case "keys create":
		return keysCreate(ctx, cfg, args[2:])
	case "keys list":
		return keysList(ctx, cfg, args[2:])
	case "keys revoke":
		return keysRevoke(ctx, cfg, args[2:])
	default:
		return errUsage
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	return fs
}

func masterKeyInit(cfg config.Config, args []string) error {
	fs := newFlags("masterkey init")
	path := fs.String("master-key", cfg.MasterKeyPath, "master key file to create")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if err := masterkey.Create(*path); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Created master key at %s (mode 0400).\n", *path)
	fmt.Fprintln(os.Stderr, "Back it up separately from the data: losing it makes every stored file unreadable.")
	return nil
}

// openDB makes sure the database's directory exists, then opens it.
func openDB(ctx context.Context, path string) (*db.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	return db.Open(ctx, path)
}

func keysCreate(ctx context.Context, cfg config.Config, args []string) error {
	fs := newFlags("keys create")
	dbPath := fs.String("db", cfg.DBPath, "SQLite database file")
	name := fs.String("name", "", "a label for the key, e.g. \"laptop\"")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	*name = strings.TrimSpace(*name)
	if *name == "" || len(*name) > 64 {
		return fmt.Errorf("--name is required and must be at most 64 characters")
	}

	d, err := openDB(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer d.Close()

	key, err := apikey.Generate()
	if err != nil {
		return err
	}
	id, err := d.CreateAPIKey(ctx, *name, apikey.Hash(key), time.Now())
	if err != nil {
		return err
	}

	// Only the key goes to stdout, so `key=$(filedrop keys create ...)` works.
	fmt.Fprintf(os.Stderr, "Created API key %q (id %d). It is shown only this once:\n", *name, id)
	fmt.Println(key)
	return nil
}

func keysList(ctx context.Context, cfg config.Config, args []string) error {
	fs := newFlags("keys list")
	dbPath := fs.String("db", cfg.DBPath, "SQLite database file")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}

	d, err := openDB(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer d.Close()

	keys, err := d.ListAPIKeys(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tCREATED\tREVOKED")
	for _, k := range keys {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%t\n", k.ID, k.Name, k.CreatedAt.Format(time.DateTime), k.Revoked)
	}
	return tw.Flush()
}
