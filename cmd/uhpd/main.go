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

// openHarnessStore picks where created harnesses live.
//
// Harness management is always offered, so there is always a store. With a path
// configured the harnesses are durable; without one they are not, and that is
// worth a line in the log rather than a surprise on the next restart — a client
// that created a harness and stored its id will get a 404 for it, and nothing
// in the discovery document can tell it that in advance.
//
// A configured path that will not open is fatal rather than a silent downgrade
// to memory: the operator asked for durability, and a server that quietly
// serves less than it was configured for is the hardest misconfiguration to
// notice.
func openHarnessStore(cfg config.Config, log *slog.Logger) service.HarnessStore {
	if cfg.HarnessStore == "" {
		log.Warn("harness store not configured; created harnesses will not survive a restart",
			"hint", "set UHP_HARNESS_STORE or UHP_WORKSPACE")
		return store.NewMemoryHarnesses()
	}
	harnesses, err := store.NewFileHarnesses(cfg.HarnessStore)
	if err != nil {
		log.Error("harness store", "error", err, "path", cfg.HarnessStore)
		os.Exit(1)
	}
	return harnesses
}

// openTaskStore picks where tasks and sessions live, and returns the function
// that closes it.
//
// Same rule as openHarnessStore above, for the reasons given there: a path
// configured means durable, no path means a warning rather than a surprise on
// the next restart, and a configured path that will not open is fatal rather
// than a quiet downgrade. What is lost without one is described on
// store.SQLiteStore.
func openTaskStore(cfg config.Config, log *slog.Logger) (service.Store, func()) {
	if cfg.Database == "" {
		log.Warn("database not configured; tasks and sessions will not survive a restart",
			"hint", "set UHP_DB or UHP_WORKSPACE")
		return store.NewMemoryStore(), func() {}
	}
	db, err := store.NewSQLiteStore(cfg.Database)
	if err != nil {
		log.Error("database", "error", err, "path", cfg.Database)
		os.Exit(1)
	}
	log.Info("database open", "path", cfg.Database)
	return db, func() {
		if err := db.Close(); err != nil {
			log.Error("close database", "error", err)
		}
	}
}

// requireDefaultHarness refuses to start when UHP_DEFAULT_HARNESS names a
// harness this server does not have.
//
// Fatal rather than deferred to the first task, because the failure it prevents
// is silent in the worst way: a typo'd default is only discovered by a client
// that omitted `harness_id`, which is the request Tasks §1.2 most encourages,
// and the answer it gets names a field it deliberately left out.
//
// Nothing is checked when the variable is unset. The default is then inferred
// per request from whichever harnesses are ready, and readiness at boot is not
// readiness later — a CLI can be logged in after the server starts.
func requireDefaultHarness(cfg config.Config, tasks *service.TaskService, log *slog.Logger) {
	if cfg.DefaultHarness == "" {
		return
	}
	h, ok, err := tasks.GetHarness(context.Background(), cfg.DefaultHarness)
	if err != nil {
		log.Error("resolve default harness", "error", err, "harness", cfg.DefaultHarness)
		os.Exit(1)
	}
	if !ok {
		log.Error("UHP_DEFAULT_HARNESS names no harness on this server",
			"harness", cfg.DefaultHarness)
		os.Exit(1)
	}
	// Not fatal. A harness that is configured but not logged in is a normal
	// state that fixes itself without restarting the server, so this is the
	// one half of the check that belongs in the log rather than in the exit
	// code.
	if h.Status != "ready" {
		log.Warn("default harness is not ready", "harness", h.ID, "status", h.Status)
	}
}

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

	taskStore, closeTaskStore := openTaskStore(cfg, log)
	defer closeTaskStore()

	opts := []service.Option{
		service.WithUploads(store.NewMemoryUploads()),
		service.WithMaxConcurrentRuns(cfg.MaxConcurrentRuns),
	}
	if cfg.Workspace != "" {
		opts = append(opts, service.WithWorkspace(cfg.Workspace))
	}
	opts = append(opts, service.WithHarnessStore(openHarnessStore(cfg, log)))
	if cfg.PublicBaseURL != "" {
		opts = append(opts, service.WithPublicBaseURL(cfg.PublicBaseURL))
	}
	if cfg.DefaultHarness != "" {
		opts = append(opts, service.WithDefaultHarness(cfg.DefaultHarness))
	}
	if cfg.SessionSharing {
		// Logged at Info and not silently, because this is the line that makes
		// the server answer requests carrying no credential. An operator
		// reading the startup log should be able to see that it is on without
		// asking the discovery document.
		log.Info("session sharing enabled; /v1/shares/{id} is served without authentication",
			"revoke", "DELETE /v1/sessions/{id}/share")
		if cfg.PublicBaseURL == "" {
			// A share is a link somebody sends to somebody else, and without an
			// origin this server emits a relative one. That is still a working
			// share — the id is what matters — but the field a client copies
			// out of the response is not something it can paste anywhere.
			log.Warn("session sharing is on with no public URL; share links will be relative",
				"hint", "set UHP_PUBLIC_URL")
		}
		opts = append(opts, service.WithSessionSharing())
	}
	taskService := service.NewTaskService(registry, taskStore, log, opts...)
	requireDefaultHarness(cfg, taskService, log)

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
			// os.Exit skips every deferred call, and the store is the one
			// that owns a file handle worth giving back.
			closeTaskStore()
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
