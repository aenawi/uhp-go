package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// testBudget is short enough to keep the suite fast and long enough that the
// adapter's opening delta is delivered before it bites — the tests below assert
// on that delta, and a budget that expired first would be asserting on an
// accident of scheduling.
const testBudget = 80 * time.Millisecond

// Lifecycle §3: "`incomplete` MUST be used when a budget stopped the work, and
// MUST NOT be used for errors." Nothing bounded a run before #54, so this is
// the first status this server has had a reason to write.
func TestABudgetStopsTheRunAndReportsIncomplete(t *testing.T) {
	svc := NewTaskService(newRegistryWith(newSlowAdapter()), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusIncomplete {
		t.Errorf("status = %q, want %q", got.Status, uhp.StatusIncomplete)
	}
	// A budget is not an error, and the distinction is what tells a client the
	// work is worth continuing.
	if got.Error != nil {
		t.Errorf("error = %+v, want null: a budget is not a failure", got.Error)
	}
	if got.IncompleteDetails["reason"] != reasonTimeout {
		t.Errorf("incomplete_details = %+v, want reason %q", got.IncompleteDetails, reasonTimeout)
	}
}

// Streaming §3 lists response.incomplete as one of the three terminal events,
// and it was one this server could never emit.
func TestTheTerminalEventOfAnExpiredRunIsResponseIncomplete(t *testing.T) {
	svc := NewTaskService(newRegistryWith(newSlowAdapter()), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))

	_, run, err := svc.StartTask(context.Background(), CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	evs := collect(t, run)
	if len(evs) == 0 {
		t.Fatal("the run published no events")
	}
	last := evs[len(evs)-1]
	if last.Type != "response.incomplete" {
		t.Fatalf("terminal event = %q, want %q", last.Type, "response.incomplete")
	}
	if last.Response == nil || last.Response.Status != uhp.StatusIncomplete {
		t.Fatalf("the terminal event does not carry an incomplete response: %+v", last.Response)
	}
	for _, ev := range evs[:len(evs)-1] {
		if ev.IsTerminal() {
			t.Fatalf("a second terminal event %q precedes the last one", ev.Type)
		}
	}
}

// Lifecycle §3: "Terminal responses MUST retain whatever output was produced
// before they became terminal." slowAdapter emits one delta before it stalls,
// so a budget that discarded it would be visible here.
func TestOutputProducedBeforeTheBudgetIsRetained(t *testing.T) {
	svc := NewTaskService(newRegistryWith(newSlowAdapter()), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Text() != "partial" {
		t.Errorf("text = %q, want %q: the budget discarded work the run had already produced", got.Text(), "partial")
	}
	if _, item := got.MessageItem(); item == nil || item.Status != "completed" {
		t.Errorf("the open output item was left open on the incomplete path: %+v", item)
	}
}

// The property the wedged-agent scenario in #54 is actually about: N runs that
// never finish take the server permanently to capacity, and every later task is
// refused 503 harness_unavailable — a refusal that tells the client to retry,
// for a condition retrying would never clear.
func TestABudgetGivesBackTheRunSlot(t *testing.T) {
	svc := NewTaskService(newRegistryWith(newSlowAdapter()), newMemStore(), testLogger(),
		WithMaxConcurrentRuns(1), WithTaskBudget(testBudget))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
		if err != nil {
			var full *NoCapacityError
			if errors.As(err, &full) {
				t.Fatalf("run %d refused for capacity: the %d stalled runs before it never gave their slots back", i, i)
			}
			t.Fatalf("StartTask %d: %v", i, err)
		}
		waitFor(t, run)
	}
}

// A budget the client cannot see is one it cannot size a retry against, and the
// resolved value is not necessarily the one it asked for.
func TestTheResolvedBudgetIsReportedToTheClient(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(),
		WithTaskBudget(90*time.Second))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "echo", TimeoutSeconds: intp(30),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Metadata["timeout_seconds"] != 30 {
		t.Errorf("metadata.timeout_seconds = %v, want 30", got.Metadata["timeout_seconds"])
	}
}

