package main

import (
	"testing"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/config"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/deploy"
)

// TestDeployDriver holds the one place a configured backend becomes the object
// that deploys somebody else's code.
//
// The Cloud Run fields are copied across by hand, and none of them is required
// for the driver to compile or to start: an install that dropped
// ServiceAccount would keep booting and keep deploying, with every workload
// running as the project's default compute account instead of the identity the
// operator named. So every field is asserted, and each is given a non-zero
// value, because a field that is never assigned is indistinguishable from one
// assigned its zero.
func TestDeployDriver(t *testing.T) {
	cloudRun := config.Config{
		DeployDriver: config.DriverCloudRun,
		CloudRun: config.CloudRunConfig{
			Project:              "launchpad-prod",
			Region:               "us-central1",
			Repository:           "images",
			ServiceAccount:       "workloads@launchpad-prod.iam.gserviceaccount.com",
			AllowUnauthenticated: true,
		},
	}

	tests := []struct {
		name  string
		cfg   config.Config
		want  string
		check func(*testing.T, deploy.Driver)
	}{
		{
			name: "docker is selected by name",
			cfg:  config.Config{DeployDriver: config.DriverDocker},
			want: "docker",
		},
		{
			// Config rejects an unknown driver before this is reached, so the
			// fallback is only ever taken by a Config nobody filled in. It
			// still has to yield a driver: returning nil would defer the
			// failure to the first deployment claimed, as a panic in a worker.
			name: "an unconfigured install falls back to docker",
			cfg:  config.Config{},
			want: "docker",
		},
		{
			name: "cloudrun is selected by name",
			cfg:  cloudRun,
			want: "cloudrun",
			check: func(t *testing.T, d deploy.Driver) {
				got, ok := d.(*deploy.CloudRunDriver)
				if !ok {
					t.Fatalf("driver is %T, want *deploy.CloudRunDriver", d)
				}
				if got.Project != cloudRun.CloudRun.Project {
					t.Errorf("Project = %q, want %q", got.Project, cloudRun.CloudRun.Project)
				}
				if got.Region != cloudRun.CloudRun.Region {
					t.Errorf("Region = %q, want %q", got.Region, cloudRun.CloudRun.Region)
				}
				if got.Repository != cloudRun.CloudRun.Repository {
					t.Errorf("Repository = %q, want %q", got.Repository, cloudRun.CloudRun.Repository)
				}
				if got.ServiceAccount != cloudRun.CloudRun.ServiceAccount {
					t.Errorf("ServiceAccount = %q, want %q", got.ServiceAccount, cloudRun.CloudRun.ServiceAccount)
				}
				if !got.AllowUnauthenticated {
					t.Error("AllowUnauthenticated = false, want true")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := deployDriver(tt.cfg)
			if driver == nil {
				t.Fatal("deployDriver returned nil")
			}
			// Name is what the engine writes into a build log, so a driver
			// reporting a backend other than the configured one would make
			// every deployment log lie about where it went.
			if got := driver.Name(); got != tt.want {
				t.Errorf("Name() = %q, want %q", got, tt.want)
			}
			if tt.check != nil {
				tt.check(t, driver)
			}
		})
	}
}
