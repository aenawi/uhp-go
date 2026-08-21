package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
)

// errEnough stops a subscription that has read as much as its test wanted. A
// feed never reaches a terminal state, so a reader has to end itself.
var errEnough = errors.New("read enough")

// take reads n events from a feed starting at `from`, or fails the test.
func take(t *testing.T, f *Feed, from, n int) []domain.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var evs []domain.Event
	err := f.Events(ctx, from, IdleTick{}, func(ev domain.Event) error {
		evs = append(evs, ev)
		if len(evs) == n {
			return errEnough
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		t.Fatalf("Feed.Events: %v", err)
	}
	if len(evs) != n {
		t.Fatalf("read %d events from %d, want %d", len(evs), from, n)
	}
	return evs
}

// runTask starts a task and waits for it, returning the finished task's id.
func runTask(t *testing.T, svc *TaskService, req CreateTaskRequest) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	task, run, err := svc.StartTask(ctx, req)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return task.ID
}

// Issue #8 / UHP Streaming §5. A dropped connection already does not abort the
// task, but until there was a feed the only way to find out what happened next
// was to poll GET /v1/responses/{id}. The feed is how a client follows a
// harness rather than a request.
func TestHarnessFeedCarriesEveryRunOnThatHarness(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	first := runTask(t, svc, CreateTaskRequest{Input: "one", HarnessID: "echo"})
	second := runTask(t, svc, CreateTaskRequest{Input: "two", HarnessID: "echo"})

	feed, ok, err := svc.HarnessFeed(ctx, "echo")
	if err != nil || !ok {
		t.Fatalf("HarnessFeed: ok=%v err=%v", ok, err)
	}

	evs := take(t, feed, 0, 16)
	for i, ev := range evs {
		if ev.Seq != i {
			t.Fatalf("event %d has sequence_number %d; a feed numbers its own stream", i, ev.Seq)
		}
		want := first
		if i >= 8 {
			want = second
		}
		if ev.ResponseID != want {
			t.Fatalf("event %d belongs to response %q, want %q", i, ev.ResponseID, want)
		}
		if ev.SessionID == "" {
			t.Fatalf("event %d carries no session id; a multiplexed feed cannot be grouped without one", i)
		}
	}
}

// A feed is scoped to one harness. Two harnesses sharing a feed would hand a
// client work it did not ask to watch, and there is no way to filter it back
// out at the client.
func TestHarnessFeedExcludesOtherHarnesses(t *testing.T) {
	svc := NewTaskService(newRegistryWith(echoAdapter{}, otherAdapter{}), newMemStore(), testLogger())
	ctx := context.Background()

	mine := runTask(t, svc, CreateTaskRequest{Input: "mine", HarnessID: "echo"})
	runTask(t, svc, CreateTaskRequest{Input: "theirs", HarnessID: "other"})

	feed, ok, err := svc.HarnessFeed(ctx, "echo")
	if err != nil || !ok {
		t.Fatalf("HarnessFeed: ok=%v err=%v", ok, err)
	}
	for _, ev := range take(t, feed, 0, 8) {
		if ev.ResponseID != mine {
			t.Fatalf("feed carried response %q from another harness", ev.ResponseID)
		}
	}
}

// The feed outlives the runs on it. A feed that ended with its last task would
// close the moment a harness went idle, which is exactly when a client is
// waiting for the next one to start.
func TestHarnessFeedStaysOpenWhenTheHarnessGoesIdle(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	runTask(t, svc, CreateTaskRequest{Input: "one", HarnessID: "echo"})

	feed, _, err := svc.HarnessFeed(context.Background(), "echo")
	if err != nil {
		t.Fatalf("HarnessFeed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = feed.Events(ctx, 8, IdleTick{}, func(domain.Event) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Events = %v, want the subscription to still be waiting", err)
	}
}

// Issue #8: resumption MUST start at the event after the given sequence number
// and MUST NOT replay what the client already saw.
func TestHarnessFeedResumesAfterTheEventTheClientSaw(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	runTask(t, svc, CreateTaskRequest{Input: "one", HarnessID: "echo"})
	second := runTask(t, svc, CreateTaskRequest{Input: "two", HarnessID: "echo"})

	feed, _, err := svc.HarnessFeed(ctx, "echo")
	if err != nil {
		t.Fatalf("HarnessFeed: %v", err)
	}

	evs := take(t, feed, 8, 8)
	if evs[0].Seq != 8 {
		t.Fatalf("resumed at %d, want 8", evs[0].Seq)
	}
	for _, ev := range evs {
		if ev.ResponseID != second {
			t.Fatalf("resumption replayed response %q the client already had", ev.ResponseID)
		}
	}
}

