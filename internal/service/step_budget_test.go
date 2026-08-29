package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// The enforcement half of #72. The harness package establishes what each base
// narrates; this establishes what the router does with it, which is the half a
// client's budget actually rests on.

// steppingAdapter narrates tool calls until it is stopped, then reports a
// terminal update of whatever kind it was built with.
//
// It is unbounded on purpose. A double that stopped by itself after N calls
// would pass every test here against a router that counted nothing, which is
// the exact defect these tests exist to catch — so the only thing that ever
// ends one of these runs is the ceiling.
type steppingAdapter struct {
	id string

	// edge is what this base claims to narrate, and is the reason the double is
	// parameterised rather than hard-coded: the whole point of StepEdge is that
	// the comparison differs, and a test that only ever ran one of them would
	// leave the other's off-by-one to a client.
	edge harness.StepEdge

	// terminal is what the adapter reports once it is cancelled. `failed` is
	// the default because it is what really happens — a CLI whose stdout is
	// torn by the kill comes back a failure — and relabelling it is the thing
	// most likely to be got wrong.
	terminal harness.UpdateType

	mu     sync.Mutex
	cancel map[string]context.CancelFunc

	// steps counts what the adapter actually narrated, so a test can say how
	// many calls the agent got to make rather than only how the task ended.
	stepsMu sync.Mutex
	steps   int
}

func newSteppingAdapter(id string, edge harness.StepEdge) *steppingAdapter {
	return &steppingAdapter{
		id: id, edge: edge, terminal: harness.UpdateFailed,
		cancel: make(map[string]context.CancelFunc),
	}
}

func (a *steppingAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness: uhp.Harness{ID: a.id, Base: a.id, Object: "harness", Name: "Stepping"},
		Capabilities: []uhpgo.Capability{
			uhpgo.CapStreaming, uhpgo.CapSessions, uhpgo.CapCancellation, uhpgo.CapTools,
		},
		Status: uhpgo.HarnessReady,
	}
}

func (a *steppingAdapter) HealthCheck(context.Context) error { return nil }
func (a *steppingAdapter) StepEdge() harness.StepEdge        { return a.edge }

func (a *steppingAdapter) narrated() int {
	a.stepsMu.Lock()
	defer a.stepsMu.Unlock()
	return a.steps
}

func (a *steppingAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
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
			case <-runCtx.Done():
				return false
			}
		}
		// Far more calls than any test's ceiling, so the run only ever ends by
		// being stopped. The loop also exits on its own, so a router that
		// counted nothing fails the assertion rather than hanging the suite.
		for i := 0; i < 100; i++ {
			if !send(harness.RunUpdate{Type: harness.UpdateToolCall}) {
				break
			}
			a.stepsMu.Lock()
			a.steps++
			a.stepsMu.Unlock()
		}
		send(harness.RunUpdate{Type: a.terminal, Err: errors.New("stdout closed mid-read")})
	}()
	return ch, nil
}

func (a *steppingAdapter) Cancel(_ context.Context, taskID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cancel, ok := a.cancel[taskID]; ok {
		cancel()
	}
	return nil
}

// intp is a pointer to n, for the pointer-shaped budgets below. Zero is a real
// ceiling here, so it cannot be spelled by omission.
func stepp(n int) *int { return &n }

// A ceiling stops the run, and stops it as `incomplete` rather than `failed` —
// which is the whole point: Lifecycle §3 requires `incomplete` for a budget and
// forbids it for an error, and the adapter here reports a failure on its way
// out exactly as a real CLI torn by the kill does.
func TestAStepBudgetStopsTheRunAndReportsIncomplete(t *testing.T) {
	a := newSteppingAdapter("stepping", harness.StepEdgeStart)
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "stepping", MaxStep: stepp(3),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusIncomplete {
		t.Errorf("status = %q, want %q — a run stopped by a ceiling is work worth "+
			"continuing, not work that could not be done", got.Status, uhp.StatusIncomplete)
	}
	if got.Error != nil {
		t.Errorf("error = %+v, want null: a budget is not a failure", got.Error)
	}
	if got.IncompleteDetails["reason"] != harness.ReasonMaxStep {
		t.Errorf("incomplete_details = %+v, want reason %q — a step ceiling reported as "+
			"`timeout` tells a client to wait when what it needs is more steps",
			got.IncompleteDetails, harness.ReasonMaxStep)
	}
}

