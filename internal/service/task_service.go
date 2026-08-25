// Package service contains the application core: orchestration logic that is
// independent of transport (HTTP) and infrastructure (CLI adapters, stores).
// It depends on the harness contract and on the Registry and Store interfaces
// it declares for itself in deps.go — never on a concrete adapter or storage
// engine.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// Errors the transport layer classifies into UHP status codes and error codes.
var (
	ErrHarnessNotFound  = errors.New("service: harness not found")
	ErrResponseNotFound = errors.New("service: response not found")
	ErrSessionNotFound  = errors.New("service: session not found")
	ErrSessionBusy      = errors.New("service: session busy")
	ErrHarnessMismatch  = errors.New("service: harness mismatch")

	// ErrStorage is a store that failed. It is the server's fault, not the
	// client's, and the distinction decides whether retrying is worth anything.
	ErrStorage = errors.New("service: storage failure")
)

// DefaultMaxConcurrentRuns bounds concurrent harness processes when nothing
// says otherwise.
//
// Every accepted task forks a CLI, those processes are overwhelmingly blocked
// on a model rather than on this machine's CPUs, and the number is therefore
// not derived from NumCPU. It is a deliberately conservative floor an operator
// raises for their own hardware — the value that matters is that it is finite.
const DefaultMaxConcurrentRuns = 8

// NoCapacityError is the refusal when every run slot is already held.
//
// It carries the bound because a client that is told only "busy" has nothing to
// size its own concurrency against, and would otherwise discover the limit by
// hammering the server until it stops being refused.
type NoCapacityError struct{ Limit int }

func (e *NoCapacityError) Error() string {
	return fmt.Sprintf("service: no capacity: %d harness runs already in flight", e.Limit)
}

// TaskService implements the "Tasks" and "Sessions" chapters of UHP.
type TaskService struct {
	registry  Registry
	store     Store
	runs      *supervisor
	log       *slog.Logger
	workspace WorkspaceRoot
	uploads   Uploads

	// harnesses persists the harnesses a client created over the API. Nil
	// means this deployment does not offer harness management at all; see
	// config.harnessStorePath for why that is a configuration decision.
	harnesses HarnessStore

	// publicBaseURL is the origin clients reach this server on, used to build
	// absolute artifact download URLs. Empty means relative URLs, which is
	// correct whenever the client shares the API's origin and is the only
	// honest answer when nobody has told the server its own address.
	publicBaseURL string

	// maxConcurrentRuns bounds how many harness processes may run at once.
	maxConcurrentRuns int

	// idempotency remembers which Idempotency-Key started which run (Tasks §6).
	idempotency *idempotencyKeys

	// sessionSharing is whether this deployment serves the anonymous read
	// views of Sessions §5. It is off unless an operator says otherwise: it is
	// the only thing here that answers a request carrying no credential, so it
	// is a posture the deployment adopts rather than one it inherits. See
	// shares.go.
	sessionSharing bool

	// defaultHarnessID is the harness a task that names none runs on. Empty
	// means nothing was configured, and DefaultHarness falls back to the sole
	// ready harness — see there for why "sole" is the only safe guess.
	defaultHarnessID string
}

func NewTaskService(reg Registry, store Store, log *slog.Logger, opts ...Option) *TaskService {
	if log == nil {
		log = slog.Default()
	}
	s := &TaskService{
		registry:          reg,
		store:             store,
		log:               log,
		maxConcurrentRuns: DefaultMaxConcurrentRuns,
		idempotency:       newIdempotencyKeys(),
	}
	for _, o := range opts {
		o(s)
	}
	// After the options, so the supervisor is built with the bound it will
	// enforce rather than having it changed underneath it.
	s.runs = newSupervisor(s.maxConcurrentRuns)
	return s
}

// Option configures a TaskService.
type Option func(*TaskService)

// WithWorkspace gives each session its own working directory under root.
// With no workspace configured, harnesses inherit the router's own directory,
// which is fine for local development and wrong for anything else.
func WithWorkspace(root string) Option {
	return func(s *TaskService) { s.workspace = WorkspaceRoot(root) }
}

// WithUploads enables POST /v1/files and the `file_id` form of input_file.
func WithUploads(u Uploads) Option {
	return func(s *TaskService) { s.uploads = u }
}

