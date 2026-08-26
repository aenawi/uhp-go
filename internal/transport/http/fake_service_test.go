package http

import (
	"context"
	"log/slog"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
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

	listHarnesses  func(context.Context) ([]uhpgo.Harness, error)
	getHarness     func(context.Context, string) (uhpgo.Harness, bool, error)
	modelAvailable func(context.Context, string, string) bool

	startTask  func(context.Context, service.CreateTaskRequest) (*domain.Task, *service.Run, error)
	getTask    func(context.Context, string) (*domain.Task, error)
	cancelTask func(context.Context, string) error

	filesEnabled             bool
	harnessManagementEnabled bool
	sessionSharingEnabled    bool

	listSessions  func(context.Context, domain.SessionFilter) (domain.SessionPage, error)
	getSession    func(context.Context, string) (*domain.Session, error)
	sessionTurns  func(context.Context, string) ([]uhp.Turn, error)
	cancelSession func(context.Context, string) error
	deleteSession func(context.Context, string) error
}

func (f *fakeService) ListHarnesses(ctx context.Context) ([]uhpgo.Harness, error) {
	return f.listHarnesses(ctx)
}

func (f *fakeService) GetHarness(ctx context.Context, id string) (uhpgo.Harness, bool, error) {
	return f.getHarness(ctx, id)
}

func (f *fakeService) ModelAvailable(ctx context.Context, harnessID, model string) bool {
	return f.modelAvailable(ctx, harnessID, model)
}

func (f *fakeService) StartTask(
	ctx context.Context, req service.CreateTaskRequest,
) (*domain.Task, *service.Run, error) {
	return f.startTask(ctx, req)
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

func (f *fakeService) SessionTurns(ctx context.Context, id string) ([]uhp.Turn, error) {
	return f.sessionTurns(ctx, id)
}

func (f *fakeService) CancelSession(ctx context.Context, id string) error {
	return f.cancelSession(ctx, id)
}

func (f *fakeService) DeleteSession(ctx context.Context, id string) error {
	return f.deleteSession(ctx, id)
}

// newFakeServer wires a server onto a stand-in service, with auth off so a test
// asserts on the endpoint rather than on the credential.
func newFakeServer(f *fakeService) *Server {
	return NewServer(f, slog.Default(), nil, 0)
}

// The three configuration answers the discovery document reports. They are
// plain fields rather than function hooks because there is nothing to observe
// about the call — the handler asks each one once and publishes the answer.
//
// Unlike everything else here, these three answer rather than panic when a test
// leaves them out, and their zero value is the deployment that offers least. A
// handler test that reaches one of them without setting it therefore gets
// "switched off" instead of a panic naming the method — so a 501 from a file
// endpoint on a bare fakeService is this default, not the behaviour under test.
func (f *fakeService) FilesEnabled() bool             { return f.filesEnabled }
func (f *fakeService) HarnessManagementEnabled() bool { return f.harnessManagementEnabled }
func (f *fakeService) SessionSharingEnabled() bool    { return f.sessionSharingEnabled }
