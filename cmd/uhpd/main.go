// Command uhpd is the composition root: it wires concrete adapters,
// stores, and the service layer together and starts the UHP-conformant HTTP
// server. This is the only file allowed to import every package at once —
// everywhere else depends on interfaces, not on this file.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aenawi/uhp-go/internal/config"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
	transporthttp "github.com/aenawi/uhp-go/internal/transport/http"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := config.Load()

	registry := harness.NewRegistry()
	for _, h := range []*harness.CLIHarness{
		harness.NewClaude(cfg.ClaudeModels),
		harness.NewCodex(cfg.CodexModels),
		harness.NewGrok(cfg.GrokModels),
		harness.NewOpenCode(cfg.OpenCodeModels),
		harness.NewPi(cfg.PiModels),
	} {
		registry.Register(h)
	}

	memStore := store.NewMemoryStore()
	opts := []service.Option{service.WithUploads(store.NewMemoryUploads())}
	if cfg.Workspace != "" {
		opts = append(opts, service.WithWorkspace(cfg.Workspace))
	}
	if cfg.HarnessStore != "" {
		// A store that will not open is fatal rather than a silent downgrade
		// to "harness management off": the operator asked for it, and a server
		// that quietly serves less than it was configured for is the hardest
		// kind of misconfiguration to notice.
		harnesses, err := store.NewFileHarnesses(cfg.HarnessStore)
		if err != nil {
			log.Error("harness store", "error", err, "path", cfg.HarnessStore)
			os.Exit(1)
		}
		opts = append(opts, service.WithHarnessStore(harnesses))
	}
	if cfg.PublicBaseURL != "" {
		opts = append(opts, service.WithPublicBaseURL(cfg.PublicBaseURL))
	}
	taskService := service.NewTaskService(registry, memStore, log, opts...)

	server := transporthttp.NewServer(taskService, log, cfg.APIKeys, cfg.MaxBodyBytes)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The listener reports its failure on a channel rather than calling
	// os.Exit from inside the goroutine, which would skip every deferred
	// cleanup in main — including the signal handler and the shutdown timeout.
	serveErr := make(chan error, 1)
	go func() {
		log.Info("uhpd listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			log.Error("server error", "error", err)
			stop()
			os.Exit(1)
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Error("shutdown", "error", err)
		}
	}
}