// The exact moment the ceiling is reached. This is a unit test rather than
// another end-to-end one deliberately: the question is an off-by-one, and a
// run's observable count is not exact to a single call — the adapter is mid-send
// when the cancel lands, and Go's select is free to take the send. A test with
// slack that wide could not see the thing it exists to catch, so the exactness
// is asserted where it is exact.
//
// One comparison serves both counted edges, and the first two rows are why it
// has to. The ceiling is spent by the event *after* the last one allowed. On a
// start edge that event is a request, so nothing more runs; on a finish edge it
// is a completion, so `opencode` takes one call more than it was given.
//
// Stopping a finish-edge run on the ceiling'th completion instead would keep the
// count exact and break the ordinary case: a run given five calls that uses
// exactly five would be killed the moment the fifth finished, before writing its
// answer, while the same request on claude completed. That is the trade this
// test pins.
func TestStepBudgetSpent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edge  harness.StepEdge
		max   int
		spent []bool // indexed by steps-1, so spent[i] is the (i+1)th event
	}{
		{
			// Ceiling 3: calls 1, 2 and 3 are asked for and run; the request
			// for call 4 is what stops it. Three calls happened.
			"a start edge allows the ceiling and stops the next request",
			harness.StepEdgeStart, 3,
			[]bool{false, false, false, true},
		},
		{
			// Ceiling 3: calls 1, 2 and 3 complete and the run carries on — it
			// used exactly what it was given and must be allowed to answer. A
			// fourth completion is one call past the ceiling and stops it,
			// having already happened.
			"a finish edge lets a compliant run finish, and stops on the call past the ceiling",
			harness.StepEdgeFinish, 3,
			[]bool{false, false, false, true},
		},
		{
			// The tightest bound the field has, and the only edge that can
			// deliver it: the call is stopped before it runs.
			"a ceiling of zero is spent by the first event",
			harness.StepEdgeStart, 0, []bool{true},
		},
		{
			// Defensive rather than reachable, and the clearest illustration of
			// why zero is refused on this edge: the stop is immediate and the
			// call has still happened, because a finish-edge base says nothing
			// until it has. requireStepBudget turns this combination away before
			// a run starts.
			"a ceiling of zero on a finish edge stops at once, though it cannot be asked for",
			harness.StepEdgeFinish, 0, []bool{true},
		},
		{
			// grok holds its own ceiling and reports its own stop, so counting
			// it here would spend the budget twice on a base already told the
			// number.
			"a native base is never spent by the router",
			harness.StepEdgeNative, 1, []bool{false, false, false},
		},
		{
			// Not reachable through StartTask, which refuses the task instead —
			// but the arm has to answer something, and "never spent" is the
			// answer that cannot silently stop a run nobody bounded.
			"an uncountable base is never spent",
			harness.StepEdgeNone, 1, []bool{false, false, false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i, want := range tc.spent {
				steps := i + 1
				if got := stepBudgetSpent(steps, tc.max, tc.edge); got != want {
					t.Errorf("stepBudgetSpent(%d, %d, %q) = %v, want %v",
						steps, tc.max, tc.edge, got, want)
				}
			}
		})
	}
}

// And the ceiling fires where it was set rather than eventually. The end-to-end
// counterpart to the unit test above, with the slack the real teardown needs:
// what it rules out is a router that counts nothing and lets the agent run to
// the end of its rope.
func TestAStepBudgetStopsNearTheNumberItWasGiven(t *testing.T) {
	for _, edge := range []harness.StepEdge{harness.StepEdgeStart, harness.StepEdgeFinish} {
		t.Run(string(edge), func(t *testing.T) {
			a := newSteppingAdapter("stepping", edge)
			svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())

			_, run, err := svc.StartTask(context.Background(), CreateTaskRequest{
				Input: "x", HarnessID: "stepping", MaxStep: stepp(4),
			})
			if err != nil {
				t.Fatalf("StartTask: %v", err)
			}
			waitFor(t, run)

			// The adapter offers a hundred calls and stops only when cancelled,
			// so anything near the ceiling proves the count happened at all.
			// The upper slack is the teardown: the adapter is blocked mid-send
			// when the cancel lands and Go's select may take the send.
			if got := a.narrated(); got < 3 || got > 6 {
				t.Errorf("the agent narrated %d calls against a ceiling of 4 — a ceiling "+
					"that fires this far out is not the number the client was told", got)
			}
		})
	}
}