// A request may narrow the deployment's bound and may not widen it. Security §5
// makes bounding task duration the server's obligation, and a budget a client
// can raise without limit is not a bound.
func TestTheResolvedBudgetIsClampedToTheServerCeiling(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(),
		WithTaskBudget(60*time.Second))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "echo", TimeoutSeconds: intp(86400),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Metadata["timeout_seconds"] != 60 {
		t.Errorf("metadata.timeout_seconds = %v, want 60: the ceiling was not applied", got.Metadata["timeout_seconds"])
	}
}

// The reporting half of the same rule. A request that was narrowed by the
// harness it named has been answered rather than overruled in silence, so the
// number it is told is the one the supervisor will enforce — the harness's,
// not the one it asked for.
func TestARequestNarrowedByItsHarnessIsToldTheHarnessBudget(t *testing.T) {
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(),
		WithHarnessStore(hs), WithTaskBudget(600*time.Second))
	ctx := context.Background()

	h, err := svc.CreateHarness(ctx, HarnessSpec{Name: "Bounded", Base: "echo", TimeoutSeconds: intp(20)})
	if err != nil {
		t.Fatalf("CreateHarness: %v", err)
	}

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: h.ID, TimeoutSeconds: intp(50),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Metadata["timeout_seconds"] != 20 {
		t.Errorf("metadata.timeout_seconds = %v, want 20: the harness's budget was widened by the request",
			got.Metadata["timeout_seconds"])
	}
}

// Every level is a clamp and none of them is a preference, so the table is one
// rule read from every direction rather than a precedence to be memorised.
func TestEveryBudgetLevelClamps(t *testing.T) {
	const ceiling = 100 * time.Second
	cases := []struct {
		name      string
		requested *int
		harness   *int
		want      time.Duration
	}{
		{"nothing set falls back to the server's own bound", nil, nil, ceiling},
		{"the harness budget is used when the request names none", nil, intp(20), 20 * time.Second},
		{"a request narrows the harness's budget", intp(5), intp(20), 5 * time.Second},
		// The direction that tells a clamp apart from a preference. Both
		// doc comments say a request may narrow the harness's budget and may
		// not widen it, and the case above passes under either reading.
		{"a request may not widen the harness's budget", intp(50), intp(20), 20 * time.Second},
		{"a harness may not widen the request's budget", intp(20), intp(50), 20 * time.Second},
		{"a request budget above the ceiling is clamped", intp(999), nil, ceiling},
		{"a harness budget above the ceiling is clamped", nil, intp(999), ceiling},
		{"a request and a harness above the ceiling are both clamped", intp(999), intp(999), ceiling},
		// Non-positive is not a budget. It reaches here only from stored
		// harness configuration, because the transport refuses one on a
		// request, and treating it as "stop immediately" would fail every task
		// on that harness.
		{"a zero harness budget is not a budget", nil, intp(0), ceiling},
		{"a negative harness budget is not a budget", nil, intp(-1), ceiling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBudget(tc.requested, tc.harness, ceiling); got != tc.want {
				t.Errorf("resolveBudget = %v, want %v", got, tc.want)
			}
		})
	}
}

// A ceiling that is not a whole number of seconds is still a ceiling a named
// budget can go under. Truncating it to whole seconds before the comparison
// makes a request for 20 seconds against a 20.5-second deployment bound come
// back with 20.5 — half a second more than it asked for, which is the widening
// of #75 in miniature. Only a duration-string UHP_TASK_TIMEOUT produces one.
func TestAFractionalCeilingIsStillNarrowedByAWholeSecondBudget(t *testing.T) {
	const ceiling = 20500 * time.Millisecond
	cases := []struct {
		name      string
		requested *int
		harness   *int
		want      time.Duration
	}{
		{"a request naming the truncated ceiling", intp(20), nil, 20 * time.Second},
		{"a harness naming the truncated ceiling", nil, intp(20), 20 * time.Second},
		{"a request above the ceiling leaves it alone", intp(21), nil, ceiling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBudget(tc.requested, tc.harness, ceiling); got != tc.want {
				t.Errorf("resolveBudget = %v, want %v", got, tc.want)
			}
		})
	}
}

