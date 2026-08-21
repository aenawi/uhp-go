package http

import (
	"context"
	"log/slog"

	"github.com/aenawi/uhp-go/internal/domain"
)

// fakeService is a TaskService whose behaviour a test sets one method at a
// time. It is what the interface in deps.go bought: the endpoints that only
// read — sessions, models, retrieve, cancel — no longer need a registry, a
// store and a real service standing behind them to be asked a question about
// status codes and JSON shape.
//
// TaskService is embedded rather than implemented in full, so the struct
// satisfies the interface while a test names only the methods its endpoint
// touches. The embedded value is nil, so a handler that reaches for anything
// else panics with a nil-pointer dereference naming that method — which is the
// answer a test wants, rather than a zero value it then asserts on and believes.
type fakeService struct {
	TaskService

	listHarnesses  func(context.Context) ([]domain.Harness, error)
	getHarness     func(context.Context, string) (domain.Harness, bool, error)
	modelAvailable func(context.Context, string, string) bool

	getTask    func(context.Context, string) (*domain.Task, error)
	cancelTask func(context.Context, string) error

	listSessions  func(context.Context, domain.SessionFilter) (domain.SessionPage, error)
	getSession    func(context.Context, string) (*domain.Session, error)
	sessionTurns  func(context.Context, string) ([]domain.Turn, error)
	cancelSession func(context.Context, string) error
}

func (f *fakeService) ListHarnesses(ctx context.Context) ([]domain.Harness, error) {
	return f.listHarnesses(ctx)
}

func (f *fakeService) GetHarness(ctx context.Context, id string) (domain.Harness, bool, error) {
	return f.getHarness(ctx, id)
}

func (f *fakeService) ModelAvailable(ctx context.Context, harnessID, model string) bool {
	return f.modelAvailable(ctx, harnessID, model)
}

func (f *fakeService) GetTask(ctx context.Context, id string) (*domain.Task, error) {
	return f.getTask(ctx, id)
}

func (f *fakeService) CancelTask(ctx context.Context, id string) error {
	return f.cancelTask(ctx, id)
}

func (f *fakeService) ListSessions(
	ctx context.Context, filter domain.SessionFilter,
) (domain.SessionPage, error) {
	return f.listSessions(ctx, filter)
}

func (f *fakeService) GetSession(ctx context.Context, id string) (*domain.Session, error) {
	return f.getSession(ctx, id)
}

func (f *fakeService) SessionTurns(ctx context.Context, id string) ([]domain.Turn, error) {
	return f.sessionTurns(ctx, id)
}

func (f *fakeService) CancelSession(ctx context.Context, id string) error {
	return f.cancelSession(ctx, id)
}

// newFakeServer wires a server onto a stand-in service, with auth off so a test
// asserts on the endpoint rather than on the credential.
func newFakeServer(f *fakeService) *Server {
	return NewServer(f, slog.Default(), nil, 0)
}