// WithPublicBaseURL makes artifact download URLs absolute.
func WithPublicBaseURL(base string) Option {
	return func(s *TaskService) { s.publicBaseURL = base }
}

// WithHarnessStore enables harness management: POST, PUT and DELETE on
// /v1/harnesses, and the `harness_management` capability.
func WithHarnessStore(h HarnessStore) Option {
	return func(s *TaskService) { s.harnesses = h }
}

// WithMaxConcurrentRuns bounds how many harness processes run at once.
//
// A value of zero or less falls back to DefaultMaxConcurrentRuns rather than
// meaning "unbounded" or "accept nothing": a server that runs no tasks and a
// server that forks without limit are both worse than a conservative default,
// and neither is a plausible reading of a misconfigured number.
func WithMaxConcurrentRuns(n int) Option {
	return func(s *TaskService) {
		if n <= 0 {
			n = DefaultMaxConcurrentRuns
		}
		s.maxConcurrentRuns = n
	}
}

// WithDefaultHarness names the harness a task that names none runs on.
//
// It is an id or an alias and is not validated here: whether it resolves
// depends on the registry and the harness store, and neither is necessarily
// wired up at the moment an option runs. cmd/uhpd checks it at startup instead,
// which is the right place — a configured default that names nothing is a
// deployment mistake, and every per-request answer to it comes too late.
func WithDefaultHarness(id string) Option {
	return func(s *TaskService) { s.defaultHarnessID = id }
}

// CreateTaskRequest mirrors the fields UHP requires from the OpenAI
// Responses-shaped request body, plus UHP's harness_id metadata extension.
type CreateTaskRequest struct {
	Input              string
	Model              string
	HarnessID          string
	PreviousResponseID string
	Metadata           map[string]any

	// Attachments are the files the request carried as input items, in the
	// order they appeared. They are materialized into the session's working
	// directory before the harness starts.
	Attachments []Attachment

	// InputItems is the `input` as the client sent it, normalised to an array.
	// It is stored and never read by a run: the harness is driven by Input and
	// Attachments above, and this exists so the client can be told what it
	// sent (Tasks §4, GET /v1/responses/{id}/input_items).
	InputItems []json.RawMessage

	// IdempotencyKey is the client's `Idempotency-Key` header, or empty. A
	// repeat of a key returns the first request's run instead of starting a
	// second one (Tasks §6).
	IdempotencyKey string
}

// StartTask starts a task, or — for a repeated idempotency key — returns the
// one the first request started.
//
// Tasks §6 is unusually blunt about why: "Agent tasks are expensive and
// side-effecting: a retry that runs the work twice is worse than a slow
// answer." So a repeat waits for the first request rather than being refused,
// and it is answered with the first request's own Run, which makes the replay
// identical to the original for both the streaming and the non-streaming path.
//
// The key is resolved before anything else, including the session-busy refusal
// in startTask. A retry arriving while its first attempt is still running is
// precisely the case where those two rules meet, and §6 decides it: a conflict
// is the one answer the specification names and forbids.
func (s *TaskService) StartTask(ctx context.Context, req CreateTaskRequest) (task *domain.Task, run *Run, err error) {
	if req.IdempotencyKey == "" {
		return s.startTask(ctx, req)
	}
	first, mine := s.idempotency.claim(req.IdempotencyKey)
	if !mine {
		return first.await(ctx)
	}
	// Deferred so that a panic in startTask still settles the claim. Every
	// request waiting on the key would otherwise block until its own context
	// expired, and every later retry would find a claim nothing will ever fill.
	defer func() { s.idempotency.settle(first, task, run, err) }()
	return s.startTask(ctx, req)
}

// ResumableStream reports whether an idempotency key names a stream this
// server has already started, and so whether a `Last-Event-ID` sent alongside
// it resumes anything.
//
// An empty key names nothing: a request without one always starts a fresh
// task, whose stream begins at zero and has no resume point to offer.
func (s *TaskService) ResumableStream(key string) bool {
	return key != "" && s.idempotency.known(key)
}

