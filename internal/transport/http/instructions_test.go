package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// promptEchoAdapter answers with the prompt it was given, which is how a
// transport-level test can see what actually reached the harness. echoAdapter
// answers "ok" whatever it is asked, and would prove only that the request
// parsed.
type promptEchoAdapter struct{ echoAdapter }

func (promptEchoAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{
		Harness: uhp.Harness{ID: "chrn_prompt", Base: "prompt", Name: "Prompt", Object: "harness"},
		Status:  uhpgo.HarnessReady,
	}
}

func (promptEchoAdapter) Run(_ context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate, 2)
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: req.Input}
	ch <- harness.RunUpdate{Type: harness.UpdateCompleted}
	close(ch)
	return ch, nil
}

func newPromptServer(t *testing.T) *Server {
	t.Helper()
	reg := harness.NewRegistry()
	reg.Register(promptEchoAdapter{})
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	return NewServer(svc, slog.Default(), nil, 0)
}

// answerTo posts a body against srv and returns the assistant text.
func answerTo(t *testing.T, srv *Server, body string) string {
	t.Helper()
	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(body))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp uhp.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var b strings.Builder
	for _, item := range resp.Output {
		for _, part := range item.Content {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// The seventh of thirteen schema properties this server reads. Issue #48
// recorded it as accepted and dropped; #80 is where it stopped being.
func TestInstructionsSurviveTheTransport(t *testing.T) {
	got := answerTo(t, newPromptServer(t),
		`{"input":"the work","instructions":"answer in French","metadata":{"harness_id":"chrn_prompt"}}`)
	if !strings.Contains(got, "answer in French") {
		t.Errorf("the prompt does not carry `instructions`:\n%s", got)
	}
	if !strings.Contains(got, "the work") {
		t.Errorf("the prompt does not carry the input:\n%s", got)
	}
}

// An empty string and an absent field mean the same thing, and neither should
// leave the prompt opening with blank lines.
func TestAnEmptyInstructionsFieldAddsNothingToThePrompt(t *testing.T) {
	srv := newPromptServer(t)
	with := answerTo(t, srv, `{"input":"the work","instructions":"","metadata":{"harness_id":"chrn_prompt"}}`)
	without := answerTo(t, srv, `{"input":"the work","metadata":{"harness_id":"chrn_prompt"}}`)
	if with != without {
		t.Errorf("an empty `instructions` changed the prompt:\n%q\n%q", with, without)
	}
	if with != "the work" {
		t.Errorf("prompt = %q, want %q", with, "the work")
	}
}

// The schema's response object has no `instructions` field, so a client cannot
// read its own guidance back off the response — and nothing here should invent
// a place for it.
func TestInstructionsAreNotEchoedOnTheResponse(t *testing.T) {
	w := do(t, newPromptServer(t), "POST", "/v1/responses",
		strings.NewReader(`{"input":"x","instructions":"secret guidance","metadata":{"harness_id":"chrn_prompt"}}`))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["instructions"]; present {
		t.Error("the response object carries an `instructions` key the schema does not define")
	}
	meta, _ := raw["metadata"].(map[string]any)
	for k, v := range meta {
		if s, ok := v.(string); ok && strings.Contains(s, "secret guidance") {
			t.Errorf("metadata[%q] echoes the task's instructions", k)
		}
	}
}
