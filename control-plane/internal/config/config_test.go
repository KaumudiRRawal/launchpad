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
			name: "overrides are honoured",
			env: map[string]string{
				"LAUNCHPAD_DATABASE_URL":     "postgres://localhost/launchpad",
				"LAUNCHPAD_ENV":              "production",
				"LAUNCHPAD_HTTP_ADDR":        ":9000",
				"LAUNCHPAD_LOG_LEVEL":        "debug",
				"LAUNCHPAD_SHUTDOWN_TIMEOUT": "30s",
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
