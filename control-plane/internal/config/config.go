// Package config loads control-plane configuration from the environment.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config holds every setting the control plane needs to start.
type Config struct {
	Env             string
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

// Load reads configuration from LAUNCHPAD_* environment variables, applying
// defaults suited to local development. It returns an error listing every
// invalid or missing setting at once, so a misconfigured deploy surfaces all
// its problems on the first boot attempt rather than one per restart.
func Load() (Config, error) {
	cfg := Config{
		Env:             envOr("LAUNCHPAD_ENV", "development"),
		HTTPAddr:        envOr("LAUNCHPAD_HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("LAUNCHPAD_DATABASE_URL"),
		ShutdownTimeout: 15 * time.Second,
	}

	var problems []string

	if cfg.DatabaseURL == "" {
		problems = append(problems, "LAUNCHPAD_DATABASE_URL is required")
	}

	level, err := parseLevel(envOr("LAUNCHPAD_LOG_LEVEL", "info"))
	if err != nil {
		problems = append(problems, err.Error())
	}
	cfg.LogLevel = level

	if d := os.Getenv("LAUNCHPAD_SHUTDOWN_TIMEOUT"); d != "" {
		parsed, err := time.ParseDuration(d)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("LAUNCHPAD_SHUTDOWN_TIMEOUT %q is not a duration", d))
		case parsed <= 0:
			problems = append(problems, "LAUNCHPAD_SHUTDOWN_TIMEOUT must be positive")
		default:
			cfg.ShutdownTimeout = parsed
		}
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

// IsProduction reports whether the control plane is running in production,
// which turns on JSON logging and turns off verbose error responses.
func (c Config) IsProduction() bool { return c.Env == "production" }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("LAUNCHPAD_LOG_LEVEL %q is not one of debug, info, warn, error", s)
	}
}
