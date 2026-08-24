package app

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mowfteedev/mowf-net/internal/platform/database"
)

// Config holds runtime configuration for the application.
type Config struct {
	HTTPAddr string
	Database database.Config
}

// LoadConfig reads configuration from environment variables with dev-friendly defaults.
func LoadConfig() (Config, error) {
	httpPort := getEnv("HTTP_PORT", "8080")
	httpAddr := getEnv("HTTP_ADDR", fmt.Sprintf(":%s", httpPort))

	dbPortStr := getEnv("DB_PORT", "5432")
	dbPort, err := strconv.Atoi(dbPortStr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid DB_PORT %q: %w", dbPortStr, err)
	}

	maxOpenConnsStr := getEnv("DB_MAX_OPEN_CONNS", "25")
	maxOpenConns, err := strconv.Atoi(maxOpenConnsStr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid DB_MAX_OPEN_CONNS %q: %w", maxOpenConnsStr, err)
	}

	maxIdleConnsStr := getEnv("DB_MAX_IDLE_CONNS", "25")
	maxIdleConns, err := strconv.Atoi(maxIdleConnsStr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid DB_MAX_IDLE_CONNS %q: %w", maxIdleConnsStr, err)
	}

	return Config{
		HTTPAddr: httpAddr,
		Database: database.Config{
			Host:            getEnv("DB_HOST", "127.0.0.1"),
			Port:            dbPort,
			User:            getEnv("DB_USER", "postgres"),
			Password:        getEnv("DB_PASSWORD", "postgres"),
			Database:        getEnv("DB_NAME", "mowf_net"),
			SSLMode:         getEnv("DB_SSLMODE", "disable"),
			MaxOpenConns:    maxOpenConns,
			MaxIdleConns:    maxIdleConns,
			ConnMaxLifetime: 15 * time.Minute,
			ConnMaxIdleTime: 5 * time.Minute,
		},
	}, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
