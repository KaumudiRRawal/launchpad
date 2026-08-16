// Package deploy turns a commit in a repository into a running service.
//
// The pipeline is fetch, detect, build, release. Each step is behind an
// interface, so the orchestration can be tested without git, Docker, or a
// network, and so a Cloud Run backend can replace the Docker one without the
// orchestration changing.
package deploy

import (
	"fmt"
	"io/fs"
	"path"
)

// Strategy is how a source tree should be turned into a runnable image.
type Strategy string

const (
	// StrategyDockerfile builds the Dockerfile the repository provides.
	StrategyDockerfile Strategy = "dockerfile"
	// StrategyGo builds a Go module with the standard toolchain.
	StrategyGo Strategy = "go"
	// StrategyNode builds a Node project from its package.json.
	StrategyNode Strategy = "node"
	// StrategyPython builds a Python project from its requirements.
	StrategyPython Strategy = "python"
)

// ErrUnknownStrategy reports a source tree Launchpad cannot build.
var ErrUnknownStrategy = fmt.Errorf("no build strategy matched")

// detectors are tried in order. A repository's own Dockerfile always wins: if
// the author took the trouble to describe their build, guessing instead would
// be both rude and wrong.
var detectors = []struct {
	strategy Strategy
	marker   string
}{
	{StrategyDockerfile, "Dockerfile"},
	{StrategyGo, "go.mod"},
	{StrategyNode, "package.json"},
	{StrategyPython, "requirements.txt"},
	{StrategyPython, "pyproject.toml"},
}

// Detect works out how to build the tree rooted at sourcePath inside fsys.
func Detect(fsys fs.FS, sourcePath string) (Strategy, error) {
	if sourcePath == "" {
		sourcePath = "."
	}

	for _, d := range detectors {
		marker := path.Join(sourcePath, d.marker)
		if _, err := fs.Stat(fsys, marker); err == nil {
			return d.strategy, nil
		}
	}
	return "", fmt.Errorf("%w in %q", ErrUnknownStrategy, sourcePath)
}

// Dockerfile returns the build instructions for a strategy. For
// StrategyDockerfile the repository supplies its own, so this returns empty
// and the caller builds the file already on disk.
//
// The generated images are multi-stage and run as a non-root user, because a
// platform that builds other people's code should not hand that code root in
// its own containers.
func (s Strategy) Dockerfile(port int) string {
	switch s {
	case StrategyDockerfile:
		return ""

	case StrategyGo:
		return fmt.Sprintf(`FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.su[m] ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app ./...

FROM alpine:3.22
RUN adduser -D -u 10001 app
COPY --from=build /out/app /app
USER app
EXPOSE %d
ENTRYPOINT ["/app"]
`, port)

	case StrategyNode:
		return fmt.Sprintf(`FROM node:24-alpine AS build
WORKDIR /src
COPY package*.json ./
RUN npm ci --omit=dev || npm install --omit=dev
COPY . .

FROM node:24-alpine
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /src ./
USER app
EXPOSE %d
CMD ["npm", "start"]
`, port)

	case StrategyPython:
		return fmt.Sprintf(`FROM python:3.13-slim
RUN useradd --uid 10001 --create-home app
WORKDIR /app
COPY requirements.tx[t] pyproject.tom[l] ./
RUN pip install --no-cache-dir -r requirements.txt 2>/dev/null || pip install --no-cache-dir .
COPY . .
USER app
EXPOSE %d
CMD ["python", "main.py"]
`, port)
	}
	return ""
}