// startTask resolves the target harness and session, persists the initial
// task, and hands it to a supervisor goroutine that owns it from then on.
//
// It returns as soon as the run is started. It does not wait for the task, and
// the returned Run stays valid — and the task keeps running — regardless of
// what the caller does with it.
func (s *TaskService) startTask(ctx context.Context, req CreateTaskRequest) (*domain.Task, *Run, error) {
	// Tasks §1.2: a task that names no harness is not a malformed task. The
	// server chooses one and reports which — it does not refuse, which is what
	// this used to do at the transport (issue #53).
	//
	// Resolved here rather than in the handler so that every caller of
	// StartTask gets the same rule, and so the chosen id is on req.HarnessID
	// before anything below reads it: the session, the task record and the
	// metadata projection all take it from there, and a default applied later
	// would be a harness that ran the work without appearing on the response.
	if req.HarnessID == "" {
		chosen, err := s.DefaultHarness(ctx)
		if err != nil {
			return nil, nil, err
		}
		req.HarnessID = chosen
	}

	adapter, harnessCfg, ok, err := s.adapterFor(ctx, req.HarnessID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrHarnessNotFound, req.HarnessID)
	}

	sessionID, nativeSessionID, fresh, err := s.resolveSession(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	// A continuation on a harness that cannot resume is not a continuation.
	// Nothing downstream would fail: the adapter is handed an empty native
	// session id, builds argv without a resume flag, and starts an agent with
	// no memory of the previous turn — which is answered 200 and reads, to the
	// client, as the model having forgotten everything it was just told.
	//
	// Refused after resolveSession so that a request naming both a different
	// harness and an unknown response is still told the more specific of the
	// two, and before the busy and capacity checks below because this refusal
	// is permanent and those are not: "try again later" is the wrong advice for
	// a request that will be refused identically forever.
	if req.PreviousResponseID != "" {
		if err := requireCapability(adapter.Info(), uhpgo.CapSessions, whyNoSessions); err != nil {
			return nil, nil, err
		}
	}

	// Lifecycle §5: a session has one working directory and one conversation,
	// so two concurrent tasks in it is not a defined state.
	if s.runs.sessionBusy(sessionID) {
		return nil, nil, fmt.Errorf("%w: session %s already has a task in flight", ErrSessionBusy, sessionID)
	}

	// Reserved here, ahead of the working directory, the input files and the
	// fork itself, because every one of those is work this server does on an
	// anonymous caller's say-so: `UHP_API_KEYS` is unset by default, so
	// authentication is not what stands between a stranger and an unbounded
	// number of CLI processes. Nothing downstream refuses either — the
	// per-session rule above only stops a second task in the *same*
	// conversation, never an unbounded number of different ones.
	//
	// Everything above this line is a refusal specific to the request, and all
	// of it reads. That ordering is the point: a saturated server must not
	// answer "busy, retry" to a request naming a harness that does not exist or
	// a response id that never did, because retrying those never works and the
	// client has been told the opposite.
	release, haveSlot := s.runs.acquire()
	if !haveSlot {
		return nil, nil, &NoCapacityError{Limit: s.runs.capacity()}
	}
	// The slot belongs to the run once the supervisor owns it; until then it
	// belongs to this function, and every early return has to give it back.
	supervised := false
	defer func() {
		if !supervised {
			release()
		}
	}()

	// Persisted only now that the task has a slot to run in. A session written
	// for a request that was then refused is a record nothing will ever read
	// again, and a client retrying against a saturated server would leave one
	// behind on every attempt — turning the cheap answer into the expensive one.
	if fresh != nil {
		if err := s.store.CreateSession(ctx, fresh); err != nil {
			return nil, nil, fmt.Errorf("service: create session: %w", err)
		}
	}

	workDir, err := s.workspace.sessionDir(sessionID)
	if err != nil {
		return nil, nil, err
	}

	// Input files are written before the run and fingerprinted with the rest of
	// the directory, so a task's own input never comes back to it as one of its
	// artifacts.
	inputPaths, err := s.materializeAttachments(ctx, workDir, req.Attachments)
	if err != nil {
		return nil, nil, err
	}

	// The harness's own configuration — skills on disk, an MCP config file,
	// and the standing instructions carrying whatever the runtime cannot
	// enforce itself. Written before the snapshot below, so none of the
	// router's scaffolding comes back as one of the task's artifacts.
	runtime, err := s.prepareRuntime(harnessCfg, s.runtimeAdapter(adapter, harnessCfg), workDir)
	if err != nil {
		return nil, nil, err
	}
	input := req.Input + attachmentNote(inputPaths)
	if runtime.Instructions != "" {
		input = runtime.Instructions + "\n\n" + input
	}

	// Tasks §1.3 and issue #43: `model` reports what ran, and a request that
	// named none still has to be answered.
	//
	// The harness's effective default is the answer at task-creation time, and
	// it is a guess: for a plain CLI harness a task with no model is invoked
	// with no `--model` flag at all, so the CLI picks its own and this is only
	// the first entry of the list this server advertises. Adapters that can
	// read the real answer off their own output replace it mid-run — see
	// harness.UpdateModel and applyUpdate. What replaces it need not be a
	// member of that advertised list: claude's init line reports the variant
	// it resolved, suffix and all, which the list does not carry. That is the
	// honest answer rather than an inconsistency, and it is why this is
	// written as a starting value and not as the final word.
	//
	// It can still come out empty, for a harness whose list is empty — pi and
	// opencode ship with no configured models, because neither has an id that
	// is true on someone else's machine (see config.Load). Nothing is invented
	// to fill that in. It is the same call CLIHarness.validateModel already
	// makes in the other direction: nothing is advertised, so nothing is
	// promised, and a `model` this server cannot know is left unsaid rather
	// than guessed at. pi still names its own on the wire; opencode does not,
	// and a task on an opencode harness with no configured list is the one
	// case #43's symptom survives — visibly, and for a reason.
	//
	// RequestedModel stays exactly as the client spelled it, empty included:
	// the two fields exist to answer "did the model I asked for run?", and a
	// client that asked for nothing is owed that answer as "I asked for
	// nothing", not as a repeat of the default.
	//
	// Only asked when there is nothing else to say. Info() is what reads a CLI
	// harness's model list, so the very first task on a cold harness forks
	// `<cli> models` and waits up to modelQueryTimeout for it. Only the first:
	// every later refresh is backgrounded by models(), so no request after it
	// waits, and any client that has looked at /v1/harnesses has already paid
	// even that one.
	model := req.Model
	if model == "" {
		model = adapter.Info().DefaultModel
	}

	now := time.Now().UTC()
	task := &domain.Task{
		Response: uhp.Response{
			ID:                 "resp_" + uuid.NewString(),
			Object:             "response",
			Status:             uhp.StatusInProgress,
			Model:              model,
			PreviousResponseID: previousResponseID(req.PreviousResponseID),
			Metadata:           req.Metadata,
			// Tasks §1.1: `store` defaults to true.
			Store:     true,
			CreatedAt: now.Unix(),
		},
		RequestedModel: req.Model,
		HarnessID:      req.HarnessID,
		SessionID:      sessionID,
		Input:          input,
		InputItems:     req.InputItems,
		UpdatedAt:      now,
	}
	// The first of the two sync points ADR-0003 names. Everything the wire
	// `metadata` object needs is known now except the model, and the response
	// is persisted on the next line — so a task that never reaches the second
	// sync point still carries its session id, which Tasks §3 makes a MUST.
	task.SyncMetadata()
	if err := s.store.CreateTask(ctx, task); err != nil {
		return nil, nil, fmt.Errorf("service: persist task: %w", err)
	}

	// The run outlives this request. Detaching from the request context is the
	// point: the client disconnecting must not stop the agent.
	//
	// This context is deliberately NOT cancellable by us. Cancellation goes
	// through the adapter (see Run.cancel), because the runner uses this
	// context to decide whether a consumer is still there to receive the
	// terminal update.
	runCtx := context.WithoutCancel(ctx)

	// Snapshot last, so that everything the router itself put in the directory
	// is already accounted for and only the harness's own writes are artifacts.
	rs := &runState{workDir: workDir, before: snapshotDir(workDir)}

	updates, err := adapter.Run(runCtx, harness.RunRequest{
		TaskID:          task.ID,
		Input:           input,
		Model:           req.Model,
		NativeSessionID: nativeSessionID,
		Metadata:        req.Metadata,
		WorkDir:         workDir,
		SkillDirs:       runtime.SkillDirs,
		McpConfigPath:   runtime.McpConfigPath,
		DisabledTools:   runtime.DisabledTools,
	})
	if err != nil {
		task.Status = uhp.StatusFailed
		// An adapter that would not start is this server's problem, not the
		// caller's request being wrong, so the class is server_error. The code is
		// vendor-prefixed because the specification has no entry for it and
		// requires an additional code to be namespaced.
		task.Error = &uhp.Error{
			Type:    uhp.ErrorTypeServerError,
			Code:    uhpgo.CodeAdapterStartFailed,
			Message: err.Error(),
		}
		task.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateTask(ctx, task)
		return task, nil, err
	}

	// The feed is keyed on the canonical id, so a task started through an alias
	// still shows up on the stream a client opened against the harness itself.
	feed := s.runs.feed(canonicalHarnessID(harnessCfg, req.HarnessID, s.registry))

	run := newRun(task.ID, sessionID, feed, func() {
		if err := adapter.Cancel(runCtx, task.ID); err != nil {
			s.log.Debug("adapter cancel", "error", err, "task_id", task.ID)
		}
	}, release)
	s.runs.add(run)
	supervised = true
	go s.supervise(runCtx, run, task, updates, rs)

	return task, run, nil
}