// The guard the seconds comparison exists for, from the other side: a ceiling
// under a second has no whole second a named budget could go under, and the
// arithmetic must leave it alone rather than round it to a budget of nothing.
// Only a duration-string UHP_TASK_TIMEOUT can produce one.
func TestASubSecondCeilingIsNotWidenedOrEmptiedByANamedBudget(t *testing.T) {
	const ceiling = 500 * time.Millisecond
	cases := []struct {
		name      string
		requested *int
		harness   *int
	}{
		{"a request naming whole seconds", intp(1), nil},
		{"a harness naming whole seconds", nil, intp(60)},
		{"both naming whole seconds", intp(1), intp(60)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveBudget(tc.requested, tc.harness, ceiling); got != ceiling {
				t.Errorf("resolveBudget = %v, want %v", got, ceiling)
			}
		})
	}
}

// A cancelled task is not an incomplete one: the client asked for a stop and
// got it, so the budget path must not claim the credit.
func TestACancelWithinBudgetStillReportsCancelled(t *testing.T) {
	a := newSlowAdapter()
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(),
		WithTaskBudget(10*time.Second))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "slow"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-a.started
	if err := svc.CancelTask(ctx, task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusCancelled {
		t.Errorf("status = %q, want %q", got.Status, uhp.StatusCancelled)
	}
	if got.IncompleteDetails != nil {
		t.Errorf("incomplete_details = %+v, want null on a cancelled task", got.IncompleteDetails)
	}
}

// A task that finishes inside its budget is untouched by it: no incomplete
// details, and the status the harness reported.
func TestATaskThatFinishesInsideItsBudgetIsCompleted(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(),
		WithTaskBudget(10*time.Second))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusCompleted {
		t.Errorf("status = %q, want %q", got.Status, uhp.StatusCompleted)
	}
	if got.IncompleteDetails != nil {
		t.Errorf("incomplete_details = %+v, want null", got.IncompleteDetails)
	}
}

// stalledAdapter is the adapter bug, met on the budget path: it stops when
// cancelled and closes its channel without ever reporting a terminal state.
// The supervisor has to settle the task itself, and after a budget the honest
// answer is `incomplete` rather than the `failed` it writes otherwise.
type stalledAdapter struct{ *slowAdapter }

func newStalledAdapter() *stalledAdapter { return &stalledAdapter{newSlowAdapter()} }

func (a *stalledAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{Harness: uhp.Harness{ID: "stalled", Name: "Stalled"},
		Capabilities: []uhpgo.Capability{uhpgo.CapStreaming, uhpgo.CapCancellation}}
}

func (a *stalledAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel[req.TaskID] = cancel
	a.mu.Unlock()

	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		defer cancel()
		select {
		case ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "partial"}:
		case <-ctx.Done():
			return
		}
		<-runCtx.Done()
	}()
	return ch, nil
}

func TestAnExpiredRunThatNeverReportsTerminalIsIncompleteNotFailed(t *testing.T) {
	svc := NewTaskService(newRegistryWith(newStalledAdapter()), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "stalled"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusIncomplete {
		t.Fatalf("status = %q, want %q: a budget stopped this run, whatever the adapter forgot to say", got.Status, uhp.StatusIncomplete)
	}
	if got.Error != nil {
		t.Errorf("error = %+v, want null", got.Error)
	}
	if got.Text() != "partial" {
		t.Errorf("text = %q, want %q", got.Text(), "partial")
	}
}

// The harness's own budget is configuration a client can set through
// POST /v1/harnesses, read back, and — before #54 — watch do nothing. It is
// the half of #54 that #48 explicitly does not cover.
//
// One second is the shortest budget this path can express, because the field
// is in seconds, so this is the slowest test in the file by design rather than
// by accident.
func TestAHarnessBudgetBoundsARunOnIt(t *testing.T) {
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	svc := NewTaskService(newRegistryWith(newSlowBase()), newMemStore(), testLogger(),
		WithHarnessStore(hs), WithTaskBudget(time.Minute))
	ctx := context.Background()

	h, err := svc.CreateHarness(ctx, HarnessSpec{Name: "Bounded", Base: "slow-base", TimeoutSeconds: intp(1)})
	if err != nil {
		t.Fatalf("CreateHarness: %v", err)
	}

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: h.ID})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusIncomplete {
		t.Errorf("status = %q, want %q: the harness's configured budget did nothing", got.Status, uhp.StatusIncomplete)
	}
	if got.Metadata["timeout_seconds"] != 1 {
		t.Errorf("metadata.timeout_seconds = %v, want 1", got.Metadata["timeout_seconds"])
	}
}

