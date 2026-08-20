package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
)

type echoAdapter struct{}

func (echoAdapter) Info() domain.Harness {
	return domain.Harness{ID: "chrn_echo", Base: "echo", Name: "Echo", Object: "harness"}
}
func (echoAdapter) HealthCheck(ctx context.Context) error { return nil }
func (echoAdapter) Run(ctx context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	ch := make(chan harness.RunUpdate, 2)
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "ok"}
	ch <- harness.RunUpdate{Type: harness.UpdateCompleted}
	close(ch)
	return ch, nil
}
func (echoAdapter) Cancel(ctx context.Context, taskID string) error { return nil }

func newTestServer() *Server {
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	memStore := store.NewMemoryStore()
	svc := service.NewTaskService(reg, memStore, slog.Default())
	return NewServer(svc, slog.Default(), nil, 0)
}

func TestCreateTaskNonStreaming(t *testing.T) {
	srv := newTestServer()
	body := `{"input":"hi","model":"m","metadata":{"harness_id":"echo"}}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var task domain.Task
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if task.Text() != "ok" {
		t.Fatalf("expected output 'ok', got %q", task.Text())
	}
	if task.Status != domain.StatusCompleted {
		t.Fatalf("expected completed status, got %s", task.Status)
	}
}

func TestListHarnesses(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest("GET", "/v1/harnesses", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestCreateTaskMissingHarness(t *testing.T) {
	srv := newTestServer()
	body := `{"input":"hi"}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
