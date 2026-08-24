package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
)

// errDisk is what a store hands back when it cannot answer at all — not a row
// that is absent, but a read that did not happen.
var errDisk = errors.New("disk gone")

// unreadableStore is a Store whose two single-row reads fail. Everything else
// is a real MemoryStore, so a service built on it is otherwise ordinary and the
// test is about one failure rather than a cripple.
//
// It has to be a stand-in: MemoryStore cannot fail, and a SQLite file that can
// would have to be corrupted mid-test to do it.
type unreadableStore struct {
	Store
}

func (unreadableStore) GetTask(context.Context, string) (*domain.Task, bool, error) {
	return nil, false, errDisk
}

func (unreadableStore) GetSession(context.Context, string) (*domain.Session, bool, error) {
	return nil, false, errDisk
}

func unreadableService() *TaskService {
	return NewTaskService(
		harness.NewRegistry(),
		unreadableStore{Store: store.NewMemoryStore()},
		slog.Default(),
	)
}

// A store that could not be read is ErrStorage, never a not-found.
//
// The two answers are the retry signal the transport turns into a status class,
// and they say opposite things: ErrStorage becomes a 500 that invites a retry,
// the not-found sentinels become a 404 that forbids one. Reporting a failed read
// as a missing row tells a client polling for a task that is still running that
// its task has vanished, while the supervisor carries on running it.
//
// Asserting the absence as well as the presence is the point. `errors.Is` on
// ErrStorage alone would pass for an error that wrapped both, and both is what
// the transport cannot act on.
func TestReadFailuresAreStorageNotNotFound(t *testing.T) {
	ctx := context.Background()
	svc := unreadableService()

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"GetTask", func() error { _, err := svc.GetTask(ctx, "resp_1"); return err }},
		{"CancelTask", func() error { return svc.CancelTask(ctx, "resp_1") }},
		{"GetSession", func() error { _, err := svc.GetSession(ctx, "sess_1"); return err }},
		{"SessionTurns", func() error { _, err := svc.SessionTurns(ctx, "sess_1"); return err }},
		{"CancelSession", func() error { return svc.CancelSession(ctx, "sess_1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("a read that failed must not succeed")
			}
			if !errors.Is(err, ErrStorage) {
				t.Errorf("error is not ErrStorage: %v", err)
			}
			if errors.Is(err, ErrResponseNotFound) || errors.Is(err, ErrSessionNotFound) {
				t.Errorf("a failed read is reported as a missing row: %v", err)
			}
			if !errors.Is(err, errDisk) {
				t.Errorf("the store's own error was dropped: %v", err)
			}
		})
	}
}

// The other half of the same rule, and the half that has to keep working: a row
// that genuinely is not there is still a not-found, not a storage failure. A
// fix that answered ErrStorage for everything would trade one wrong status for
// another.
func TestGenuineMissesAreStillNotFound(t *testing.T) {
	ctx := context.Background()
	svc := NewTaskService(harness.NewRegistry(), store.NewMemoryStore(), slog.Default())

	for _, tc := range []struct {
		name string
		want error
		call func() error
	}{
		{"GetTask", ErrResponseNotFound, func() error { _, err := svc.GetTask(ctx, "resp_missing"); return err }},
		{"CancelTask", ErrResponseNotFound, func() error { return svc.CancelTask(ctx, "resp_missing") }},
		{"GetSession", ErrSessionNotFound, func() error { _, err := svc.GetSession(ctx, "sess_missing"); return err }},
		{"SessionTurns", ErrSessionNotFound, func() error { _, err := svc.SessionTurns(ctx, "sess_missing"); return err }},
		{"CancelSession", ErrSessionNotFound, func() error { return svc.CancelSession(ctx, "sess_missing") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, tc.want) {
				t.Errorf("error is not %v: %v", tc.want, err)
			}
			if errors.Is(err, ErrStorage) {
				t.Errorf("a missing row is reported as a storage failure: %v", err)
			}
		})
	}
}

// ListSessions and ListSessionTasks fail as a whole rather than per row, so
// neither has a not-found to be confused with — but an unwrapped error reaches
// the transport's default arm and becomes 502 harness_unavailable, which blames
// a harness for a disk. They belong to the same rule and are checked here
// because only a stand-in can make either fail.
func TestListFailuresAreStorage(t *testing.T) {
	ctx := context.Background()
	svc := NewTaskService(harness.NewRegistry(), unlistableStore{store.NewMemoryStore()}, slog.Default())

	if _, err := svc.ListSessions(ctx, domain.SessionFilter{}); !errors.Is(err, ErrStorage) {
		t.Errorf("ListSessions: error is not ErrStorage: %v", err)
	}
	// The session read has to succeed for the listing beneath it to be the
	// failure under test, so this store keeps MemoryStore's GetSession and a
	// session is seeded for it to find.
	if err := svc.store.CreateSession(ctx, &domain.Session{Session: uhp.Session{ID: "sess_1"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.SessionTurns(ctx, "sess_1"); !errors.Is(err, ErrStorage) {
		t.Errorf("SessionTurns: error is not ErrStorage: %v", err)
	}
}

type unlistableStore struct {
	Store
}

func (unlistableStore) ListSessions(context.Context, domain.SessionFilter) (domain.SessionPage, error) {
	return domain.SessionPage{}, errDisk
}

func (unlistableStore) ListSessionTasks(context.Context, string) ([]*domain.Task, error) {
	return nil, errDisk
}
