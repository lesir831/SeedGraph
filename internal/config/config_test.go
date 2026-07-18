package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("SEEDGRAPH_SECRET_KEY", strings.Repeat("s", 32))
	t.Setenv("SEEDGRAPH_ADMIN_PASSWORD", "correct-horse")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddress != ":8080" || cfg.SyncInterval != 30*time.Second ||
		!cfg.IYUUSyncEnabled || cfg.IYUUSyncInterval != 24*time.Hour ||
		cfg.IYUUSitesURL != "https://2025.iyuu.cn/reseed/sites/index" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadRejectsWeakSecretsAndPasswords(t *testing.T) {
	t.Setenv("SEEDGRAPH_SECRET_KEY", "short")
	t.Setenv("SEEDGRAPH_ADMIN_PASSWORD", "correct-horse")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a weak secret")
	}

	t.Setenv("SEEDGRAPH_SECRET_KEY", strings.Repeat("s", 32))
	t.Setenv("SEEDGRAPH_ADMIN_PASSWORD", "short")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a weak password")
	}
}

func TestLoadRejectsFullSyncShorterThanIncremental(t *testing.T) {
	t.Setenv("SEEDGRAPH_SECRET_KEY", strings.Repeat("s", 32))
	t.Setenv("SEEDGRAPH_ADMIN_PASSWORD", "correct-horse")
	t.Setenv("SEEDGRAPH_SYNC_INTERVAL", "2m")
	t.Setenv("SEEDGRAPH_FULL_SYNC_INTERVAL", "1m")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid interval relationship")
	}
}