// resolveSession implements UHP session continuation: if PreviousResponseID
// is set, reuse its session; otherwise mint a new session for this harness.
//
// It reads and never writes. A new session comes back as `fresh`, unpersisted,
// for the caller to store once the task is actually going to run: every refusal
// between here and the fork would otherwise leave behind a session no task will
// ever use.
func (s *TaskService) resolveSession(ctx context.Context, req CreateTaskRequest) (sessionID, nativeSessionID string, fresh *domain.Session, err error) {
	if req.PreviousResponseID == "" {
		now := time.Now().UTC()
		harnessID := req.HarnessID
		if canonical, ok := s.registry.Resolve(req.HarnessID); ok {
			harnessID = canonical
		}
		sess := &domain.Session{
			Session: uhp.Session{
				ID:        "sess_" + uuid.NewString(),
				Object:    "session",
				HarnessID: harnessID,
				Title:     titleFor(req.Input),
				Status:    string(uhp.StatusInProgress),
				CreatedAt: now.Unix(),
				UpdatedAt: now.Unix(),
			},
		}
		return sess.ID, "", sess, nil
	}

	prevTask, found, err := s.store.GetTask(ctx, req.PreviousResponseID)
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: read response %q: %w", ErrStorage, req.PreviousResponseID, err)
	}
	if !found {
		return "", "", nil, fmt.Errorf("%w: previous_response_id %q", ErrResponseNotFound, req.PreviousResponseID)
	}
	sess, found, err := s.store.GetSession(ctx, prevTask.SessionID)
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: read session %q: %w", ErrStorage, prevTask.SessionID, err)
	}
	if !found {
		return "", "", nil, fmt.Errorf("%w: session for %q", ErrSessionNotFound, req.PreviousResponseID)
	}
	// Lifecycle §4: continuing a conversation with a different agent is a
	// different conversation, and doing it quietly loses work the client
	// believed it had.
	//
	// Compare canonical ids on both sides. Sessions record the canonical
	// `chrn_` id, so comparing it against a request that used the friendly
	// base-name alias would report a mismatch between a harness and itself.
	requested := req.HarnessID
	if canonical, ok := s.registry.Resolve(requested); ok {
		requested = canonical
	}
	if requested != "" && sess.HarnessID != "" && requested != sess.HarnessID {
		return "", "", nil, fmt.Errorf("%w: session %s runs on %q, request asked for %q",
			ErrHarnessMismatch, sess.ID, sess.HarnessID, req.HarnessID)
	}
	return sess.ID, sess.NativeSessionID, nil, nil
}