// The same rule on a task's own stream. This is the one the issue calls mostly
// plumbing: the log was already retained and replayed from zero, so a resume
// point is a starting sequence number rather than a new mechanism.
func TestRunResumesAfterTheEventTheClientSaw(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	_, run, err := svc.StartTask(ctx, CreateTaskRequest{Input: "world", HarnessID: "echo"})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	all := collect(t, run)
	if len(all) < 4 {
		t.Fatalf("run produced %d events, too few to resume within", len(all))
	}

	var got []domain.Event
	if err := run.Events(ctx, 3, IdleTick{}, func(ev domain.Event) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != len(all)-3 {
		t.Fatalf("resumed stream has %d events, want %d", len(got), len(all)-3)
	}
	if got[0].Seq != 3 {
		t.Fatalf("resumed at %d, want 3", got[0].Seq)
	}
}

// A feed outlives every task on its harness, so it cannot retain everything.
// What it drops it must admit to dropping: a resumption answered with a
// silently later event is a gap the client has no way to notice.
func TestFeedRefusesAResumePointItHasDiscarded(t *testing.T) {
	f := newFeed(4)
	for i := 0; i < 10; i++ {
		f.publish(domain.Event{Type: "response.output_text.delta"}, "resp_x", "sess_x")
	}

	if f.Oldest() == 0 {
		t.Fatal("a feed past its retention still reports sequence 0 as retained")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := f.Events(ctx, 0, IdleTick{}, func(domain.Event) error { return nil })

	var gap *EventGapError
	if !errors.As(err, &gap) {
		t.Fatalf("Events = %v, want an EventGapError", err)
	}
	if gap.From != 0 || gap.Oldest != f.Oldest() {
		t.Fatalf("gap = %+v, want From 0 and Oldest %d", gap, f.Oldest())
	}
}

// The floor is a floor. `retain` is what the docs and the endpoint promise a
// client, so the log has to still hold that many after an eviction, not on its
// way back up to it.
func TestFeedKeepsAtLeastItsRetentionFloor(t *testing.T) {
	const retain = 16
	f := newFeed(retain)
	for i := 0; i < 200; i++ {
		f.publish(domain.Event{Type: "response.output_text.delta"}, "resp_x", "sess_x")
	}

	newest := take(t, f, f.Oldest(), 1)[0].Seq
	if available := 200 - f.Oldest(); available < retain {
		t.Fatalf("feed retains %d events (oldest %d, newest at least %d), want at least %d",
			available, f.Oldest(), newest, retain)
	}
}

// A subscriber can also fall out of the window while it is reading, if it is
// slower than the harness is busy. Told, not silently skipped: the events it
// is handed next would otherwise jump, and nothing on the wire would say so.
func TestFeedTellsASubscriberThatFellBehind(t *testing.T) {
	f := newFeed(4)
	for i := 0; i < 3; i++ {
		f.publish(domain.Event{Type: "response.output_text.delta"}, "resp_x", "sess_x")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The first event is delivered, and then the log runs away underneath this
	// subscriber before it asks for the second.
	err := f.Events(ctx, 0, IdleTick{}, func(domain.Event) error {
		for i := 0; i < 40; i++ {
			f.publish(domain.Event{Type: "response.output_text.delta"}, "resp_x", "sess_x")
		}
		return nil
	})

	var gap *EventGapError
	if !errors.As(err, &gap) {
		t.Fatalf("Events = %v, want an EventGapError once the reader fell behind", err)
	}
}

// Issue #8: a `Last-Event-ID` is only honoured against a stream this server
// still remembers, and an expired key remembers nothing. Reporting one as
// resumable lets the request behind it start a fresh task and then skip into
// it, swallowing the events the client has never seen — the exact failure the
// guard exists to prevent, arriving through the door it left open.
func TestAnExpiredKeyResumesNothing(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})

	now := time.Now().UTC()
	svc.idempotency.clock = func() time.Time { return now }

	runTask(t, svc, CreateTaskRequest{Input: "x", HarnessID: "echo", IdempotencyKey: "k1"})
	if !svc.ResumableStream("k1") {
		t.Fatal("a key whose run just finished does not resume its own stream")
	}

	now = now.Add(4 * IdempotencyRetention)
	if svc.ResumableStream("k1") {
		t.Fatal("an expired key still reports a stream to resume; the next request starts a new task instead")
	}
	if svc.ResumableStream("") {
		t.Fatal("an absent key reports a stream to resume")
	}
}

// The race the tombstone exists for: a subscription that has already checked
// its harness exists asks for the feed just after a delete removed it. Minting
// a fresh, open feed there would leave it waiting on a harness that is gone.
func TestAFeedAskedForAfterDeletionIsAlreadyClosed(t *testing.T) {
	s := newSupervisor(0)

	// A feed with a window's worth of events in it, so that what the closed
	// entry keeps is visible.
	before := s.feed("chrn_gone")
	for i := 0; i < 20; i++ {
		before.publish(domain.Event{Type: "response.output_text.delta"}, "resp_x", "sess_x")
	}
	s.closeFeed("chrn_gone")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	after := s.feed("chrn_gone")
	var seen int
	if err := s.feed("chrn_gone").Events(ctx, 0, IdleTick{}, func(domain.Event) error {
		seen++
		return nil
	}); err != nil {
		t.Fatalf("Events = %v, want a clean end on a deleted harness", err)
	}
	// The marker holds nothing. A retained window pins a whole task per
	// `response.created` in it, and a marker saying "this harness is gone"
	// needs to remember none of that.
	if seen != 0 || after.Head() != 0 {
		t.Fatalf("the closed entry replayed %d events and sits at head %d, want an empty marker",
			seen, after.Head())
	}
}

// The subscribers already reading the real feed when it is deleted are ended
// too, not just the ones that arrive afterwards.
func TestDeletionEndsSubscribersAlreadyOnTheFeed(t *testing.T) {
	s := newSupervisor(0)
	feed := s.feed("chrn_gone")

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done <- feed.Events(ctx, 0, IdleTick{}, func(domain.Event) error { return nil })
	}()

	s.closeFeed("chrn_gone")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("subscriber ended with %v, want a clean end", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a subscriber already on the feed was never told the harness was deleted")
	}
}