// slowBase is slowAdapter with a base name, so a managed harness can be built
// on it. A harness created over the API names a base, and slowAdapter has none.
type slowBase struct{ *slowAdapter }

func newSlowBase() slowBase { return slowBase{newSlowAdapter()} }

func (slowBase) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness: uhp.Harness{ID: "chrn_slow_base", Base: "slow-base", Object: "harness", Name: "Slow base"},
		Capabilities: []uhpgo.Capability{
			uhpgo.CapStreaming, uhpgo.CapSessions, uhpgo.CapCancellation,
		},
		Status: uhpgo.HarnessReady,
	}
}

// dyingAdapter is the teardown that actually happens, and the one no other
// double in this file produces: a CLI killed on its deadline that prints
// something on the way out.
//
// Three of the five real harnesses report a problem by printing it rather than
// by their exit code (harness.harnessFailure), so parseLine turns that into an
// UpdateFailed before the runner's own terminal switch is reached — and
// process.run tests its scan error ahead of its own cancellation besides. Both
// arrive here as `failed` from a run the budget stopped.
type dyingAdapter struct{ *slowAdapter }

func newDyingAdapter() *dyingAdapter { return &dyingAdapter{newSlowAdapter()} }

func (a *dyingAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{Harness: uhp.Harness{ID: "dying", Name: "Dying"},
		Capabilities: []uhpgo.Capability{uhpgo.CapStreaming, uhpgo.CapCancellation}}
}

