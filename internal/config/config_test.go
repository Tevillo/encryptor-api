package config

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxFileBytes != 2<<30 || c.MaxTotalBytes != 10<<30 {
		t.Fatalf("limits = %d / %d, want 2 GiB / 10 GiB", c.MaxFileBytes, c.MaxTotalBytes)
	}
	if c.DBPath == "" || c.MasterKeyPath == "" || c.DataDir == "" {
		t.Fatal("default paths are empty")
	}
}

func TestLoadFromEnv(t *testing.T) {
	c, err := Load(env(map[string]string{
		"FILEDROP_DB":              "/data/f.db",
		"FILEDROP_MASTER_KEY":      "/keys/master.key",
		"FILEDROP_DATA_DIR":        "/data/files",
		"FILEDROP_MAX_FILE_BYTES":  "100",
		"FILEDROP_MAX_TOTAL_BYTES": "1000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{DBPath: "/data/f.db", MasterKeyPath: "/keys/master.key", DataDir: "/data/files",
		MaxFileBytes: 100, MaxTotalBytes: 1000}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadRejectsBadLimits(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
	}{
		{"not a number", map[string]string{"FILEDROP_MAX_FILE_BYTES": "2GB"}},
		{"zero", map[string]string{"FILEDROP_MAX_TOTAL_BYTES": "0"}},
		{"negative", map[string]string{"FILEDROP_MAX_FILE_BYTES": "-5"}},
		{"total below per-file", map[string]string{"FILEDROP_MAX_FILE_BYTES": "100", "FILEDROP_MAX_TOTAL_BYTES": "99"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(env(tt.vars)); err == nil {
				t.Fatal("Load succeeded, want an error")
			}
		})
	}
}
