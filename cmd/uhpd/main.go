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
	"github.com/aenawi/uhp-go/internal/domain"
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

// announceLiveStepBudgets names every stored harness whose `maxStep` became a
// real bound on the day #72 landed.
//
// `POST /v1/harnesses` has accepted and stored `max_step` since harness
// management existed, and until now nothing read it. An operator who set 12
// meant 12, so those values are honoured retroactively rather than
// grandfathered to null — destroying a stated intent to avoid a surprise is the
// worse of the two, and a bound that appears from nowhere is only a surprise
// while nobody has said so.
//
// This is that saying, and it is a log line rather than a data migration for
// the reason the migration would be wrong: nothing about the stored values
// needs changing, only somebody needs telling. It is Info rather than Warn
// because the configuration is not a mistake, and it prints nothing at all on
// the ordinary deployment where no harness carries one.
//
// A store that cannot be listed is not fatal here. The server is about to serve
// harnesses from it and will report that failure where a client can see it;
// exiting over an announcement would turn a read hiccup into an outage.
// It needs the registry because whether a stored ceiling can be *held* is a fact
// about the base underneath, not about the number — so a value this deployment
// will silently drop has to be named as dropped rather than announced as live.
func announceLiveStepBudgets(harnesses service.HarnessStore, registry *harness.Registry, log *slog.Logger) {
	configs, err := harnesses.ListHarnesses(context.Background())
	if err != nil {
		log.Warn("could not read stored harnesses to report their step budgets", "error", err)
		return
	}
	for _, cfg := range configs {
		if cfg.MaxStep == nil {
			continue
		}
		// Announcing a value this server will not act on would be the lie the
		// whole change is about, in the one place an operator is most likely to
		// believe it. Three things fall into that: a negative, which is not a
		// bound; a ceiling the base cannot hold; and a base that is not compiled
		// into this binary at all.
		//
		// None of the three can be *written* any more — the harness handlers
		// refuse them — so every one of these is a row that predates the field
		// meaning anything, and Warn is what a row nothing else will ever
		// mention deserves.
		if reason := unenforceableStepBudget(cfg, registry); reason != "" {
			log.Warn("harness carries a step budget this server cannot enforce; it is ignored",
				"harness_id", cfg.ID, "name", cfg.Name, "base", cfg.Base,
				"max_step", *cfg.MaxStep, "reason", reason,
				"note", "PATCH max_step to null to say so explicitly, or to a value this base can hold")
			continue
		}
		// Deliberately not phrased as a migration notice. Nothing on a harness
		// records when it was written, so "stored before max_step was enforced"
		// — which this line used to say — would go on being printed for every
		// harness created afterwards, where it is simply false. What is true on
		// every run is the bound and how to remove it, and that is what an
		// operator wondering why their tasks stop early needs from this line.
		log.Info("harness bounds agent steps",
			"harness_id", cfg.ID, "name", cfg.Name, "max_step", *cfg.MaxStep,
			"note", "tasks on this harness may ask for fewer steps and not more; PATCH max_step to null to remove the bound")
	}
}

// unenforceableStepBudget says why a stored ceiling will be ignored, or "" when
// it will be honoured. It is the reporting side of service.enforceableStepBudget
// and has to agree with it; the service drops the value, and this is what says
// so out loud.
func unenforceableStepBudget(cfg domain.HarnessConfig, registry *harness.Registry) string {
	if *cfg.MaxStep < 0 {
		return "a negative number of steps is not a bound"
	}
	base, ok := registry.Get(cfg.Base)
	if !ok {
		return "base " + cfg.Base + " is not compiled into this server, so nothing can run it"
	}
	edge := harness.StepEdgeNone
	if c, ok := base.(harness.StepCounter); ok {
		edge = c.StepEdge()
	}
	switch {
	case edge == harness.StepEdgeNone:
		return "base " + cfg.Base + " narrates no countable tool call and bounds nothing itself"
	case *cfg.MaxStep == 0 && edge != harness.StepEdgeStart:
		return "base " + cfg.Base + " cannot stop a tool call before it runs, so it cannot " +
			"permit none"
	}
	return ""
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

// warnSuspendedShares says so when this server is holding shares it is not
// serving.
//
// Turning UHP_SESSION_SHARING off suspends the shares it minted rather than
// revoking them: the endpoints answer 501, the rows stay, and a restart with
// the variable set again resolves every id ever minted. That is a decision, and
// the argument for it is in internal/service/shares.go rather than repeated
// here; what is left for this file is the consequence, which is that an
// operator who meant "stop serving these" has to be told which of the two they
// got (#68).
//
// Silent when there is nothing suspended, which is nearly every deployment: a
// line printed on every start is a line nobody reads on the start that
// mattered. And a store that will not answer is a warning rather than an exit,
// because this is a courtesy about state the server does not serve — making it
// a boot dependency would let a read nothing needs stop a server that is
// otherwise correct.
func warnSuspendedShares(ctx context.Context, taskStore service.Store, log *slog.Logger) {
	shares, err := taskStore.CountShares(ctx)
	if err != nil {
		log.Warn("could not count the shares this server holds", "error", err)
		return
	}
	if shares == 0 {
		return
	}
	log.Warn("session sharing is off and this server still holds shares; they are suspended, not revoked, and every one of them resolves again if it is turned back on",
		"shares", shares,
		"hint", "to withdraw them, start with UHP_SESSION_SHARING=1 and revoke each one (uhpc unshare <session_id>)")
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

	// First, and before anything is opened or spawned. An open server is a
	// deployment mistake rather than a request-time one, so the only useful
	// place to refuse it is the one where nothing has been served yet. See
	// config.CheckAuthPosture for what it refuses and what it merely warns
	// about.
	if err := cfg.CheckAuthPosture(log); err != nil {
		log.Error("authentication", "error", err)
		os.Exit(1)
	}

	cfg.CheckStepBudget(log)

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
		// Zero when UHP_TASK_TIMEOUT is unset or unreadable, which the service
		// turns into its own default. There is no spelling of "unbounded":
		// Security §5 makes bounding task duration this server's obligation,
		// and before #54 nothing here did it.
		service.WithTaskBudget(cfg.TaskTimeout),
		// Zero when UHP_TASK_MAX_STEP is unset, and zero here means *no*
		// deployment-wide step ceiling rather than a default one — the opposite
		// of the line above, and deliberately so. See WithTaskMaxStep.
		service.WithTaskMaxStep(cfg.TaskMaxStep),
	}
	if cfg.Workspace != "" {
		opts = append(opts, service.WithWorkspace(cfg.Workspace))
	}
	harnessStore := openHarnessStore(cfg, log)
	announceLiveStepBudgets(harnessStore, registry, log)
	opts = append(opts, service.WithHarnessStore(harnessStore))
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
			"revoke", "DELETE /v1/sessions/{id}/share",
			// Said here rather than only in the README, because the operator
			// who needs it is the one reading this line before turning the
			// variable back off (#68).
			"note", "turning this off suspends the shares minted here rather than revoking them")
		if cfg.PublicBaseURL == "" {
			// A share is a link somebody sends to somebody else, and without an
			// origin this server emits a relative one. That is still a working
			// share — the id is what matters — but the field a client copies
			// out of the response is not something it can paste anywhere.
			log.Warn("session sharing is on with no public URL; share links will be relative",
				"hint", "set UHP_PUBLIC_URL")
		}
		opts = append(opts, service.WithSessionSharing())
	} else {
		// The other half of that honesty, and the one an operator is more
		// likely to need: what turning it off did and did not do.
		warnSuspendedShares(context.Background(), taskStore, log)
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
