package app

import (
	"testing"
)

func TestLoadConfig_Defaults(t *testing.T) {
	// Explicitly isolate all config environment variables so tests do not inherit shell/CI environment
	envVars := []string{
		"HTTP_PORT",
		"HTTP_ADDR",
		"DB_HOST",
		"DB_PORT",
		"DB_USER",
		"DB_PASSWORD",
		"DB_NAME",
		"DB_SSLMODE",
		"DB_MAX_OPEN_CONNS",
		"DB_MAX_IDLE_CONNS",
	}
	for _, k := range envVars {
		t.Setenv(k, "")
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() unexpected error: %v", err)
	}

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %s, want :8080", cfg.HTTPAddr)
	}
	if cfg.Database.Host != "127.0.0.1" {
		t.Errorf("Database.Host = %s, want 127.0.0.1", cfg.Database.Host)
	}
	if cfg.Database.Port != 5432 {
		t.Errorf("Database.Port = %d, want 5432", cfg.Database.Port)
	}
	if cfg.Database.User != "postgres" {
		t.Errorf("Database.User = %s, want postgres", cfg.Database.User)
	}
	if cfg.Database.Password != "postgres" {
		t.Errorf("Database.Password = %s, want postgres", cfg.Database.Password)
	}
	if cfg.Database.Database != "mowf_net" {
		t.Errorf("Database.Database = %s, want mowf_net", cfg.Database.Database)
	}
	if cfg.Database.SSLMode != "disable" {
		t.Errorf("Database.SSLMode = %s, want disable", cfg.Database.SSLMode)
	}
	if cfg.Database.MaxOpenConns != 25 {
		t.Errorf("Database.MaxOpenConns = %d, want 25", cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns != 25 {
		t.Errorf("Database.MaxIdleConns = %d, want 25", cfg.Database.MaxIdleConns)
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
