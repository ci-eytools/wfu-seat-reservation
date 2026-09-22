package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAreTheKnownWorkingValues(t *testing.T) {
	cfg := Default()
	cfg.Normalize()
	if cfg.Proxy != DefaultProxy {
		t.Errorf("proxy default = %q, want %q", cfg.Proxy, DefaultProxy)
	}
	if cfg.FIDEnc != DefaultFIDEnc || cfg.MappID != DefaultMappID {
		t.Errorf("identifiers = %q/%q", cfg.FIDEnc, cfg.MappID)
	}
	if cfg.PollSeconds != DefaultPollSeconds {
		t.Errorf("poll seconds = %d", cfg.PollSeconds)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

func TestNormalizeRepairsEmptyValues(t *testing.T) {
	cfg := Config{}
	cfg.Normalize()
	if cfg.FIDEnc == "" || cfg.MappID == "" || cfg.LogDir == "" {
		t.Fatalf("normalize left empty identifiers: %+v", cfg)
	}
	if cfg.PollSeconds != DefaultPollSeconds {
		t.Errorf("poll seconds = %d, want %d", cfg.PollSeconds, DefaultPollSeconds)
	}

	// Out-of-range values are clamped rather than rejected.
	cfg = Config{PollSeconds: 100000}
	cfg.Normalize()
	if cfg.PollSeconds != 900 {
		t.Errorf("poll seconds = %d, want it clamped to 900", cfg.PollSeconds)
	}
}

func TestValidateRejectsMalformedValues(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"proxy without scheme", Config{Proxy: "127.0.0.1:7890"}},
		{"bad date", Config{Day: "16/09/2026"}},
		{"short date", Config{Day: "2026-9-1"}},
	}
	for _, tc := range cases {
		cfg := tc.cfg
		cfg.Normalize()
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", tc.name)
		}
	}

	// An empty proxy means a direct connection and is valid.
	direct := Config{Proxy: ""}
	direct.Normalize()
	if err := direct.Validate(); err != nil {
		t.Errorf("empty proxy must be valid: %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WFUSEAT_CONFIG_DIR", dir)

	cfg := Default()
	cfg.Proxy = "http://127.0.0.1:1080"
	cfg.Day = "2026-09-16"
	cfg.ASCIIOnly = true
	cfg.PollSeconds = 45
	cfg.DefaultRoomID = 6299

	path, err := Save(cfg)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("config written to %q, want it under %q", path, dir)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config permissions = %o, want 600", perm)
	}

	loaded, loadedPath, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loadedPath != path {
		t.Errorf("loaded path = %q, want %q", loadedPath, path)
	}
	if loaded != cfg {
		t.Errorf("round trip changed the config:\n got %+v\nwant %+v", loaded, cfg)
	}
}

func TestLoadWithoutFileReturnsDefaults(t *testing.T) {
	t.Setenv("WFUSEAT_CONFIG_DIR", t.TempDir())
	cfg, path, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path == "" {
		t.Error("expected a config path even when the file is absent")
	}
	if cfg.Proxy != DefaultProxy {
		t.Errorf("proxy = %q, want the default", cfg.Proxy)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Load must not create the config file")
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WFUSEAT_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(); err == nil {
		t.Fatal("expected malformed JSON to be reported")
	}
}

func TestUnknownKeysKeepDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WFUSEAT_CONFIG_DIR", dir)
	body := `{"proxy":"http://127.0.0.1:9999","future_option":"ignored"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Proxy != "http://127.0.0.1:9999" {
		t.Errorf("proxy = %q", cfg.Proxy)
	}
	if cfg.FIDEnc != DefaultFIDEnc {
		t.Errorf("omitted keys must keep their default, got %q", cfg.FIDEnc)
	}
}
