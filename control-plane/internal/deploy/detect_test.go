package deploy

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name       string
		files      []string
		sourcePath string
		want       Strategy
		wantErr    bool
	}{
		{name: "dockerfile", files: []string{"Dockerfile"}, want: StrategyDockerfile},
		{name: "go module", files: []string{"go.mod", "main.go"}, want: StrategyGo},
		{name: "node project", files: []string{"package.json"}, want: StrategyNode},
		{name: "python requirements", files: []string{"requirements.txt"}, want: StrategyPython},
		{name: "python pyproject", files: []string{"pyproject.toml"}, want: StrategyPython},
		{
			// A repository that describes its own build has said how it wants
			// to be built; guessing instead would be wrong.
			name:  "dockerfile wins over a language marker",
			files: []string{"Dockerfile", "go.mod", "package.json"},
			want:  StrategyDockerfile,
		},
		{
			name:       "monorepo subdirectory",
			files:      []string{"README.md", "services/api/go.mod"},
			sourcePath: "services/api",
			want:       StrategyGo,
		},
		{
			// The marker must be found at the source path, not anywhere.
			name:       "marker outside the source path is ignored",
			files:      []string{"go.mod", "services/api/README.md"},
			sourcePath: "services/api",
			wantErr:    true,
		},
		{name: "nothing recognisable", files: []string{"README.md"}, wantErr: true},
		{name: "empty tree", files: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for _, f := range tt.files {
				fsys[f] = &fstest.MapFile{Data: []byte("x")}
			}

			got, err := Detect(fsys, tt.sourcePath)
			if tt.wantErr {
				if !errors.Is(err, ErrUnknownStrategy) {
					t.Fatalf("Detect() error = %v, want ErrUnknownStrategy", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detect() error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("Detect() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStrategyDockerfile(t *testing.T) {
	t.Run("repository dockerfile is used as-is", func(t *testing.T) {
		if got := StrategyDockerfile.Dockerfile(8080); got != "" {
			t.Errorf("Dockerfile() = %q, want empty so the repo's own file is used", got)
		}
	})

	t.Run("generated dockerfiles embed the port and drop root", func(t *testing.T) {
		// A platform that builds other people's code should not hand that
		// code root inside its own containers.
		for _, s := range []Strategy{StrategyGo, StrategyNode, StrategyPython} {
			generated := s.Dockerfile(3000)
			if generated == "" {
				t.Errorf("%s: Dockerfile() is empty", s)
				continue
			}
			if !strings.Contains(generated, "EXPOSE 3000") {
				t.Errorf("%s: Dockerfile() does not expose the requested port:\n%s", s, generated)
			}
			if !strings.Contains(generated, "USER app") {
				t.Errorf("%s: Dockerfile() does not drop to a non-root user:\n%s", s, generated)
			}
		}
	})
}