// `max_step: 0` is a coherent request — run, but call no tools — and is the
// tightest bound the field can express. It is also the one a naive
// implementation drops, because zero is the natural spelling of "no budget".
func TestAZeroStepBudgetPermitsNoToolCall(t *testing.T) {
	a := newSteppingAdapter("stepping", harness.StepEdgeStart)
	svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "stepping", MaxStep: stepp(0),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusIncomplete {
		t.Errorf("status = %q, want %q: a ceiling of zero was treated as no ceiling",
			got.Status, uhp.StatusIncomplete)
	}
	if got.MaxStep == nil || *got.MaxStep != 0 {
		t.Errorf("MaxStep = %v, want a pointer to 0 — reported as absent, a client is told "+
			"its tightest bound was dropped", got.MaxStep)
	}
}

// The other direction, and the one that would go unnoticed: a run that never
// calls a tool must finish normally under any ceiling. Without this, every
// "what does this file do" question on a bounded harness comes back
// `incomplete`.
func TestACeilingDoesNotBreakARunThatCallsNothing(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger())
	ctx := context.Background()

	task, run, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "echo", MaxStep: stepp(0),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitFor(t, run)

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != uhp.StatusCompleted {
		t.Errorf("status = %q, want %q: an agent that only talks spends no steps",
			got.Status, uhp.StatusCompleted)
	}
}

// An unbounded task is the ordinary one, and nothing about #72 may change it.
// There is no default ceiling: the wall clock already discharges Security §5's
// obligation, and a surprise step budget would break every task that
// legitimately takes forty calls.
func TestATaskThatAskedForNoCeilingGetsNone(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger())
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
	if got.MaxStep != nil {
		t.Errorf("MaxStep = %v, want nil: nothing supplies a default step ceiling", *got.MaxStep)
	}
	if _, present := got.Metadata["max_step"]; present {
		t.Errorf("metadata.max_step = %v is present on an unbounded task — absent and "+
			"a number are different answers", got.Metadata["max_step"])
	}
}

// A client may narrow what its deployment allows and may not widen it, and is
// told the number that will actually be enforced rather than discovering it by
// being stopped early. Same rule as the wall clock's, same reason.
func TestTheReportedCeilingIsTheOneEnforced(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(),
		WithTaskMaxStep(10))
	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		requested *int
		want      int
	}{
		{"a request above the deployment's ceiling is narrowed", stepp(100), 10},
		{"a request below it is honoured", stepp(2), 2},
		{"a request that named none inherits the ceiling", nil, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, run, err := svc.StartTask(ctx, CreateTaskRequest{
				Input: "x", HarnessID: "echo", MaxStep: tc.requested,
			})
			if err != nil {
				t.Fatalf("StartTask: %v", err)
			}
			waitFor(t, run)

			got, err := svc.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if got.MaxStep == nil || *got.MaxStep != tc.want {
				t.Fatalf("MaxStep = %v, want %d", got.MaxStep, tc.want)
			}
			if got.Metadata["max_step"] != tc.want {
				t.Errorf("metadata.max_step = %v, want %d — the resolved number is what a "+
					"client can act on", got.Metadata["max_step"], tc.want)
			}
		})
	}
}

// ADR-0007's rule at the one place it can still be broken: a grant may be
// per-base, a bound may not. A base that cannot hold the ceiling refuses the
// task rather than accepting a number nobody enforces.
func TestACeilingIsRefusedOnABaseThatCannotHoldIt(t *testing.T) {
	svc := NewTaskService(newRegistryWith(uncountableAdapter{}), newMemStore(), testLogger())
	ctx := context.Background()

	_, _, err := svc.StartTask(ctx, CreateTaskRequest{
		Input: "x", HarnessID: "uncountable", MaxStep: stepp(5),
	})
	if !errors.Is(err, ErrStepBudgetUnsupported) {
		t.Fatalf("error = %v, want ErrStepBudgetUnsupported: a bound accepted on a base "+
			"that cannot hold it is the silence ADR-0004 removed, wearing the appearance "+
			"of success", err)
	}

	// And only the ceiling is refused. The same base must still serve every
	// task that asked for no budget, or a harness would be taken out of service
	// by a field nobody used.
	task, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "x", HarnessID: "uncountable"})
	if err != nil {
		t.Fatalf("an unbounded task was refused too: %v", err)
	}
	waitFor(t, run)
	if got, err := svc.GetTask(ctx, task.ID); err != nil || got.Status != uhp.StatusCompleted {
		t.Errorf("status = %v (err %v), want completed", got.Status, err)
	}
}