// applyUpdate folds one harness.RunUpdate into the persisted task state and
// returns the events to publish. Only the supervisor calls it, so the task has
// exactly one writer and the lost-update race between cancel and the update
// stream cannot occur.
//
// One update can produce several events, because UHP's vocabulary describes an
// item's lifecycle: the first text delta of a run also opens an output item and
// a content part, and a terminal update closes them.
func (s *TaskService) applyUpdate(ctx context.Context, task *domain.Task, upd harness.RunUpdate, seq *sequencer, rs *runState) ([]uhpgo.Event, error) {
	// Lifecycle §3: "A server MUST NOT transition out of a terminal state."
	if isTerminalStatus(task.Status) {
		return nil, nil
	}

	task.UpdatedAt = time.Now().UTC()

	switch upd.Type {
	case harness.UpdateDelta:
		var evs []uhpgo.Event
		_, existing := task.MessageItem()
		idx, itemID := task.AppendText(upd.Delta)

		if existing == nil {
			// Streaming §3: output_item.added precedes every event referring
			// to that item, and content_part.added opens the part.
			_, item := task.MessageItem()
			shell := *item
			shell.Content = nil
			evs = append(evs,
				seq.next(uhp.Event{
					Type: "response.output_item.added",
					Item: &shell, ItemID: itemID, OutputIndex: intp(idx),
				}),
				seq.next(uhp.Event{
					Type:   "response.content_part.added",
					Part:   &uhp.ContentPart{Type: "output_text", Annotations: []uhp.Annotation{}},
					ItemID: itemID, OutputIndex: intp(idx), ContentIndex: intp(0),
				}),
			)
		}

		if err := s.store.UpdateTask(ctx, task); err != nil {
			return nil, err
		}
		evs = append(evs, seq.next(uhp.Event{
			Type:  "response.output_text.delta",
			Delta: upd.Delta,
			// Without these three a client cannot tell which item, which
			// part of it, or which position a fragment belongs to.
			ItemID: itemID, OutputIndex: intp(idx), ContentIndex: intp(0),
		}))
		return evs, nil

	case harness.UpdateArtifact:
		if upd.Artifact != nil {
			if err := s.store.AppendArtifact(ctx, task.ID, *upd.Artifact); err != nil {
				return nil, err
			}
			task.Artifacts = append(task.Artifacts, *upd.Artifact)
		}
		return nil, nil

	case harness.UpdateUsage:
		task.Usage = upd.Usage
		if err := s.store.UpdateTask(ctx, task); err != nil {
			return nil, err
		}
		return nil, nil

	case harness.UpdateModel:
		// Issue #43: the harness has named the model it is actually running,
		// which beats the default this task was created with — that one is the
		// first entry of an advertised list, and this one is the runtime's own
		// answer.
		//
		// It only ever replaces the router's own guess. A model the client
		// named is left exactly as the client spelled it, and that restriction
		// is not caution — it is a captured failure avoided.
		//
		// `model != requested_model` publishes `model_fallback: true`
		// (domain.Task.MarshalJSON), a claim that the model asked for is not
		// the model that ran. claude's init line reports `claude-opus-5[1m]`
		// where the request said `claude-opus-5`: same model, Claude Code's
		// 1M-context variant, no substitution of any kind. Overwriting there
		// would tell every such client its request had been overridden, on
		// every run. Reporting a fallback that never happened is the same
		// class of defect as reporting nothing at all, and #43 is about the
		// case where the client asked for nothing and can therefore be told
		// nothing but the truth.
		//
		// What would justify widening this is a capture of a CLI actually
		// serving a different model than the one it was given — not the
		// absence of one.
		if upd.Model == "" || task.RequestedModel != "" || upd.Model == task.Model {
			return nil, nil
		}
		task.Model = upd.Model
		// The second sync point. Model is what requested_model and
		// model_fallback are compared against, so the projection has to run
		// again here or the response goes out declaring a substitution that
		// the line above just undid — or failing to declare one it just made.
		task.SyncMetadata()
		if err := s.store.UpdateTask(ctx, task); err != nil {
			return nil, err
		}
		return nil, nil

	case harness.UpdateSessionID:
		// Issue #5: the harness has revealed its own session/thread id, which
		// is what makes a later --resume actually resume something.
		task.NativeSessionID = upd.SessionID
		if err := s.persistNativeSessionID(ctx, task); err != nil {
			s.log.Error("persist native session id", "error", err, "task_id", task.ID)
		}
		return nil, nil

	case harness.UpdateCompleted:
		task.Status = uhp.StatusCompleted
		return s.terminal(ctx, task, seq, "response.completed", rs)

	case harness.UpdateCancelled:
		// Lifecycle §3: a cancelled task MUST report "cancelled", never
		// "failed" — the client that asked for a stop did not hit an error.
		// Output produced before cancellation is retained.
		task.Status = uhp.StatusCancelled
		task.Error = nil
		// Streaming §4: a cancelled task terminates with response.failed
		// carrying status "cancelled"; the status field, not the event name,
		// is authoritative.
		return s.terminal(ctx, task, seq, "response.failed", rs)

	case harness.UpdateFailed:
		task.Status = uhp.StatusFailed
		msg := "the harness could not complete the work"
		if upd.Err != nil {
			msg = upd.Err.Error()
		}
		task.Error = &uhp.Error{Type: uhp.ErrorTypeHarness, Code: uhp.CodeHarnessError, Message: msg}
		return s.terminal(ctx, task, seq, "response.failed", rs)

	default:
		return nil, nil
	}
}

