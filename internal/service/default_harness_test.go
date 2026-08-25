package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// unavailableAdapter is a harness that exists and cannot run — the state a
// configured CLI is in before anybody logs in to it.
type unavailableAdapter struct{ echoAdapter }

func (unavailableAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness: uhp.Harness{
			ID: "chrn_sleeping", Base: "sleeping", Object: "harness", Name: "Sleeping",
		},
		Status: uhpgo.HarnessUnavailable,
	}
}

func serviceWith(adapters ...harness.Adapter) *TaskService {
	reg := harness.NewRegistry()
	for _, a := range adapters {
		reg.Register(a)
	}
	return NewTaskService(reg, store.NewMemoryStore(), slog.Default())
}

// Tasks §1.2: "If `harness_id` is absent, the server MUST use a default harness
// and MUST report which one it used in the response `metadata`."
//
// This is the request the specification most encourages a client to send, and
// it used to be answered `400 invalid_input` (issue #53).
func TestATaskNamingNoHarnessRunsOnTheOnlyReadyOne(t *testing.T) {
	svc := serviceWith(echoAdapter{})

	task, run, err := svc.StartTask(context.Background(), CreateTaskRequest{Input: "hi"})
	if err != nil {
		t.Fatalf("a task naming no harness was refused: %v", err)
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}

	if task.HarnessID != "chrn_echo" {
		t.Fatalf("HarnessID = %q, want chrn_echo", task.HarnessID)
	}
	// The second half of the MUST, and the half that is easy to miss: choosing
	// a harness silently is no better than refusing, because the client cannot
	// tell what ran.
	if got := task.Metadata["harness_id"]; got != "chrn_echo" {
		t.Fatalf("metadata.harness_id = %v, want chrn_echo — the server chose a harness and did not report it", got)
	}
}

// "Sole" means sole among the *ready* ones. A harness that is configured but
// not logged in is not a candidate, and counting it would turn a server that
// can serve exactly one task into one that refuses every task.
func TestAnUnavailableHarnessDoesNotMakeTheDefaultAmbiguous(t *testing.T) {
	svc := serviceWith(echoAdapter{}, unavailableAdapter{})

	got, err := svc.DefaultHarness(context.Background())
	if err != nil {
		t.Fatalf("DefaultHarness: %v", err)
	}
	if got != "chrn_echo" {
		t.Fatalf("DefaultHarness = %q, want chrn_echo", got)
	}
}

// Two ready harnesses is the case where there is no honest answer. Guessing
// would run an agent the caller did not ask for and bill them for it, so the
// refusal is correct — but it has to carry the list, because the client is
// being refused for omitting a field the specification let it omit.
func TestTwoReadyHarnessesRefuseWithTheListToChooseFrom(t *testing.T) {
	svc := serviceWith(echoAdapter{}, secondEcho{})

	_, _, err := svc.StartTask(context.Background(), CreateTaskRequest{Input: "hi"})
	var noDefault *NoDefaultHarnessError
	if !errors.As(err, &noDefault) {
		t.Fatalf("expected a NoDefaultHarnessError, got %v", err)
	}
	if len(noDefault.Candidates) != 2 {
		t.Fatalf("Candidates = %v, want both harnesses so the client can choose", noDefault.Candidates)
	}
}

// secondEcho is a second ready harness, distinct only in id.
type secondEcho struct{ echoAdapter }

func (secondEcho) Info() uhpgo.Harness {
	info := echoAdapter{}.Info()
	info.ID = "chrn_echo2"
	info.Base = "echo2"
	return info
}

// A configured default wins over the inference, including over an inference
// that would have succeeded. An operator who named one has answered the
// question, and nothing should be able to overrule that answer.
func TestAConfiguredDefaultWinsOverTheSoleReadyHarness(t *testing.T) {
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	reg.Register(secondEcho{})
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		WithDefaultHarness("chrn_echo2"))

	got, err := svc.DefaultHarness(context.Background())
	if err != nil {
		t.Fatalf("DefaultHarness: %v", err)
	}
	if got != "chrn_echo2" {
		t.Fatalf("DefaultHarness = %q, want the configured chrn_echo2", got)
	}
}

// A named harness is still honoured. The default path must not become the only
// path — this is the regression that would make every task run on one harness.
func TestANamedHarnessIsUnaffectedByTheDefault(t *testing.T) {
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	reg.Register(secondEcho{})
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		WithDefaultHarness("chrn_echo2"))

	task, run, err := svc.StartTask(context.Background(), CreateTaskRequest{
		Input: "hi", HarnessID: "chrn_echo",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if task.HarnessID != "chrn_echo" {
		t.Fatalf("HarnessID = %q, want the requested chrn_echo", task.HarnessID)
	}
}