// uncountableAdapter narrates no tool call and bounds nothing itself: the sixth
// harness somebody adds without probing it. Nothing registered today is this,
// which is why the double has to exist for the refusal to be testable at all.
type uncountableAdapter struct{ echoAdapter }

func (uncountableAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness: uhp.Harness{
			ID: "uncountable", Base: "uncountable", Object: "harness", Name: "Uncountable",
		},
		Status: uhpgo.HarnessReady,
	}
}

// Deliberately shadowing echoAdapter's, which claims a start edge.
func (uncountableAdapter) StepEdge() harness.StepEdge { return harness.StepEdgeNone }

// The unit half, and the one no end-to-end test can pin cheaply: nil is
// unbounded and zero is a ceiling, on every level of the resolution.
func TestResolveStepBudget(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		requested, configured *int
		ceiling               int
		want                  *int
	}{
		{"nothing set is unbounded", nil, nil, 0, nil},
		{"the request alone", stepp(5), nil, 0, stepp(5)},
		{"the harness alone", nil, stepp(7), 0, stepp(7)},
		{"the deployment alone", nil, nil, 9, stepp(9)},

		// Every level is a clamp and none is a preference, so the answer is the
		// shortest rather than the first — which is what stops a client
		// widening a bound its operator set.
		{"the shortest of three wins", stepp(8), stepp(3), 9, stepp(3)},
		{"a request cannot widen the harness's", stepp(50), stepp(4), 0, stepp(4)},
		{"a harness cannot widen the deployment's", nil, stepp(50), 6, stepp(6)},

		// Zero is a real ceiling at every level, and is the value most likely
		// to be mistaken for an absence.
		{"a request of zero is a ceiling, not an absence", stepp(0), nil, 0, stepp(0)},
		{"zero is the shortest there is", stepp(0), stepp(3), 9, stepp(0)},

		// A stored negative has nobody to tell — the configuration may predate
		// all of this — so it is skipped rather than read as "no tool calls",
		// which would turn an operator's typo into every task on that harness
		// answering without touching anything. A negative *request* never
		// arrives: the transport refuses it.
		{"a negative harness budget is skipped", nil, stepp(-1), 0, nil},
		{"a negative does not beat a real ceiling", nil, stepp(-1), 6, stepp(6)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStepBudget(tc.requested, tc.configured, tc.ceiling)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("resolved %d, want unbounded", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("resolved unbounded, want %d", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("resolved %d, want %d", *got, *tc.want)
			}
		})
	}
}

// Only the base that enforces its own ceiling is told the number. Telling the
// other four would give it a second home and no second effect, and a value
// carried but unused is the kind that quietly stops matching the one that is.
func TestOnlyANativeBaseIsHandedTheCeiling(t *testing.T) {
	for _, tc := range []struct {
		edge harness.StepEdge
		want int
	}{
		{harness.StepEdgeNative, 5},
		{harness.StepEdgeStart, 0},
		{harness.StepEdgeFinish, 0},
		{harness.StepEdgeNone, 0},
	} {
		t.Run(string(tc.edge), func(t *testing.T) {
			if got := nativeMaxStep(stepp(5), tc.edge); got != tc.want {
				t.Errorf("nativeMaxStep = %d, want %d", got, tc.want)
			}
		})
	}

	// Zero is not passed on even natively: the supervisor trips it on the first
	// call any base narrates, grok included, and `--max-turns 0` is not a
	// number grok has been shown to accept.
	if got := nativeMaxStep(stepp(0), harness.StepEdgeNative); got != 0 {
		t.Errorf("nativeMaxStep(0) = %d, want 0", got)
	}
	if got := nativeMaxStep(nil, harness.StepEdgeNative); got != 0 {
		t.Errorf("nativeMaxStep(nil) = %d, want 0", got)
	}
}

// nativeAdapter bounds its own steps and narrates none, which is grok. It is a
// double rather than the real adapter because what is under test is the
// router's arithmetic, and driving grok would test grok.
type nativeAdapter struct{ echoAdapter }

func (nativeAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness: uhp.Harness{
			ID: "native", Base: "native", Object: "harness", Name: "Self-bounding",
		},
		Status: uhpgo.HarnessReady,
	}
}

func (nativeAdapter) StepEdge() harness.StepEdge { return harness.StepEdgeNative }

