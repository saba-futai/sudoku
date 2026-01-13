package config

import (
	"encoding/json"
	"os"
)

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := Config{
		EnablePureDownlink: true,
	}
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.Finalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
