// Package config reads the service's settings from environment variables.
//
// On the server, systemd loads them from the .env file (EnvironmentFile=).
// For the CLI in a shell, load the same file first:
//
//	set -a; . /path/to/.env; set +a
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Defaults for the size limits, in bytes (binary units).
const (
	DefaultMaxFileBytes  int64 = 2 << 30  // 2 GiB
	DefaultMaxTotalBytes int64 = 10 << 30 // 10 GiB
)

// Config holds every setting. Command-line flags may override the paths.
type Config struct {
	DBPath        string // FILEDROP_DB
	MasterKeyPath string // FILEDROP_MASTER_KEY
	DataDir       string // FILEDROP_DATA_DIR: where encrypted files are stored
	MaxFileBytes  int64  // FILEDROP_MAX_FILE_BYTES: largest single upload
	MaxTotalBytes int64  // FILEDROP_MAX_TOTAL_BYTES: all pending + ready files together
}

// Load reads settings through getenv (normally os.Getenv), filling in defaults
// for anything unset.
func Load(getenv func(string) string) (Config, error) {
	dir := defaultDir()
	c := Config{
		DBPath:        orDefault(getenv("FILEDROP_DB"), filepath.Join(dir, "filedrop.db")),
		MasterKeyPath: orDefault(getenv("FILEDROP_MASTER_KEY"), filepath.Join(dir, "master.key")),
		DataDir:       orDefault(getenv("FILEDROP_DATA_DIR"), filepath.Join(dir, "files")),
	}

	var err error
	if c.MaxFileBytes, err = bytesVar(getenv, "FILEDROP_MAX_FILE_BYTES", DefaultMaxFileBytes); err != nil {
		return Config{}, err
	}
	if c.MaxTotalBytes, err = bytesVar(getenv, "FILEDROP_MAX_TOTAL_BYTES", DefaultMaxTotalBytes); err != nil {
		return Config{}, err
	}
	if c.MaxTotalBytes < c.MaxFileBytes {
		return Config{}, fmt.Errorf("FILEDROP_MAX_TOTAL_BYTES (%d) must be at least FILEDROP_MAX_FILE_BYTES (%d)",
			c.MaxTotalBytes, c.MaxFileBytes)
	}
	return c, nil
}

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".local", "share", "filedrop")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func bytesVar(getenv func(string) string, name string, def int64) (int64, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive whole number of bytes, got %q", name, v)
	}
	return n, nil
}