// terminal captures what the run produced, closes any open output item, and
// emits the single terminal event.
//
// Capture happens here rather than at the call sites so that every path to a
// terminal state goes through it — including the one where an adapter closes
// its channel without saying anything. A task that wrote a file and then
// crashed still produced the file.
func (s *TaskService) terminal(ctx context.Context, task *domain.Task, seq *sequencer, evType string, rs *runState) ([]uhpgo.Event, error) {
	var evs []uhpgo.Event

	s.captureArtifacts(ctx, task, rs)
	s.citeArtifacts(task)

	if idx, item := task.MessageItem(); item != nil {
		item.Status = "completed"
		text := ""
		// The closing part repeats the item's own annotations, so a client that
		// followed the stream ends up with the same citations as one that read
		// only the final response.
		annotations := []uhp.Annotation{}
		if len(item.Content) > 0 {
			text = item.Content[0].Text
			if item.Content[0].Annotations != nil {
				annotations = item.Content[0].Annotations
			}
		}
		part := uhp.ContentPart{Type: "output_text", Text: text, Annotations: annotations}
		evs = append(evs,
			seq.next(uhp.Event{
				Type: "response.output_text.done", Text: text,
				ItemID: item.ID, OutputIndex: intp(idx), ContentIndex: intp(0),
			}),
			seq.next(uhp.Event{
				Type: "response.content_part.done", Part: &part,
				ItemID: item.ID, OutputIndex: intp(idx), ContentIndex: intp(0),
			}),
			seq.next(uhp.Event{
				Type: "response.output_item.done", Item: cloneItem(item),
				ItemID: item.ID, OutputIndex: intp(idx),
			}),
		)
	}

	if err := s.store.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	s.markSessionStatus(ctx, task.SessionID, task.Status, task.ID)

	// Streaming §4: the terminal event carries the complete final response, so
	// a client that missed intermediate events can rely on it alone.
	evs = append(evs, seq.next(uhp.Event{Type: evType, Response: responseOf(task)}))
	return evs, nil
}

