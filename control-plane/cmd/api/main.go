// Command api runs the Launchpad control plane.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/config"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/deploy"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/httpx"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/metrics"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/proxy"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/store"
	"github.com/KaumudiRRawal/launchpad/control-plane/migrations"
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet if config failed, so report to stderr.
		fmt.Fprintf(os.Stderr, "launchpad: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)

	// Cancels on SIGINT/SIGTERM, which starts graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("starting launchpad control plane",
		slog.String("env", cfg.Env),
		slog.String("addr", cfg.HTTPAddr),
		slog.String("version", buildVersion()))

	pool, err := store.Connect(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool, migrations.FS, log); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	repo := store.NewRepository(pool)

	api := &httpx.API{
		DB:         pool,
		Store:      repo,
		Log:        log,
		Version:    buildVersion(),
		BaseDomain: cfg.BaseDomain,
		ProxyPort:  cfg.ProxyPort(),
	}

	// Every request to a deployed workload passes through the proxy, so that is
	// where latency and reliability are measured. Nothing has to be installed
	// in the deployed application, and a workload that has stopped answering is
	// measured by the same code that measured it while it was healthy.
	collector := &metrics.Collector{
		Sink: repo,
		Log:  log.With(slog.String("component", "metrics")),
	}

	router := &proxy.Proxy{
		Resolver:   repo,
		Recorder:   collector,
		Log:        log.With(slog.String("component", "proxy")),
		BaseDomain: cfg.BaseDomain,
	}

	proxySrv := &http.Server{
		Addr:              cfg.ProxyAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	// Drained with the deploy workers below, because its last act is to flush
	// the minute in progress: exiting first would leave a hole in the
	// measurements immediately before every restart.
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		collector.Run(ctx)
	}()

	// The deploy workers run in-process. Splitting them into a separate
	// service would buy independent scaling that a platform this size does not
	// need yet, at the cost of a second deployable to operate. The claim query
	// takes a row lock, so running several workers here is already safe.
	for i := range cfg.DeployWorkers {
		engine := &deploy.Engine{
			Store:       repo,
			Driver:      &deploy.DockerDriver{},
			Fetcher:     &deploy.GitFetcher{},
			Log:         log.With(slog.Int("worker", i)),
			Invalidator: router,
			BaseDomain:  cfg.BaseDomain,
			ProxyPort:   cfg.ProxyPort(),
		}

		workers.Add(1)
		go func() {
			defer workers.Done()
			engine.Run(ctx)
		}()
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Deliberately generous: streaming build logs hold connections open
		// for the length of a deployment.
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  2 * time.Minute,
		BaseContext:  func(net.Listener) context.Context { return ctx },
	}

	serveErr := make(chan error, 2)
	go func() {
		log.Info("environment proxy listening",
			slog.String("addr", cfg.ProxyAddr),
			slog.String("base_domain", cfg.BaseDomain))
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("proxy server: %w", err)
		}
	}()

	go func() {
		log.Info("http server listening", slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received, draining connections",
			slog.Duration("timeout", cfg.ShutdownTimeout))
	}

	// ctx is already cancelled here, so the shutdown deadline needs a fresh one.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	if err := proxySrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("proxy shutdown failed: %w", err)
	}

	// The workers observe the same cancelled context; waiting lets an
	// in-flight deployment finish recording its outcome rather than being
	// abandoned mid-build with its status stuck at building.
	log.Info("waiting for background workers to drain")
	workers.Wait()

	log.Info("shutdown complete")
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}

	// JSON in production for log aggregation; human-readable text locally.
	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// buildVersion reports the VCS revision stamped in by the Go toolchain, so a
// running instance can be traced back to a commit without a build flag.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified == "true" {
		return revision + "-dirty"
	}
	return revision
}
