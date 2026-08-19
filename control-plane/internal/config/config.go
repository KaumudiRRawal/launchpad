// Package config loads control-plane configuration from the environment.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
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
	// DeployWorkers is how many deployments may build concurrently. Builds are
	// CPU and I/O heavy, so this is a deliberate throttle rather than a number
	// to raise freely.
	DeployWorkers int
	// ProxyAddr is where the environment router listens. Deployed workloads are
	// reached through it, never directly.
	ProxyAddr string
	// BaseDomain is the suffix under which environment subdomains are minted.
	// "localhost" works without any DNS setup, because every *.localhost name
	// already resolves to the loopback address.
	BaseDomain string
}

// ProxyPort returns the port from ProxyAddr, which the public URL of an
// environment has to name when it is not the scheme default.
func (c Config) ProxyPort() int {
	_, port, err := net.SplitHostPort(c.ProxyAddr)
	if err != nil {
		return 0
	}
	parsed, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return parsed
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
		DeployWorkers:   2,
		ProxyAddr:       envOr("LAUNCHPAD_PROXY_ADDR", ":8081"),
		BaseDomain:      envOr("LAUNCHPAD_BASE_DOMAIN", "localhost"),
	}

	var problems []string

	if raw := os.Getenv("LAUNCHPAD_DEPLOY_WORKERS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("LAUNCHPAD_DEPLOY_WORKERS %q is not an integer", raw))
		case parsed < 0:
			problems = append(problems, "LAUNCHPAD_DEPLOY_WORKERS must not be negative")
		default:
			// Zero is legitimate: it runs the API without deploy workers, which
			// is how a read-only replica would be configured.
			cfg.DeployWorkers = parsed
		}
	}

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