// persistNativeSessionID stores the harness's own session id on the session,
// so a later task continuing this conversation can pass it to --resume.
func (s *TaskService) persistNativeSessionID(ctx context.Context, task *domain.Task) error {
	if task.SessionID == "" || task.NativeSessionID == "" {
		return nil
	}
	// The session is written before the run that reaches this, so neither answer
	// can be the client's fault: a failed read and a session that is no longer
	// there are both this server losing state mid-run.
	sess, found, err := s.store.GetSession(ctx, task.SessionID)
	if err != nil {
		return fmt.Errorf("%w: read session %q: %w", ErrStorage, task.SessionID, err)
	}
	if !found {
		return fmt.Errorf("%w: session %q vanished mid-run", ErrStorage, task.SessionID)
	}
	sess.NativeSessionID = task.NativeSessionID
	sess.LastResponseID = task.ID
	sess.UpdatedAt = time.Now().UTC().Unix()
	return s.store.UpdateSession(ctx, sess)
}

func intp(i int) *int { return &i }

// previousResponseID renders an absent continuation as the explicit null the
// wire object requires, rather than as an empty string.
//
// The distinction is the client's: `null` says this response continues nothing,
// where `""` would be a response id of no characters. The service works in
// plain strings because an empty one is unambiguous internally, so the
// conversion happens here, once, at the boundary where the two meanings differ.
func previousResponseID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// GetTask answers GET /v1/responses/{id}.
//
// A store that could not be read is ErrStorage and a 500; only an absent row is
// ErrResponseNotFound and a 404. The two used to be the same answer, which told
// a client polling a task that was still running that its task had ceased to
// exist — the one refusal it could not check by asking again, because not
// asking again is what a 404 means.
func (s *TaskService) GetTask(ctx context.Context, id string) (*domain.Task, error) {
	t, found, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: read response %q: %w", ErrStorage, id, err)
	}
	if !found {
		return nil, fmt.Errorf("%w: %q", ErrResponseNotFound, id)
	}
	return t, nil
}

