package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		name  string
		slug  string
		valid bool
	}{
		{name: "simple", slug: "launchpad", valid: true},
		{name: "with digits", slug: "app2", valid: true},
		{name: "internal hyphen", slug: "my-cool-app", valid: true},
		{name: "single character", slug: "a", valid: true},
		{name: "empty", slug: "", valid: false},
		{name: "uppercase", slug: "MyApp", valid: false},
		{name: "leading hyphen", slug: "-app", valid: false},
		{name: "trailing hyphen", slug: "app-", valid: false},
		{name: "underscore", slug: "my_app", valid: false},
		{name: "dot", slug: "my.app", valid: false},
		{name: "space", slug: "my app", valid: false},
		{name: "at the length limit", slug: strings.Repeat("a", 63), valid: true},
		{name: "one over the length limit", slug: strings.Repeat("a", 64), valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSlug("slug", tt.slug)
			if tt.valid && err != nil {
				t.Errorf("ValidateSlug(%q) = %v, want nil", tt.slug, err)
			}
			if !tt.valid && err == nil {
				t.Errorf("ValidateSlug(%q) = nil, want an error", tt.slug)
			}
		})
	}
}

func TestCreateProjectInputValidate(t *testing.T) {
	t.Run("defaults the branch to main", func(t *testing.T) {
		in := CreateProjectInput{
			Slug:    "demo",
			Name:    "Demo",
			RepoURL: "https://github.com/example/demo",
		}
		if err := in.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		if in.DefaultBranch != "main" {
			t.Errorf("DefaultBranch = %q, want main", in.DefaultBranch)
		}
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		in := CreateProjectInput{
			Slug:    "  demo  ",
			Name:    "  Demo  ",
			RepoURL: "  https://github.com/example/demo  ",
		}
		if err := in.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		if in.Slug != "demo" {
			t.Errorf("Slug = %q, want demo", in.Slug)
		}
		if in.Name != "Demo" {
			t.Errorf("Name = %q, want Demo", in.Name)
		}
	})

	t.Run("reports every problem at once", func(t *testing.T) {
		// A caller fixing a form should not have to resubmit to find the next
		// mistake, so all three failures must come back together.
		in := CreateProjectInput{Slug: "Bad Slug", Name: "", RepoURL: "not-a-url"}

		err := in.Validate()
		if err == nil {
			t.Fatal("Validate() = nil, want errors")
		}

		var problems ValidationErrors
		if !errors.As(err, &problems) {
			t.Fatalf("Validate() returned %T, want ValidationErrors", err)
		}
		if len(problems) != 3 {
			t.Errorf("got %d problems, want 3: %v", len(problems), problems)
		}
	})

	t.Run("rejects non-http clone URLs", func(t *testing.T) {
		// SSH remotes would fail later at clone time with a far less obvious
		// error, because the builder authenticates with a token.
		for _, repo := range []string{
			"git@github.com:example/demo.git",
			"ssh://git@github.com/example/demo.git",
			"file:///etc/passwd",
			"https://",
		} {
			in := CreateProjectInput{Slug: "demo", Name: "Demo", RepoURL: repo}
			if err := in.Validate(); err == nil {
				t.Errorf("Validate() with repo_url %q = nil, want an error", repo)
			}
		}
	})
}

func TestCreateServiceInputValidate(t *testing.T) {
	t.Run("applies defaults", func(t *testing.T) {
		in := CreateServiceInput{Name: "api"}
		if err := in.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		if in.SourcePath != "." {
			t.Errorf("SourcePath = %q, want .", in.SourcePath)
		}
		if in.Port != 8080 {
			t.Errorf("Port = %d, want 8080", in.Port)
		}
	})

	t.Run("refuses a source path that escapes the repository", func(t *testing.T) {
		for _, path := range []string{"../etc", "/etc/passwd", "services/../../secrets"} {
			in := CreateServiceInput{Name: "api", SourcePath: path}
			if err := in.Validate(); err == nil {
				t.Errorf("Validate() with source_path %q = nil, want an error", path)
			}
		}
	})

	t.Run("rejects out-of-range ports", func(t *testing.T) {
		for _, port := range []int{-1, 65536, 99999} {
			in := CreateServiceInput{Name: "api", Port: port}
			if err := in.Validate(); err == nil {
				t.Errorf("Validate() with port %d = nil, want an error", port)
			}
		}
	})
}

