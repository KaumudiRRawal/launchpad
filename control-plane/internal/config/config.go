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
	// DeployDriver names the backend deployments run on.
	DeployDriver string
	// CloudRun is read only when DeployDriver selects it.
	CloudRun CloudRunConfig
}

// Deploy driver names. The default is Docker because it is the one that needs
// no account, no billing and no credentials to try.
const (
	DriverDocker   = "docker"
	DriverCloudRun = "cloudrun"
)

// CloudRunConfig holds what the Cloud Run driver needs. The fields are grouped
// because they are only meaningful together: a project without a region names
// nothing deployable.
type CloudRunConfig struct {
	Project    string
	Region     string
	Repository string
	// ServiceAccount is the identity deployed workloads run as. Left empty,
	// Google falls back to the project's default compute account, which holds
	// far more permission than somebody else's code should get.
	ServiceAccount string
	// AllowUnauthenticated makes deployed workloads publicly reachable, which
	// they have to be: the platform's proxy forwards to them without minting an
	// identity token, so a workload requiring IAM authentication would answer
	// 403 to every request arriving through the front door. Installs that front
	// Cloud Run with something that does sign requests set it false.
	AllowUnauthenticated bool
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
		DeployDriver:    envOr("LAUNCHPAD_DEPLOY_DRIVER", DriverDocker),
		CloudRun: CloudRunConfig{
			Project:              os.Getenv("LAUNCHPAD_GCP_PROJECT"),
			Region:               os.Getenv("LAUNCHPAD_GCP_REGION"),
			Repository:           envOr("LAUNCHPAD_ARTIFACT_REPOSITORY", "launchpad"),
			ServiceAccount:       os.Getenv("LAUNCHPAD_CLOUD_RUN_SERVICE_ACCOUNT"),
			AllowUnauthenticated: true,
		},
	}

	var problems []string

	switch cfg.DeployDriver {
	case DriverDocker:
	case DriverCloudRun:
		// Checked at boot rather than when the first deployment is claimed: a
		// misconfigured driver should stop the process starting, not fail one
		// deployment per queued job with the same message.
		if cfg.CloudRun.Project == "" {
			problems = append(problems, "LAUNCHPAD_GCP_PROJECT is required when the deploy driver is cloudrun")
		}
		if cfg.CloudRun.Region == "" {
			problems = append(problems, "LAUNCHPAD_GCP_REGION is required when the deploy driver is cloudrun")
		}
	default:
		problems = append(problems, fmt.Sprintf("LAUNCHPAD_DEPLOY_DRIVER %q is not one of %s, %s",
			cfg.DeployDriver, DriverDocker, DriverCloudRun))
	}

	if raw := os.Getenv("LAUNCHPAD_CLOUD_RUN_ALLOW_UNAUTHENTICATED"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("LAUNCHPAD_CLOUD_RUN_ALLOW_UNAUTHENTICATED %q is not a boolean", raw))
		} else {
			cfg.CloudRun.AllowUnauthenticated = parsed
		}
	}

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