// TaskInputItems answers GET /v1/responses/{id}/input_items: the input the task
// was created with, "for clients that need to reconstruct a transcript without
// having stored it themselves".
//
// It reads the stored items rather than rebuilding them from Task.Input, which
// is the whole point of storing them — Input is the flattened prompt, and a
// rebuild would silently drop every file the request carried.
//
// A task stored before this was implemented has no items and answers with an
// empty list. That is the honest answer: the server genuinely does not know
// what was sent, and an invented single text item would be indistinguishable
// from a task that really was one string.
func (s *TaskService) TaskInputItems(ctx context.Context, id string) ([]json.RawMessage, error) {
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.InputItems == nil {
		return []json.RawMessage{}, nil
	}
	return task.InputItems, nil
}

// DeleteTask removes a stored response (Tasks §4).
//
// It deliberately does not cancel, and the specification says why: "A server
// MUST NOT let this cancel a running task — cancellation and deletion are
// different intentions, and conflating them means a client cannot clean up
// history without stopping work."
//
// So a run in flight is left entirely alone. The supervisor owns it, it reaches
// a terminal state as it would have anyway, and its final UpdateTask writes to
// a row that is no longer there — which the memory store reports as an error
// and SQLite as zero rows affected. Neither is worth failing the delete over:
// the client asked for the record to be gone and it is gone, and the alternative
// is refusing a delete because the work it is not stopping has not finished.
//
// The session is untouched. Disposing of a whole conversation is
// DELETE /v1/traces/{id}, which does cancel first, and keeping the two apart is
// the entire point of this method.
func (s *TaskService) DeleteTask(ctx context.Context, id string) error {
	found, err := s.store.DeleteTask(ctx, id)
	if err != nil {
		return fmt.Errorf("%w: delete response %q: %w", ErrStorage, id, err)
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrResponseNotFound, id)
	}
	return nil
}

// GetRun returns the live run for a task, if it is still in flight.
func (s *TaskService) GetRun(taskID string) (*Run, bool) { return s.runs.get(taskID) }

// CancelTask asks the supervisor to stop a run.
//
// It signals and returns; it does not itself write the task's status. The
// supervisor observes the adapter's "cancelled" update and writes the terminal
// state, so there is exactly one writer and no lost update.
func (s *TaskService) CancelTask(ctx context.Context, taskID string) error {
	task, found, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("%w: read response %q: %w", ErrStorage, taskID, err)
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrResponseNotFound, taskID)
	}

	// Sessions §4: "Cancelling an already-terminal task MUST succeed and
	// change nothing." A client retrying a cancel after a dropped connection
	// should not be told it failed for having succeeded twice.
	if isTerminalStatus(task.Status) {
		return nil
	}

	run, ok := s.runs.get(taskID)
	if !ok {
		// Not terminal and not running: nothing owns it. Settle it here so it
		// cannot sit "in_progress" forever.
		//
		// No capability check: there is no harness process to stop, so nothing
		// is being promised that cannot be delivered. Refusing would leave the
		// task stuck in a non-terminal state that nothing else will ever write.
		task.Status = uhp.StatusCancelled
		task.Error = nil
		task.UpdatedAt = time.Now().UTC()
		return s.store.UpdateTask(ctx, task)
	}

	if err := s.requireHarnessCapability(ctx, task.HarnessID, uhpgo.CapCancellation, whyNoCancellation); err != nil {
		return err
	}

	run.cancel()
	return nil
}

// requireHarnessCapability is requireCapability for a caller that has a harness
// id rather than the harness itself.
//
// A harness that no longer resolves is not refused. It has been deleted since
// the task started, so it advertises nothing at all any more, and refusing to
// stop an agent that is demonstrably still running — the run is in flight, or
// this would not be reached — is the worse of the two failures.
//
// A store that could not be read is a different thing entirely and is passed
// on. Errors §4 makes the class the retry signal, and answering a failed read
// with either a capability refusal or a silent success would tell a client its
// request settled when what actually happened is that this server could not
// read its own state.
func (s *TaskService) requireHarnessCapability(
	ctx context.Context, harnessID string, c uhpgo.Capability, consequence string,
) error {
	h, ok, err := s.GetHarness(ctx, harnessID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return requireCapability(h, c, consequence)
}