func TestCreateEnvironmentInputValidate(t *testing.T) {
	tests := []struct {
		name  string
		in    CreateEnvironmentInput
		valid bool
	}{
		{name: "production", in: CreateEnvironmentInput{Kind: EnvironmentProduction, Name: "production"}, valid: true},
		{name: "preview", in: CreateEnvironmentInput{Kind: EnvironmentPreview, Name: "pr-42"}, valid: true},
		{name: "unknown kind", in: CreateEnvironmentInput{Kind: "staging", Name: "staging"}, valid: false},
		{name: "empty kind", in: CreateEnvironmentInput{Name: "x"}, valid: false},
		{name: "name is not a DNS label", in: CreateEnvironmentInput{Kind: EnvironmentPreview, Name: "PR 42"}, valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if tt.valid && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tt.valid && err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

func TestSubdomain(t *testing.T) {
	t.Run("combines environment and project", func(t *testing.T) {
		got, err := Subdomain("demo", "pr-42")
		if err != nil {
			t.Fatalf("Subdomain() = %v, want nil", err)
		}
		if got != "pr-42-demo" {
			t.Errorf("Subdomain() = %q, want pr-42-demo", got)
		}
	})

	t.Run("rejects a combination that exceeds the hostname limit", func(t *testing.T) {
		// Both parts are individually legal DNS labels, but concatenating
		// them is not — which is the whole reason this check exists.
		long := strings.Repeat("a", 40)
		if _, err := Subdomain(long, long); err == nil {
			t.Error("Subdomain() = nil, want a length error")
		}
	})
}

func TestCreateDeploymentInputValidate(t *testing.T) {
	const validSHA = "0123456789abcdef0123456789abcdef01234567"

	t.Run("accepts a full SHA and lowercases it", func(t *testing.T) {
		in := CreateDeploymentInput{
			ServiceID:     "svc",
			EnvironmentID: "env",
			CommitSHA:     strings.ToUpper(validSHA),
		}
		if err := in.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		if in.CommitSHA != validSHA {
			t.Errorf("CommitSHA = %q, want it lowercased", in.CommitSHA)
		}
	})

	t.Run("rejects short and malformed SHAs", func(t *testing.T) {
		// A deployment names one commit forever, and a short SHA can become
		// ambiguous as the repository grows.
		for _, sha := range []string{"abc1234", "", "zzzz56789abcdef0123456789abcdef01234567", validSHA + "0"} {
			in := CreateDeploymentInput{ServiceID: "svc", EnvironmentID: "env", CommitSHA: sha}
			if err := in.Validate(); err == nil {
				t.Errorf("Validate() with commit_sha %q = nil, want an error", sha)
			}
		}
	})
}

func TestCreateAPIKeyInputValidate(t *testing.T) {
	tests := []struct {
		name  string
		in    CreateAPIKeyInput
		valid bool
	}{
		{name: "named", in: CreateAPIKeyInput{Name: "ci"}, valid: true},
		{name: "at the length limit", in: CreateAPIKeyInput{Name: strings.Repeat("k", 200)}, valid: true},
		{name: "missing", in: CreateAPIKeyInput{}, valid: false},
		{name: "only whitespace", in: CreateAPIKeyInput{Name: "   "}, valid: false},
		{name: "past the length limit", in: CreateAPIKeyInput{Name: strings.Repeat("k", 201)}, valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if tt.valid {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}

			// The 422 body names the field to correct, and this input has
			// exactly one field a caller could have got wrong.
			var problems ValidationErrors
			if !errors.As(err, &problems) {
				t.Fatalf("Validate() returned %T, want ValidationErrors", err)
			}
			if len(problems) != 1 || problems[0].Field != "name" {
				t.Errorf("got %v, want one problem naming name", problems)
			}
		})
	}

	t.Run("trims the name before it is stored", func(t *testing.T) {
		// The handler hands the normalised input straight to CreateAPIKey, so
		// whatever Validate leaves behind is the name an operator reads later
		// when deciding which key to revoke.
		in := CreateAPIKeyInput{Name: "  ci deploy  "}
		if err := in.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
		if in.Name != "ci deploy" {
			t.Errorf("Name = %q, want %q", in.Name, "ci deploy")
		}
	})
}

func TestDeploymentStatusTransitions(t *testing.T) {
	tests := []struct {
		from, to DeploymentStatus
		allowed  bool
	}{
		{from: DeploymentQueued, to: DeploymentBuilding, allowed: true},
		{from: DeploymentBuilding, to: DeploymentDeploying, allowed: true},
		{from: DeploymentDeploying, to: DeploymentLive, allowed: true},
		{from: DeploymentLive, to: DeploymentSuperseded, allowed: true},
		{from: DeploymentQueued, to: DeploymentFailed, allowed: true},

		// Skipping the build stage would mean releasing an image that was
		// never produced.
		{from: DeploymentQueued, to: DeploymentLive, allowed: false},
		{from: DeploymentBuilding, to: DeploymentLive, allowed: false},

		// Terminal states are terminal: a late worker update must not
		// resurrect a deployment that already failed.
		{from: DeploymentFailed, to: DeploymentBuilding, allowed: false},
		{from: DeploymentFailed, to: DeploymentLive, allowed: false},
		{from: DeploymentSuperseded, to: DeploymentLive, allowed: false},
		{from: DeploymentLive, to: DeploymentBuilding, allowed: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"_to_"+string(tt.to), func(t *testing.T) {
			if got := tt.from.CanTransitionTo(tt.to); got != tt.allowed {
				t.Errorf("%s.CanTransitionTo(%s) = %v, want %v", tt.from, tt.to, got, tt.allowed)
			}
		})
	}
}

func TestDeploymentStatusTerminal(t *testing.T) {
	terminal := []DeploymentStatus{DeploymentLive, DeploymentFailed, DeploymentSuperseded}
	running := []DeploymentStatus{DeploymentQueued, DeploymentBuilding, DeploymentDeploying}

	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%s.Terminal() = false, want true", s)
		}
	}
	for _, s := range running {
		if s.Terminal() {
			t.Errorf("%s.Terminal() = true, want false", s)
		}
	}
}

