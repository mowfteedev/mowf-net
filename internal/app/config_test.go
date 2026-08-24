package app

import (
	"testing"
)

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() unexpected error: %v", err)
	}

	if cfg.HTTPAddr != ":8080" && cfg.HTTPAddr != "8080" {
		t.Errorf("expected default HTTPAddr :8080, got %s", cfg.HTTPAddr)
	}
	if cfg.Database.Host != "127.0.0.1" {
		t.Errorf("expected default DB Host 127.0.0.1, got %s", cfg.Database.Host)
	}
	if cfg.Database.Port != 5432 {
		t.Errorf("expected default DB Port 5432, got %d", cfg.Database.Port)
	}
}

func TestLoadConfig_CustomEnv(t *testing.T) {
	t.Setenv("HTTP_PORT", "9090")
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "custom_user")
	t.Setenv("DB_PASSWORD", "custom_pass")
	t.Setenv("DB_NAME", "custom_db")
	t.Setenv("DB_SSLMODE", "require")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() unexpected error: %v", err)
	}

	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %s, want :9090", cfg.HTTPAddr)
	}
	if cfg.Database.Host != "db.internal" {
		t.Errorf("Database.Host = %s, want db.internal", cfg.Database.Host)
	}
	if cfg.Database.Port != 5433 {
		t.Errorf("Database.Port = %d, want 5433", cfg.Database.Port)
	}
	if cfg.Database.User != "custom_user" {
		t.Errorf("Database.User = %s, want custom_user", cfg.Database.User)
	}
	if cfg.Database.Password != "custom_pass" {
		t.Errorf("Database.Password = %s, want custom_pass", cfg.Database.Password)
	}
	if cfg.Database.Database != "custom_db" {
		t.Errorf("Database.Database = %s, want custom_db", cfg.Database.Database)
	}
	if cfg.Database.SSLMode != "require" {
		t.Errorf("Database.SSLMode = %s, want require", cfg.Database.SSLMode)
	}
}