func (a *dyingAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel[req.TaskID] = cancel
	a.mu.Unlock()

	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		defer cancel()
		send := func(u harness.RunUpdate) bool {
			select {
			case ch <- u:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(harness.RunUpdate{Type: harness.UpdateDelta, Delta: "partial"}) {
			return
		}
		<-runCtx.Done()
		send(harness.RunUpdate{Type: harness.UpdateFailed, Err: errTornDown})
	}()
	return ch, nil
}

var errTornDown = errors.New("harness: claude: output truncated: read |0: file already closed")

// Lifecycle §3: "`incomplete` MUST be used when a budget stopped the work, and
// MUST NOT be used for errors." A budget that reports `failed` because the kill
// it issued tore the CLI's own output is the MUST inverted — and it inverts it
// on the teardown most likely to happen, not on an exotic one.
func TestAHarnessThatFailsWhileItsBudgetTearsItDownIsIncomplete(t *testing.T) {
	svc := NewTaskService(newRegistryWith(newDyingAdapter()), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "dying"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusIncomplete {
		t.Fatalf("status = %q, want %q: the budget's own teardown was reported as a harness failure",
			got.Status, uhp.StatusIncomplete)
	}
	if got.Error != nil {
		t.Errorf("error = %+v, want null: a budget is not an error", got.Error)
	}
	if got.IncompleteDetails["reason"] != reasonTimeout {
		t.Errorf("incomplete_details = %+v, want reason %q", got.IncompleteDetails, reasonTimeout)
	}
	if got.Text() != "partial" {
		t.Errorf("text = %q, want %q", got.Text(), "partial")
	}
}

// The other half of the relabel, and the reason it is not "everything
// terminal": an agent that finishes inside the window between the deadline
// firing and the kill landing produced whole work. The MUST is not to report
// `completed` for work that was truncated, and this work was not.
func TestAnAgentThatFinishesAsItsBudgetFiresIsStillCompleted(t *testing.T) {
	a := newRacingAdapter()
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "racing"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusCompleted {
		t.Errorf("status = %q, want %q: whole work was reported as truncated", got.Status, uhp.StatusCompleted)
	}
	if got.IncompleteDetails != nil {
		t.Errorf("incomplete_details = %+v, want null on a completed task", got.IncompleteDetails)
	}
}

// racingAdapter answers `completed` only once its budget has already stopped
// it, which is the race the relabel has to leave alone.
type racingAdapter struct{ *slowAdapter }

func newRacingAdapter() *racingAdapter { return &racingAdapter{newSlowAdapter()} }

func (a *racingAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{Harness: uhp.Harness{ID: "racing", Name: "Racing"},
		Capabilities: []uhpgo.Capability{uhpgo.CapStreaming, uhpgo.CapCancellation}}
}

func (a *racingAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel[req.TaskID] = cancel
	a.mu.Unlock()

	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		defer cancel()
		select {
		case ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "whole"}:
		case <-ctx.Done():
			return
		}
		<-runCtx.Done()
		select {
		case ch <- harness.RunUpdate{Type: harness.UpdateCompleted}:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// A budget under a second still has to be reported, or a server that enforces
// a bound tells the client nothing about it. Only a duration-string
// UHP_TASK_TIMEOUT can produce one, but truncating it to zero makes
// SyncMetadata drop `timeout_seconds` entirely — which is the reporting half
// of #54 quietly undone.
func TestASubSecondBudgetIsStillReported(t *testing.T) {
	cases := []struct {
		budget time.Duration
		want   int
	}{
		{500 * time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{90 * time.Second, 90},
	}
	for _, tc := range cases {
		t.Run(tc.budget.String(), func(t *testing.T) {
			if got := budgetSeconds(tc.budget); got != tc.want {
				t.Errorf("budgetSeconds(%v) = %d, want %d", tc.budget, got, tc.want)
			}
		})
	}
}

// tearingDownAdapter is the teardown window itself, held open. It stalls until
// its own Cancel is called, announces that it has started stopping, and then
// waits to be released before it reports anything terminal.
//
// Every real teardown has that window — the adapter's Cancel, the signal, and
// the Wait that process.run backstops with `cmd.WaitDelay = 5 * time.Second` —
// and holding it open is what makes a client cancel that lands inside it
// something a test can arrange rather than sleep through.
type tearingDownAdapter struct {
	*slowAdapter
	stopping chan struct{}
	release  chan struct{}
	// terminal is what the adapter finally says, or nil for the adapter bug:
	// a stream closed without any terminal state at all.
	terminal *harness.RunUpdate
}

func newTearingDownAdapter(terminal *harness.RunUpdate) *tearingDownAdapter {
	return &tearingDownAdapter{
		slowAdapter: newSlowAdapter(),
		stopping:    make(chan struct{}),
		release:     make(chan struct{}),
		terminal:    terminal,
	}
}

func (a *tearingDownAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{Harness: uhp.Harness{ID: "tearing-down", Name: "Tearing down"},
		Capabilities: []uhpgo.Capability{uhpgo.CapStreaming, uhpgo.CapCancellation}}
}

func (a *tearingDownAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel[req.TaskID] = cancel
	a.mu.Unlock()

	ch := make(chan harness.RunUpdate)
	go func() {
		defer close(ch)
		defer cancel()
		select {
		case ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "partial"}:
		case <-ctx.Done():
			return
		}
		<-runCtx.Done()
		close(a.stopping)
		<-a.release
		if a.terminal == nil {
			return
		}
		select {
		case ch <- *a.terminal:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// Issue #76: a budget's teardown is not the scheduling quantum the comment
// defending this used to claim. The run stays in it for the adapter's Cancel,
// the signal, and the Wait behind them, so a client calling
// POST /v1/responses/{id}/cancel seconds after a deadline fired was answered
// `incomplete` with reason `timeout` — the status a client retries, for work
// somebody stopped on purpose. On a wedged agent that invites a re-run of the
// thing that wedged.
//
// A stop someone asked for is a fact, and it outranks a guess about which
// goroutine noticed first.
func TestACancelDuringABudgetTeardownIsReportedAsCancelled(t *testing.T) {
	cases := []struct {
		name     string
		terminal *harness.RunUpdate
	}{
		{"the adapter reports the stop", &harness.RunUpdate{Type: harness.UpdateCancelled}},
		// The teardown that actually happens: a CLI killed mid-sentence comes
		// back `failed`, and the cancel still outranks it.
		{"the adapter fails as it is torn down", &harness.RunUpdate{Type: harness.UpdateFailed, Err: errTornDown}},
		// And the adapter bug on the same path: nothing terminal at all, so
		// the supervisor settles the task itself.
		{"the adapter reports nothing at all", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTearingDownAdapter(tc.terminal)
			svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(),
				WithTaskBudget(testBudget))
			ctx := context.Background()

			task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "tearing-down"})
			if err != nil {
				t.Fatalf("StartTask: %v", err)
			}

			// The deadline has fired and the adapter is already stopping, so
			// the cancel below lands squarely inside the window that used to
			// relabel it.
			<-a.stopping
			if err := svc.CancelTask(ctx, task.ID); err != nil {
				t.Fatalf("CancelTask: %v", err)
			}
			close(a.release)
			waitFor(t, run)

			got, err := svc.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if got.Status != uhp.StatusCancelled {
				t.Fatalf("status = %q, want %q: a stop the client asked for was reported as the budget's",
					got.Status, uhp.StatusCancelled)
			}
			if got.IncompleteDetails != nil {
				t.Errorf("incomplete_details = %+v, want null on a cancelled task", got.IncompleteDetails)
			}
			if got.Error != nil {
				t.Errorf("error = %+v, want null: someone asked for this stop", got.Error)
			}
			// Lifecycle §3: "Terminal responses MUST retain whatever output
			// was produced before they became terminal."
			if got.Text() != "partial" {
				t.Errorf("text = %q, want %q", got.Text(), "partial")
			}

			// Streaming §4: a cancelled task terminates with response.failed
			// carrying status `cancelled`; the status field, not the event
			// name, is authoritative. What must not be there is
			// response.incomplete.
			evs := collect(t, run)
			if len(evs) == 0 {
				t.Fatal("the run published no events")
			}
			last := evs[len(evs)-1]
			if last.Type != "response.failed" {
				t.Errorf("terminal event = %q, want %q", last.Type, "response.failed")
			}
			if last.Response == nil || last.Response.Status != uhp.StatusCancelled {
				t.Errorf("the terminal event does not carry a cancelled response: %+v", last.Response)
			}
		})
	}
}

// The other direction, and the one the relabel exists for: nobody asked for a
// stop, so the budget keeps the credit and the task is `incomplete`. A fix that
// settled every expired run as `cancelled` would pass the test above and invert
// Lifecycle §3 here.
func TestABudgetTeardownNobodyCancelledIsStillIncomplete(t *testing.T) {
	a := newTearingDownAdapter(&harness.RunUpdate{Type: harness.UpdateFailed, Err: errTornDown})
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "tearing-down"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-a.stopping
	close(a.release)
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusIncomplete {
		t.Fatalf("status = %q, want %q", got.Status, uhp.StatusIncomplete)
	}
	if got.IncompleteDetails["reason"] != reasonTimeout {
		t.Errorf("incomplete_details = %+v, want reason %q", got.IncompleteDetails, reasonTimeout)
	}
}

// Cancelling the session is the other way a client asks for the same stop, and
// Sessions §4 makes it a distinct scope rather than a distinct outcome: what it
// stops is still a stop somebody asked for, so it must outrank the budget too.
func TestACancelledSessionDuringABudgetTeardownIsReportedAsCancelled(t *testing.T) {
	a := newTearingDownAdapter(&harness.RunUpdate{Type: harness.UpdateCancelled})
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "tearing-down"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	<-a.stopping
	if err := svc.CancelSession(ctx, task.SessionID); err != nil {
		t.Fatalf("CancelSession: %v", err)
	}
	close(a.release)
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusCancelled {
		t.Fatalf("status = %q, want %q", got.Status, uhp.StatusCancelled)
	}
	if got.IncompleteDetails != nil {
		t.Errorf("incomplete_details = %+v, want null on a cancelled task", got.IncompleteDetails)
	}
}

// The carve-out, from the cancel side. An agent that actually finished inside
// the teardown window produced whole work, and neither the budget nor a cancel
// racing it takes that away: the MUST is not to report `completed` for work
// that was truncated, and this work was not.
func TestAnAgentThatFinishesAsItIsCancelledDuringItsBudgetTeardownIsStillCompleted(t *testing.T) {
	a := newTearingDownAdapter(&harness.RunUpdate{Type: harness.UpdateCompleted})
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger(),
		WithTaskBudget(testBudget))
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "tearing-down"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-a.stopping
	if err := svc.CancelTask(ctx, task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	close(a.release)
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusCompleted {
		t.Errorf("status = %q, want %q: whole work was reported as stopped", got.Status, uhp.StatusCompleted)
	}
	if got.IncompleteDetails != nil {
		t.Errorf("incomplete_details = %+v, want null on a completed task", got.IncompleteDetails)
	}
}
