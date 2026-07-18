package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const minimumSecretBytes = 32

// Config contains runtime settings loaded from environment variables.
type Config struct {
	ListenAddress    string
	DatabasePath     string
	WebDirectory     string
	AdminPassword    string
	SecretKey        []byte
	CookieSecure     bool
	SyncInterval     time.Duration
	FullSyncInterval time.Duration
	StaleAfter       time.Duration
	IYUUSitesURL     string
	IYUUSyncEnabled  bool
	IYUUSyncInterval time.Duration
}

// Load reads and validates SeedGraph's environment configuration.
func Load() (Config, error) {
	secret, err := parseSecret(os.Getenv("SEEDGRAPH_SECRET_KEY"))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		ListenAddress:    envOrDefault("SEEDGRAPH_LISTEN_ADDR", ":8080"),
		DatabasePath:     envOrDefault("SEEDGRAPH_DATABASE_PATH", "data/seedgraph.db"),
		WebDirectory:     envOrDefault("SEEDGRAPH_WEB_DIR", "frontend/dist"),
		AdminPassword:    os.Getenv("SEEDGRAPH_ADMIN_PASSWORD"),
		SecretKey:        secret,
		SyncInterval:     30 * time.Second,
		FullSyncInterval: 30 * time.Minute,
		StaleAfter:       5 * time.Minute,
		IYUUSitesURL:     envOrDefault("SEEDGRAPH_IYUU_SITES_URL", "https://2025.iyuu.cn/reseed/sites/index"),
		IYUUSyncInterval: 24 * time.Hour,
	}

	if cfg.AdminPassword == "" {
		return Config{}, errors.New("SEEDGRAPH_ADMIN_PASSWORD is required")
	}
	if len(cfg.AdminPassword) < 8 {
		return Config{}, errors.New("SEEDGRAPH_ADMIN_PASSWORD must contain at least 8 characters")
	}

	if cfg.CookieSecure, err = parseBool("SEEDGRAPH_COOKIE_SECURE", false); err != nil {
		return Config{}, err
	}
	if cfg.SyncInterval, err = parseDuration("SEEDGRAPH_SYNC_INTERVAL", cfg.SyncInterval); err != nil {
		return Config{}, err
	}
	if cfg.FullSyncInterval, err = parseDuration("SEEDGRAPH_FULL_SYNC_INTERVAL", cfg.FullSyncInterval); err != nil {
		return Config{}, err
	}
	if cfg.StaleAfter, err = parseDuration("SEEDGRAPH_STALE_AFTER", cfg.StaleAfter); err != nil {
		return Config{}, err
	}
	if cfg.IYUUSyncEnabled, err = parseBool("SEEDGRAPH_IYUU_SYNC_ENABLED", true); err != nil {
		return Config{}, err
	}
	if cfg.IYUUSyncInterval, err = parseDuration("SEEDGRAPH_IYUU_SYNC_INTERVAL", cfg.IYUUSyncInterval); err != nil {
		return Config{}, err
	}
	if cfg.FullSyncInterval < cfg.SyncInterval {
		return Config{}, errors.New("SEEDGRAPH_FULL_SYNC_INTERVAL must not be shorter than SEEDGRAPH_SYNC_INTERVAL")
	}

	return cfg, nil
}

func parseSecret(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("SEEDGRAPH_SECRET_KEY is required")
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) >= minimumSecretBytes {
		return decoded, nil
	}
	if len([]byte(value)) < minimumSecretBytes {
		return nil, fmt.Errorf("SEEDGRAPH_SECRET_KEY must contain at least %d bytes (raw or base64 encoded)", minimumSecretBytes)
	}
	return []byte(value), nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func parseBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

func parseDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return parsed, nil
}