// `max_step: 0` asks for a run that calls no tools, and only a base that
// announces a call *before* it runs can deliver that. The other two are refused
// rather than half-honoured, because "half a ceiling of zero" is one tool call —
// side effects and all — against a client that asked for none.
//
// Both rows are the same hole for different reasons, which is why this is a
// table and not `grok`'s special case.
func TestAZeroCeilingIsRefusedWhereACallCannotBeStoppedInTime(t *testing.T) {
	for _, tc := range []struct {
		name string
		edge harness.StepEdge
	}{
		// opencode. It narrates a call only once the call is over, so the first
		// one has already happened by the moment the router could act.
		{"a finish edge has already run the call by the time it says so", harness.StepEdgeFinish},
		// grok. Not counted here at all, so there is nothing to trip on — and
		// `--max-turns 0` is no run rather than a run that calls nothing.
		{"a self-bounding base has nothing to trip on", harness.StepEdgeNative},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newSteppingAdapter("bounded", tc.edge)
			svc := NewTaskService(newRegistryWith(a), newMemStore(), testLogger())
			ctx := context.Background()

			_, _, err := svc.StartTask(ctx, CreateTaskRequest{
				Input: "x", HarnessID: "bounded", MaxStep: stepp(0),
			})
			if !errors.Is(err, ErrStepBudgetUnsupported) {
				t.Fatalf("error = %v, want ErrStepBudgetUnsupported: a ceiling of zero that "+
					"still permits a call is a bound reported and not held", err)
			}
			if a.narrated() != 0 {
				t.Errorf("the refused task still made %d tool calls", a.narrated())
			}
		})
	}
}

// Only zero, and only on those two. Every positive ceiling is held on all four
// edges, so refusing those would take the field away from harnesses that
// support it.
func TestAPositiveCeilingIsAcceptedOnEveryBoundableBase(t *testing.T) {
	for _, edge := range []harness.StepEdge{
		harness.StepEdgeStart, harness.StepEdgeFinish, harness.StepEdgeNative,
	} {
		t.Run(string(edge), func(t *testing.T) {
			if err := requireStepBudget(stepp(5), edge, "h"); err != nil {
				t.Errorf("a ceiling of 5 was refused on a %q base: %v", edge, err)
			}
		})
	}
	// And an unbounded task is never refused anywhere, including on the base
	// that can hold nothing — a harness must not be taken out of service by a
	// field nobody used.
	for _, edge := range []harness.StepEdge{
		harness.StepEdgeNone, harness.StepEdgeStart, harness.StepEdgeFinish, harness.StepEdgeNative,
	} {
		if err := requireStepBudget(nil, edge, "h"); err != nil {
			t.Errorf("a task that asked for no ceiling was refused on a %q base: %v", edge, err)
		}
	}
}

// A permanent refusal must cost nothing. Everything below the check in
// StartTask does work that is never read again — a session row, a working
// directory, the caller's input files — and a client retrying a request that
// will be refused identically forever would leave a set of them behind on every
// attempt.
func TestARefusedCeilingLeavesNoSessionBehind(t *testing.T) {
	store := newMemStore()
	svc := NewTaskService(newRegistryWith(uncountableAdapter{}), store, testLogger())
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, _, err := svc.StartTask(ctx, CreateTaskRequest{
			Input: "x", HarnessID: "uncountable", MaxStep: stepp(5),
		}); err == nil {
			t.Fatalf("attempt %d was accepted on a base that cannot hold a ceiling", i)
		}
	}

	page, err := store.ListSessions(ctx, domain.SessionFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(page.Sessions) != 0 {
		t.Errorf("store holds %d sessions, want 0: five permanently-refused requests each "+
			"left one behind", len(page.Sessions))
	}
}

