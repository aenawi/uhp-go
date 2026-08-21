// Package service contains the application core: orchestration logic that is
// independent of transport (HTTP) and infrastructure (CLI adapters, stores).
// It depends on the harness contract and on the Registry and Store interfaces
// it declares for itself in deps.go — never on a concrete adapter or storage
// engine.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
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

// startTask resolves the target harness and session, persists the initial
// task, and hands it to a supervisor goroutine that owns it from then on.
//
// It returns as soon as the run is started. It does not wait for the task, and
// the returned Run stays valid — and the task keeps running — regardless of
// what the caller does with it.
func (s *TaskService) startTask(ctx context.Context, req CreateTaskRequest) (*domain.Task, *Run, error) {
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

	now := time.Now().UTC()
	task := &domain.Task{
		ID:                 "resp_" + uuid.NewString(),
		Object:             "response",
		Status:             domain.StatusInProgress,
		Model:              req.Model,
		RequestedModel:     req.Model,
		HarnessID:          req.HarnessID,
		SessionID:          sessionID,
		PreviousResponseID: req.PreviousResponseID,
		Input:              input,
		Metadata:           req.Metadata,
		// Tasks §1.1: `store` defaults to true.
		Store:     true,
		CreatedAt: now,
		UpdatedAt: now,
	}
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
		InputFiles:      inputPaths,
		WorkDir:         workDir,
		SkillDirs:       runtime.SkillDirs,
		McpConfigPath:   runtime.McpConfigPath,
		DisabledTools:   runtime.DisabledTools,
	})
	if err != nil {
		task.Status = domain.StatusFailed
		task.Error = &domain.TaskError{Code: "adapter_start_failed", Message: err.Error()}
		task.UpdatedAt = time.Now().UTC()
		_ = s.store.UpdateTask(ctx, task)
		return task, nil, err
	}

	run := newRun(task.ID, sessionID, func() {
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
			ID:        "sess_" + uuid.NewString(),
			HarnessID: harnessID,
			Title:     titleFor(req.Input),
			Status:    domain.StatusInProgress,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return sess.ID, "", sess, nil
	}

	prevTask, err := s.store.GetTask(ctx, req.PreviousResponseID)
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: previous_response_id %q", ErrResponseNotFound, req.PreviousResponseID)
	}
	sess, err := s.store.GetSession(ctx, prevTask.SessionID)
	if err != nil {
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
func (s *TaskService) applyUpdate(ctx context.Context, task *domain.Task, upd harness.RunUpdate, seq *sequencer, rs *runState) ([]domain.Event, error) {
	// Lifecycle §3: "A server MUST NOT transition out of a terminal state."
	if isTerminalStatus(task.Status) {
		return nil, nil
	}

	task.UpdatedAt = time.Now().UTC()

	switch upd.Type {
	case harness.UpdateDelta:
		var evs []domain.Event
		_, existing := task.MessageItem()
		idx, itemID := task.AppendText(upd.Delta)

		if existing == nil {
			// Streaming §3: output_item.added precedes every event referring
			// to that item, and content_part.added opens the part.
			_, item := task.MessageItem()
			shell := *item
			shell.Content = nil
			evs = append(evs,
				seq.next(domain.Event{
					Type: "response.output_item.added",
					Item: &shell, ItemID: itemID, OutputIndex: intp(idx),
				}),
				seq.next(domain.Event{
					Type:   "response.content_part.added",
					Part:   &domain.ContentPart{Type: "output_text", Annotations: []domain.Annotation{}},
					ItemID: itemID, OutputIndex: intp(idx), ContentIndex: intp(0),
				}),
			)
		}

		if err := s.store.UpdateTask(ctx, task); err != nil {
			return nil, err
		}
		evs = append(evs, seq.next(domain.Event{
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

	case harness.UpdateSessionID:
		// Issue #5: the harness has revealed its own session/thread id, which
		// is what makes a later --resume actually resume something.
		task.NativeSessionID = upd.SessionID
		if err := s.persistNativeSessionID(ctx, task); err != nil {
			s.log.Error("persist native session id", "error", err, "task_id", task.ID)
		}
		return nil, nil

	case harness.UpdateCompleted:
		task.Status = domain.StatusCompleted
		return s.terminal(ctx, task, seq, "response.completed", rs)

	case harness.UpdateCancelled:
		// Lifecycle §3: a cancelled task MUST report "cancelled", never
		// "failed" — the client that asked for a stop did not hit an error.
		// Output produced before cancellation is retained.
		task.Status = domain.StatusCancelled
		task.Error = nil
		// Streaming §4: a cancelled task terminates with response.failed
		// carrying status "cancelled"; the status field, not the event name,
		// is authoritative.
		return s.terminal(ctx, task, seq, "response.failed", rs)

	case harness.UpdateFailed:
		task.Status = domain.StatusFailed
		msg := "the harness could not complete the work"
		if upd.Err != nil {
			msg = upd.Err.Error()
		}
		task.Error = &domain.TaskError{Type: "harness_error", Code: "harness_error", Message: msg, Retryable: true}
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
func (s *TaskService) terminal(ctx context.Context, task *domain.Task, seq *sequencer, evType string, rs *runState) ([]domain.Event, error) {
	var evs []domain.Event

	s.captureArtifacts(ctx, task, rs)
	s.citeArtifacts(task)

	if idx, item := task.MessageItem(); item != nil {
		item.Status = "completed"
		text := ""
		// The closing part repeats the item's own annotations, so a client that
		// followed the stream ends up with the same citations as one that read
		// only the final response.
		annotations := []domain.Annotation{}
		if len(item.Content) > 0 {
			text = item.Content[0].Text
			if item.Content[0].Annotations != nil {
				annotations = item.Content[0].Annotations
			}
		}
		part := domain.ContentPart{Type: "output_text", Text: text, Annotations: annotations}
		evs = append(evs,
			seq.next(domain.Event{
				Type: "response.output_text.done", Text: text,
				ItemID: item.ID, OutputIndex: intp(idx), ContentIndex: intp(0),
			}),
			seq.next(domain.Event{
				Type: "response.content_part.done", Part: &part,
				ItemID: item.ID, OutputIndex: intp(idx), ContentIndex: intp(0),
			}),
			seq.next(domain.Event{
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
	evs = append(evs, seq.next(domain.Event{Type: evType, Response: cloneTask(task)}))
	return evs, nil
}

// persistNativeSessionID stores the harness's own session id on the session,
// so a later task continuing this conversation can pass it to --resume.
func (s *TaskService) persistNativeSessionID(ctx context.Context, task *domain.Task) error {
	if task.SessionID == "" || task.NativeSessionID == "" {
		return nil
	}
	sess, err := s.store.GetSession(ctx, task.SessionID)
	if err != nil {
		return err
	}
	sess.NativeSessionID = task.NativeSessionID
	sess.LastResponseID = task.ID
	sess.UpdatedAt = time.Now().UTC()
	return s.store.UpdateSession(ctx, sess)
}

func intp(i int) *int { return &i }

// GetTask answers GET /v1/responses/{id}.
func (s *TaskService) GetTask(ctx context.Context, id string) (*domain.Task, error) {
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrResponseNotFound, id)
	}
	return t, nil
}

// GetRun returns the live run for a task, if it is still in flight.
func (s *TaskService) GetRun(taskID string) (*Run, bool) { return s.runs.get(taskID) }

// CancelTask asks the supervisor to stop a run.
//
// It signals and returns; it does not itself write the task's status. The
// supervisor observes the adapter's "cancelled" update and writes the terminal
// state, so there is exactly one writer and no lost update.
func (s *TaskService) CancelTask(ctx context.Context, taskID string) error {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
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
		task.Status = domain.StatusCancelled
		task.Error = nil
		task.UpdatedAt = time.Now().UTC()
		return s.store.UpdateTask(ctx, task)
	}

	run.cancel()
	return nil
}