// A subscriber that names no starting point is served from wherever the log
// is, resolved inside the subscription. Reading the position first and passing
// it as a number leaves a window in which an eviction turns a fresh subscriber
// into a gap refusal — a client turned away because the harness was busy while
// it was connecting.
func TestFromOldestIsResolvedInsideTheSubscription(t *testing.T) {
	f := newFeed(4)
	for i := 0; i < 30; i++ {
		f.publish(domain.Event{Type: "response.output_text.delta"}, "resp_x", "sess_x")
	}

	evs := take(t, f, FromOldest, 1)
	if evs[0].Seq != f.Oldest() {
		t.Fatalf("started at %d, want the oldest retained event %d", evs[0].Seq, f.Oldest())
	}
}

// A resume point still inside the retained window is honoured, which is the
// whole reason to keep a window at all.
func TestFeedResumesWithinItsRetainedWindow(t *testing.T) {
	f := newFeed(8)
	for i := 0; i < 8; i++ {
		f.publish(domain.Event{Type: "response.output_text.delta"}, "resp_x", "sess_x")
	}

	evs := take(t, f, 6, 2)
	if evs[0].Seq != 6 || evs[1].Seq != 7 {
		t.Fatalf("resumed at %d,%d, want 6,7", evs[0].Seq, evs[1].Seq)
	}
}

// Deleting a harness ends the streams following it. Leaving them open would
// have a client waiting on a harness that no longer exists, and no event will
// ever arrive to tell it so.
func TestDeletingAHarnessEndsItsFeed(t *testing.T) {
	svc, _ := managedService(t)
	ctx := context.Background()

	h, err := svc.CreateHarness(ctx, HarnessSpec{Name: "Watched", Base: "echo"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	feed, ok, err := svc.HarnessFeed(ctx, h.ID)
	if err != nil || !ok {
		t.Fatalf("HarnessFeed: ok=%v err=%v", ok, err)
	}

	done := make(chan error, 1)
	go func() {
		sub, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done <- feed.Events(sub, 0, IdleTick{}, func(domain.Event) error { return nil })
	}()

	if err := svc.DeleteHarness(ctx, h.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("feed ended with %v, want a clean end", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the feed of a deleted harness never ended")
	}
}
