package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"wfuseat/internal/scheduleclock"
	"wfuseat/internal/storage"
)

func LoadAccount(root, id string) (Config, string, error) {
	dir, err := storage.AccountDir(root, id)
	if err != nil {
		return Config{}, "", err
	}
	path := filepath.Join(dir, "config.json")
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return cfg, path, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return cfg, path, err
		}
	}
	cfg.Account = id
	cfg.Root = root
	cfg.LogDir = filepath.Join(dir, "logs")
	cfg.Normalize()
	return cfg, path, cfg.Validate()
}
func GlobalTime(root string) (string, error) {
	db, err := storage.Open(root)
	if err != nil {
		return "", err
	}
	defer db.Close()
	b, err := db.Get("execution_time")
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "08:00:00", nil
	}
	return string(b), nil
}
func SaveGlobalTime(root, clock string) error {
	if err := scheduleclock.Validate(clock); err != nil {
		return err
	}
	db, err := storage.Open(root)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Put("execution_time", []byte(clock))
}
