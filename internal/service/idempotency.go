package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
)

// IdempotencyRetention is how long a key is remembered once the run it
// started is over.
//
// Tasks §6 asks for at least 24 hours, and the reason is the one it states
// plainly: "Agent tasks are expensive and side-effecting: a retry that runs the
// work twice is worse than a slow answer." A window shorter than the interval
// over which a client gives up and retries is no protection at all, and the
// specification names the interval, so this is not a number to tune.
const IdempotencyRetention = 24 * time.Hour

// errStartAbandoned is what a waiter is told when the first request neither
// started a run nor said why.
//
// In practice that means the first request panicked. It cannot be left as a
// nil task and a nil error, because every request waiting on the key would
// then dereference the nil rather than report the failure — one request's bug
// becoming every retry's crash.
var errStartAbandoned = errors.New("service: the first request for this idempotency key did not finish starting")

// firstStart is one key's claim: the outcome of the request that got there
// first, and the channel every later request waits on.
//
// The outcome — task, run, err — is written once, before ready is closed, and
// read only after it is closed. That ordering is the entire synchronisation:
// closing a channel happens-before a receive from it, so a waiter can never
// observe a half-filled outcome and no lock is held while a run is in flight.
// `expiresAt` is the exception and is guarded by the registry's lock instead,
// because the retention sweep is the only thing that reads or writes it.
type firstStart struct {
	key   string
	ready chan struct{}

	task *domain.Task
	run  *Run
	err  error

	// expiresAt is when this claim may be forgotten. It stays zero until the
	// answer exists, and only the retention sweep writes it, under the
	// registry's lock.
	expiresAt time.Time
}

