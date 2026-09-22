// Package config loads and stores the user-level TUI configuration.
//
// Nothing here touches the remote service or the Python modules; it only holds
// the settings the operator needs (proxy, application identifiers, display
// preferences). Defaults mirror the constants used by the original notebook.
package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/storage"
)

// DefaultProxy matches the working local proxy configuration used by the
// original notebook. Environment proxies are never consulted, so this is the
// only way outbound traffic can be routed.
const DefaultProxy = "http://127.0.0.1:7890"

// DefaultFIDEnc and DefaultMappID mirror seat_client.SeatClient defaults.
const (
	DefaultFIDEnc = "35bbd135397006a8"
	DefaultMappID = "4109435"
)

// DefaultLogDir mirrors login_diagnostics.LOG_DIR relative to the repository.
const DefaultLogDir = "work/login_logs"

// DefaultPollSeconds bounds how long a QR code is polled before it is reported
// as timed out.
const DefaultPollSeconds = 120

// DefaultAutoLeadSeconds is the lead a scheduled request uses when the server has
// not named its reservation opening time.
const DefaultAutoLeadSeconds = 5

// Config is the persisted user configuration.
type Config struct {
	UserAgent string `json:"user_agent,omitempty"`
	// Keep the legacy JSON key so existing delay values become fixed delays.
	DelayMS   int    `json:"delay_max_ms"`
	Account   string `json:"-"`
	Root      string `json:"-"`
	Cron      string `json:"cron,omitempty"`
	ExecuteAt string `json:"-"`
	// Proxy is the HTTP(S) proxy for all outbound requests. Empty means direct.
	Proxy string `json:"proxy"`

	// FIDEnc and MappID identify the seat application.
	FIDEnc string `json:"fid_enc"`
	MappID string `json:"mapp_id"`

	// LogDir receives the redacted request log.
	LogDir string `json:"log_dir"`

	// ASCIIOnly replaces box drawing and status glyphs with ASCII fallbacks for
	// terminals without reliable Unicode support.
	ASCIIOnly bool `json:"ascii_only"`

	// PollSeconds is the QR polling budget.
	PollSeconds int `json:"poll_seconds"`

	// Day optionally preselects a date; empty means the server's day.
	Day string `json:"day,omitempty"`

	// DefaultRoomID optionally preselects a room.
	DefaultRoomID int `json:"default_room_id,omitempty"`

	// AllowSubmit enables sending a reservation. It is on by default; the TUI
	// still asks for confirmation before every write.
	AllowSubmit bool `json:"allow_submit"`

	// the client prefers the target named by the seat page itself, and these exist
	// for a page whose form is built by JavaScript. No endpoint is ever guessed.
	SubmitURL string `json:"submit_url,omitempty"`
	CancelURL string `json:"cancel_url,omitempty"`

	// SubmitMethod is the HTTP method used for the configured URLs.
	SubmitMethod string `json:"submit_method,omitempty"`

	// AutoLeadSeconds is how far ahead of time a scheduled request fires when the
	// server has not said when the reservation window opens.
	AutoLeadSeconds int `json:"auto_lead_seconds,omitempty"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Proxy:           DefaultProxy,
		FIDEnc:          DefaultFIDEnc,
		MappID:          DefaultMappID,
		LogDir:          DefaultLogDir,
		PollSeconds:     DefaultPollSeconds,
		AllowSubmit:     true,
		SubmitMethod:    "POST",
		AutoLeadSeconds: DefaultAutoLeadSeconds,
	}
}

// Normalize fills empty fields with defaults and clamps invalid values.
// AccountUserAgent preserves the supported browser identity while identifying
// each account with an opaque application product token, never its student ID.
func AccountUserAgent(identity string) string {
	digest := sha256.Sum256([]byte("wfuseat-user-agent/v1/" + identity))
	return fmt.Sprintf("%s WFUseat/%x", chaoxing.BrowserUserAgent, digest[:16])
}
func (c *Config) Normalize() {
	if c.UserAgent == "" && c.Account != "" {
		c.UserAgent = AccountUserAgent(c.Account)
	}
	def := Default()
	if strings.TrimSpace(c.FIDEnc) == "" {
		c.FIDEnc = def.FIDEnc
	}
	if strings.TrimSpace(c.MappID) == "" {
		c.MappID = def.MappID
	}
	if strings.TrimSpace(c.LogDir) == "" {
		c.LogDir = def.LogDir
	}
	if c.PollSeconds <= 0 {
		c.PollSeconds = def.PollSeconds
	}
	if c.PollSeconds > 900 {
		c.PollSeconds = 900
	}
	if c.AutoLeadSeconds <= 0 {
		c.AutoLeadSeconds = def.AutoLeadSeconds
	}
	if c.AutoLeadSeconds > 3600 {
		c.AutoLeadSeconds = 3600
	}
	c.Proxy = strings.TrimSpace(c.Proxy)
	c.Day = strings.TrimSpace(c.Day)
	c.SubmitURL = strings.TrimSpace(c.SubmitURL)
	c.CancelURL = strings.TrimSpace(c.CancelURL)
	c.SubmitMethod = strings.ToUpper(strings.TrimSpace(c.SubmitMethod))
	if c.SubmitMethod == "" {
		c.SubmitMethod = "POST"
	}
}

// Validate reports whether the configuration can be used.
func (c Config) Validate() error {
	if c.DelayMS < 0 || c.DelayMS > 60000 {
		return fmt.Errorf("固定延迟需要 0–60000 毫秒")
	}
	if c.Proxy != "" && !strings.Contains(c.Proxy, "://") {
		return fmt.Errorf("代理地址需要形如 http://host:port，收到 %q", c.Proxy)
	}
	if c.Day != "" && !validDay(c.Day) {
		return fmt.Errorf("日期需要 YYYY-MM-DD 格式，收到 %q", c.Day)
	}
	if c.SubmitMethod != "POST" && c.SubmitMethod != "GET" {
		return fmt.Errorf("写接口方法只支持 POST 或 GET，收到 %q", c.SubmitMethod)
	}
	return nil
}

func validDay(day string) bool {
	if len(day) != 10 || day[4] != '-' || day[7] != '-' {
		return false
	}
	for i, r := range day {
		if i == 4 || i == 7 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Dir returns the configuration directory, honouring WFUSEAT_CONFIG_DIR.
func Dir() (string, error) {
	if override := os.Getenv("WFUSEAT_CONFIG_DIR"); override != "" {
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户配置目录: %w", err)
	}
	return filepath.Join(base, "wfuseat"), nil
}

// Path returns the configuration file path.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the configuration, returning defaults when the file is absent.
// The second result is the path actually consulted.
func Load() (Config, string, error) {
	path, err := Path()
	if err != nil {
		return Default(), "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			cfg := Default()
			cfg.Normalize()
			return cfg, path, nil
		}
		return Default(), path, fmt.Errorf("无法读取配置 %s: %w", path, err)
	}
	cfg := Default()
	// Unknown or missing keys keep their default value.
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Default(), path, fmt.Errorf("配置文件 %s 不是合法 JSON: %w", path, err)
	}
	cfg.Normalize()
	return cfg, path, nil
}

// Save writes the configuration with owner-only permissions.
func Save(cfg Config) (string, error) {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	path, err := Path()
	if cfg.Account != "" {
		var dir string
		dir, err = storage.AccountDir(cfg.Root, cfg.Account)
		path = filepath.Join(dir, "config.json")
		cfg.LogDir = filepath.Join(dir, "logs")
	}
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("无法创建配置目录: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	// Write to a temporary file then rename, so a crash cannot truncate the
	// existing configuration.
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return "", err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	_, err = file.Write(raw)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("无法写入配置: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("无法保存配置: %w", err)
	}
	return path, nil
}