// TestPublicURL pins the port rule. The only reason this function is not string
// concatenation is that a port has to disappear from the URL when it is the
// scheme's default, and every install that is not a laptop runs the proxy on
// 80 — so the branch nobody exercises locally is the one every real user sees.
func TestPublicURL(t *testing.T) {
	tests := []struct {
		name       string
		baseDomain string
		port       int
		subdomain  string
		want       string
	}{
		{
			name:       "names a non-default port",
			baseDomain: "localhost",
			port:       8081,
			subdomain:  "pr-42-demo",
			want:       "http://pr-42-demo.localhost:8081",
		},
		{
			name:       "elides the scheme default",
			baseDomain: "launchpad.dev",
			port:       80,
			subdomain:  "production-demo",
			want:       "http://production-demo.launchpad.dev",
		},
		{
			// Config.ProxyPort() returns 0 when the proxy address carries no
			// port, which means the default one rather than a port zero.
			name:       "elides an unset port",
			baseDomain: "launchpad.dev",
			port:       0,
			subdomain:  "production-demo",
			want:       "http://production-demo.launchpad.dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PublicURL(tt.baseDomain, tt.port, tt.subdomain); got != tt.want {
				t.Errorf("PublicURL(%q, %d, %q) = %q, want %q",
					tt.baseDomain, tt.port, tt.subdomain, got, tt.want)
			}
		})
	}
}
