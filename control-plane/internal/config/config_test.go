package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(*testing.T, Config)
	}{
		{
			name: "defaults applied when only database url is set",
			env:  map[string]string{"LAUNCHPAD_DATABASE_URL": "postgres://localhost/launchpad"},
			check: func(t *testing.T, c Config) {
				if c.Env != "development" {
					t.Errorf("Env = %q, want development", c.Env)
				}
				if c.HTTPAddr != ":8080" {
					t.Errorf("HTTPAddr = %q, want :8080", c.HTTPAddr)
				}
				if c.LogLevel != slog.LevelInfo {
					t.Errorf("LogLevel = %v, want info", c.LogLevel)
				}
				if c.ShutdownTimeout != 15*time.Second {
					t.Errorf("ShutdownTimeout = %v, want 15s", c.ShutdownTimeout)
				}
			},
		},
		{
			name:    "database url is required",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "invalid log level is rejected",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL": "postgres://localhost/launchpad",
				"LAUNCHPAD_LOG_LEVEL":    "chatty",
			},
			wantErr: true,
		},
		{
			name: "non-positive shutdown timeout is rejected",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL":     "postgres://localhost/launchpad",
				"LAUNCHPAD_SHUTDOWN_TIMEOUT": "0s",
			},
			wantErr: true,
		},
		{
			name: "docker is the default deploy driver",
			env:  map[string]string{"LAUNCHPAD_DATABASE_URL": "postgres://localhost/launchpad"},
			check: func(t *testing.T, c Config) {
				if c.DeployDriver != DriverDocker {
					t.Errorf("DeployDriver = %q, want %q", c.DeployDriver, DriverDocker)
				}
			},
		},
		{
			name: "an unknown deploy driver is rejected",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL":  "postgres://localhost/launchpad",
				"LAUNCHPAD_DEPLOY_DRIVER": "kubernetes",
			},
			wantErr: true,
		},
		{
			// A driver that cannot name what it deploys into should stop the
			// process starting, not fail one deployment per queued job.
			name: "cloudrun without a project or region is rejected",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL":  "postgres://localhost/launchpad",
				"LAUNCHPAD_DEPLOY_DRIVER": DriverCloudRun,
			},
			wantErr: true,
		},
		{
			name: "cloudrun with a project and region is configured",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL":  "postgres://localhost/launchpad",
				"LAUNCHPAD_DEPLOY_DRIVER": DriverCloudRun,
				"LAUNCHPAD_GCP_PROJECT":   "launchpad-prod",
				"LAUNCHPAD_GCP_REGION":    "europe-west1",
			},
			check: func(t *testing.T, c Config) {
				if c.CloudRun.Project != "launchpad-prod" {
					t.Errorf("CloudRun.Project = %q, want launchpad-prod", c.CloudRun.Project)
				}
				if c.CloudRun.Repository != "launchpad" {
					t.Errorf("CloudRun.Repository = %q, want the default launchpad", c.CloudRun.Repository)
				}
				// Deployed workloads are reached through the proxy, which sends
				// no identity token, so they have to be publicly reachable
				// unless something that does sign requests is in front.
				if !c.CloudRun.AllowUnauthenticated {
					t.Error("CloudRun.AllowUnauthenticated = false, want true by default")
				}
			},
		},
		{
			name: "cloud run public access can be turned off",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL":                    "postgres://localhost/launchpad",
				"LAUNCHPAD_DEPLOY_DRIVER":                   DriverCloudRun,
				"LAUNCHPAD_GCP_PROJECT":                     "launchpad-prod",
				"LAUNCHPAD_GCP_REGION":                      "europe-west1",
				"LAUNCHPAD_CLOUD_RUN_ALLOW_UNAUTHENTICATED": "false",
			},
			check: func(t *testing.T, c Config) {
				if c.CloudRun.AllowUnauthenticated {
					t.Error("CloudRun.AllowUnauthenticated = true, want false")
				}
			},
		},
		{
			name: "a non-boolean public access setting is rejected",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL":                    "postgres://localhost/launchpad",
				"LAUNCHPAD_CLOUD_RUN_ALLOW_UNAUTHENTICATED": "sometimes",
			},
			wantErr: true,
		},
		{
			name: "overrides are honoured",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL":     "postgres://localhost/launchpad",
				"LAUNCHPAD_ENV":              "production",
				"LAUNCHPAD_HTTP_ADDR":        ":9000",
				"LAUNCHPAD_LOG_LEVEL":        "debug",
				"LAUNCHPAD_SHUTDOWN_TIMEOUT": "30s",
				"LAUNCHPAD_PROXY_ADDR":       ":9081",
				"LAUNCHPAD_BASE_DOMAIN":      "launchpad.example",
				"LAUNCHPAD_DEPLOY_WORKERS":   "4",
			},
			check: func(t *testing.T, c Config) {
				if !c.IsProduction() {
					t.Error("IsProduction() = false, want true")
				}
				if c.HTTPAddr != ":9000" {
					t.Errorf("HTTPAddr = %q, want :9000", c.HTTPAddr)
				}
				if c.LogLevel != slog.LevelDebug {
					t.Errorf("LogLevel = %v, want debug", c.LogLevel)
				}
				if c.ShutdownTimeout != 30*time.Second {
					t.Errorf("ShutdownTimeout = %v, want 30s", c.ShutdownTimeout)
				}
				if c.DeployWorkers != 4 {
					t.Errorf("DeployWorkers = %d, want 4", c.DeployWorkers)
				}
				if c.BaseDomain != "launchpad.example" {
					t.Errorf("BaseDomain = %q, want launchpad.example", c.BaseDomain)
				}
				// The environment's public URL has to name the proxy's port.
				if c.ProxyPort() != 9081 {
					t.Errorf("ProxyPort() = %d, want 9081", c.ProxyPort())
				}
			},
		},
	}

	// Every LAUNCHPAD_* var the loader reads. Cleared before each case so a
	// developer's exported shell environment cannot change the result.
	known := []string{
		"LAUNCHPAD_ENV",
		"LAUNCHPAD_HTTP_ADDR",
		"LAUNCHPAD_DATABASE_URL",
		"LAUNCHPAD_LOG_LEVEL",
		"LAUNCHPAD_SHUTDOWN_TIMEOUT",
		"LAUNCHPAD_DEPLOY_WORKERS",
		"LAUNCHPAD_PROXY_ADDR",
		"LAUNCHPAD_BASE_DOMAIN",
		"LAUNCHPAD_DEPLOY_DRIVER",
		"LAUNCHPAD_GCP_PROJECT",
		"LAUNCHPAD_GCP_REGION",
		"LAUNCHPAD_ARTIFACT_REPOSITORY",
		"LAUNCHPAD_CLOUD_RUN_SERVICE_ACCOUNT",
		"LAUNCHPAD_CLOUD_RUN_ALLOW_UNAUTHENTICATED",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range known {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Load() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}