// await blocks until the first request has settled and then returns exactly
// what it returned.
//
// Waiting is the specified behaviour, not a convenience: Tasks §6 requires a
// server to wait for a first request that is still running "rather than
// returning a partial or a conflict". The wait itself costs nothing — the run
// is supervised whether or not anyone is listening, and abandoning this ctx
// abandons only the wait.
func (f *firstStart) await(ctx context.Context) (*domain.Task, *Run, error) {
	select {
	case <-f.ready:
		return f.task, f.run, f.err
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// settled reports whether the first request has finished starting.
func (f *firstStart) settled() bool {
	select {
	case <-f.ready:
		return true
	default:
		return false
	}
}

// answered reports whether the result this claim promises actually exists yet.
//
// The distinction from settled() is the one the retention window turns on. A
// claim settles the moment the run is handed to the supervisor, which is the
// *start* of the work; the answer a retry came for does not exist until that
// run is terminal, which for an agent task can be a day later.
func (f *firstStart) answered() bool {
	if !f.settled() {
		return false
	}
	// Safe without a lock: settled() returning true is a receive from a closed
	// channel, which happens-after the write of every other field.
	return f.run == nil || f.run.terminated()
}

// idempotencyKeys maps an Idempotency-Key to the run its first request started
// (Tasks §6).
//
// It holds the *Run, not just the task id, and that is the design decision
// worth stating. A run retains its whole event log, so handing a retry the
// original Run makes the replay identical to the original in both directions:
// the non-streaming path waits on a run that is already terminal and reads the
// same stored task, and the streaming path replays the same events with the
// same sequence numbers. Nothing above this has to know a replay happened, and
// there is no second code path for "reconstruct a stream from storage" to
// disagree with the first one.
//
// The cost is that a finished run's events stay in memory for the retention
// window rather than being collected when the last subscriber leaves. That is
// paid deliberately: the alternative is a synthesised stream that is a
// different answer from the one the first request got, which is precisely what
// §6 forbids.
//
// Keys live in memory and do not survive a restart, so a retry that arrives
// after one runs the work again. That used to be moot, because the responses
// a durable key index would point at were in memory too; with a database
// configured they are not, and this is now the weaker half. Moving the index
// into store.Store is its own change — it needs a retention sweep that runs
// against SQL rather than a map, and a decision about what a key means to a
// second process reading the same file.
type idempotencyKeys struct {
	mu    sync.Mutex
	byKey map[string]*firstStart

	retention time.Duration
	// clock is a field so a retention test does not have to run for a day.
	clock func() time.Time
}

func newIdempotencyKeys() *idempotencyKeys {
	return &idempotencyKeys{
		byKey:     make(map[string]*firstStart),
		retention: IdempotencyRetention,
		clock:     time.Now,
	}
}

// claim reserves a key, reporting whether this caller is the one that got it.
//
// A caller told `false` must not start anything; it awaits the returned claim.
// The reservation is taken before any work begins rather than recorded after
// it, because two retries can arrive at the same instant — that is what a
// client-side timeout produces — and a record written afterwards would already
// be too late to stop the second execution.
func (k *idempotencyKeys) claim(key string) (*firstStart, bool) {
	now := k.clock()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.evictExpired(now)
	if existing, ok := k.byKey[key]; ok {
		return existing, false
	}
	claimed := &firstStart{key: key, ready: make(chan struct{})}
	k.byKey[key] = claimed
	return claimed, true
}

// known reports whether a key already names a task this server started.
//
// It answers a question `claim` cannot be asked without also taking the key:
// whether a stream exists to resume. A caller carrying a resume point for a key
// nobody has used is holding a resume point for a stream that does not exist,
// and starting one and skipping into it would silently discard its opening
// events.
// It sweeps first, exactly as claim does, because the answer has to be the one
// claim would give a moment later. A key that is expired but not yet swept
// would otherwise be reported as known, and the claim behind it would sweep it
// and start a fresh task — which is the very case the caller asked about.
func (k *idempotencyKeys) known(key string) bool {
	now := k.clock()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.evictExpired(now)
	_, ok := k.byKey[key]
	return ok
}

// settle records what the first request produced and wakes everything waiting
// on it.
//
// A start that produced no run gives the key back. Nothing was executed, so
// there is no second execution to prevent, and the failures that get here are
// mostly the retryable ones — no capacity, a store that would not write. Errors
// §4 tells a client to retry those, and to carry the same Idempotency-Key when
// it does; a key bound to the refusal would answer that retry with the same
// refusal until the key expired.
//
// Requests already blocked on the key are still handed that refusal, rather
// than being promoted to try again themselves. Concurrent requests sharing one
// key are retries of the same request, so a refusal that is about the request
// is theirs too, and one that is about the server — no capacity — is a truthful
// answer to all of them. Either way the key is free again, so their next retry
// is a real attempt.
func (k *idempotencyKeys) settle(claimed *firstStart, task *domain.Task, run *Run, err error) {
	if run == nil {
		if err == nil {
			task, err = nil, errStartAbandoned
		}
		k.mu.Lock()
		// Compared, not just deleted: the key may already have been released and
		// re-claimed by a later request, and deleting blind would drop that one's
		// claim and let a third request start a second execution.
		if current, ok := k.byKey[claimed.key]; ok && current == claimed {
			delete(k.byKey, claimed.key)
		}
		k.mu.Unlock()
	}

	claimed.task, claimed.run, claimed.err = task, run, err
	close(claimed.ready)
}

// evictExpired drops keys whose retention window has passed. The caller holds
// the lock.
//
// The window runs from the answer, not from the request, and that is the whole
// subtlety here. An agent task can work for longer than a day. Dating the key
// from the request instead would have the retry that finally came to collect
// the result find its own key expired and start the work a second time — the
// exact failure §6 exists to prevent, arriving at the worst possible moment.
// So a claim is dated the first time this sweep sees its run terminal, and
// then lives a full window from there.
//
// Nothing sweeps on a timer, so a key is only ever forgotten by a later keyed
// request. An idle server keeps what it has, including the event log each
// retained run holds onto — the price of replaying a retry identically rather
// than reconstructing something close to it.
func (k *idempotencyKeys) evictExpired(now time.Time) {
	for key, claimed := range k.byKey {
		switch {
		case !claimed.answered():
			// Still working. However old the claim is, it is the only thing
			// stopping a retry from starting a second run alongside this one.
		case claimed.expiresAt.IsZero():
			claimed.expiresAt = now.Add(k.retention)
		case !now.Before(claimed.expiresAt):
			delete(k.byKey, key)
		}
	}
}