// The harness half of the same two refusals (#72). `maxStep` has been stored by
// `POST /v1/harnesses` since harness management existed and was read by nothing;
// now it bounds every task that runs on the harness, so a value that cannot be
// enforced has to be refused where the *operator* is listening rather than
// discovered later by a client that never sent the field.
func TestAHarnessCannotStoreACeilingItCannotHold(t *testing.T) {
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	svc := NewTaskService(newRegistryWith(uncountableAdapter{}, echoAdapter{}), newMemStore(),
		testLogger(), WithHarnessStore(hs))
	ctx := context.Background()

	t.Run("a negative is not a bound", func(t *testing.T) {
		_, err := svc.CreateHarness(ctx, HarnessSpec{Base: "echo", MaxStep: stepp(-1)})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput: a negative ceiling is meaningless, "+
				"and storing one would leave the startup log announcing a bound nothing holds", err)
		}
	})

	// Reported as `invalid_input`, not as the task path's
	// `uhpgo_step_budget_unsupported`: there a client's *task* named a ceiling
	// and the harness it chose cannot hold it, and here the harness itself is
	// what is wrong. `ErrInvalidInput` carries no `param`, so the message text
	// is the only pointer the operator gets and has to spell the field the way
	// the request body does.
	t.Run("a base that cannot hold it", func(t *testing.T) {
		_, err := svc.CreateHarness(ctx, HarnessSpec{Base: "uncountable", MaxStep: stepp(5)})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error = %v, want ErrInvalidInput: a harness stored this way carries a "+
				"bound this server would silently drop on every task that runs on it", err)
		}
		if !strings.Contains(err.Error(), "`max_step`") {
			t.Errorf("the refusal does not name the field as the request body spells it: %v", err)
		}
	})

	t.Run("a ceiling the base can hold is stored", func(t *testing.T) {
		h, err := svc.CreateHarness(ctx, HarnessSpec{Base: "echo", MaxStep: stepp(4)})
		if err != nil {
			t.Fatalf("CreateHarness: %v", err)
		}
		if h.MaxStep == nil || *h.MaxStep != 4 {
			t.Errorf("maxStep = %v, want 4", h.MaxStep)
		}
	})

	t.Run("and zero is one of them, on a base that can stop a call in time", func(t *testing.T) {
		if _, err := svc.CreateHarness(ctx, HarnessSpec{Base: "echo", MaxStep: stepp(0)}); err != nil {
			t.Errorf("CreateHarness with maxStep 0: %v — an operator saying `this harness "+
				"may not use tools` is a coherent configuration", err)
		}
	})
}

// A harness whose stored `maxStep` predates #72 must stay editable.
//
// PATCH rebuilds a whole spec from what the harness already is, so a value that
// would be refused on the way in flows back through the same validation on every
// later edit. Left unguarded, renaming such a harness is answered `400` about a
// field the client never sent — and the fix for the bad value is a PATCH, which
// is the request being refused. The rule is therefore on *changing* the ceiling,
// not on carrying one.
func TestALegacyCeilingDoesNotMakeAHarnessUneditable(t *testing.T) {
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	svc := NewTaskService(newRegistryWith(echoAdapter{}), newMemStore(), testLogger(),
		WithHarnessStore(hs))
	ctx := context.Background()

	// Written straight to the store, which is how such a row got there: through
	// a build where nothing validated the field because nothing read it.
	created, err := svc.CreateHarness(ctx, HarnessSpec{Base: "echo", Name: "Legacy"})
	if err != nil {
		t.Fatalf("CreateHarness: %v", err)
	}
	legacy := domain.HarnessConfig{
		ID: created.ID, Base: "echo", Name: "Legacy", MaxStep: stepp(-1),
		CreatedAt: created.CreatedAt,
	}
	if err := hs.PutHarness(ctx, legacy); err != nil {
		t.Fatalf("PutHarness: %v", err)
	}

	t.Run("an unrelated edit is not refused", func(t *testing.T) {
		got, err := svc.PatchHarness(ctx, created.ID, HarnessPatch{
			Name: Optional[string]{Set: true, Value: "Renamed"},
		})
		if err != nil {
			t.Fatalf("renaming was refused for a field the client never sent: %v", err)
		}
		if got.Name != "Renamed" {
			t.Errorf("name = %q, want %q", got.Name, "Renamed")
		}
	})

	t.Run("and clearing the bad value is the fix, not another refusal", func(t *testing.T) {
		got, err := svc.PatchHarness(ctx, created.ID, HarnessPatch{
			MaxStep: Optional[*int]{Set: true, Value: nil},
		})
		if err != nil {
			t.Fatalf("clearing an unenforceable ceiling was refused: %v", err)
		}
		if got.MaxStep != nil {
			t.Errorf("maxStep = %v, want nil", *got.MaxStep)
		}
	})

	t.Run("but replacing it with another bad one still is", func(t *testing.T) {
		if _, err := svc.PatchHarness(ctx, created.ID, HarnessPatch{
			MaxStep: Optional[*int]{Set: true, Value: stepp(-2)},
		}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("error = %v, want ErrInvalidInput: the guard is on carrying a legacy "+
				"value, never on writing a new bad one", err)
		}
	})
}
