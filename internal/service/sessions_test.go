package service

import (
	"context"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
)

func runOnce(t *testing.T, svc *TaskService, req CreateTaskRequest) *domain.Task {
	t.Helper()
	ctx := context.Background()
	task, run, err := svc.StartTask(ctx, req)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return task
}

// A session must be reachable by the friendly alias as well as the canonical
// id. Sessions store the canonical `chrn_` id, so comparing it against a
// request that used the alias reported a harness mismatch between a harness
// and itself.
func TestContinuationWorksThroughAnAlias(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	first := runOnce(t, svc, CreateTaskRequest{Input: "a", HarnessID: "echo"})

	second := runOnce(t, svc, CreateTaskRequest{
		Input: "b", HarnessID: "chrn_echo", PreviousResponseID: first.ID,
	})
	if second.SessionID != first.SessionID {
		t.Fatalf("alias and canonical id produced different sessions")
	}
	third := runOnce(t, svc, CreateTaskRequest{
		Input: "c", HarnessID: "echo", PreviousResponseID: second.ID,
	})
	if third.SessionID != first.SessionID {
		t.Fatalf("continuation through the alias started a new session")
	}
}

func TestSessionCarriesTitleAndStatus(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	task := runOnce(t, svc, CreateTaskRequest{Input: "Summarise   the\nREADME", HarnessID: "echo"})

	sess, err := svc.GetSession(context.Background(), task.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Title != "Summarise the README" {
		t.Errorf("title = %q, want whitespace collapsed onto one line", sess.Title)
	}
	if sess.Status != string(uhp.StatusCompleted) {
		t.Errorf("session status = %q, want it to follow its latest task", sess.Status)
	}
	if sess.HarnessID != "chrn_echo" {
		t.Errorf("session harness = %q, want the canonical id", sess.HarnessID)
	}
}

func TestTitleIsTruncatedOnARuneBoundary(t *testing.T) {
	// Multi-byte runes: truncating by bytes would split one and produce
	// invalid UTF-8.
	long := strings.Repeat("é", 200)
	got := titleFor(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long title was not truncated: %q", got)
	}
	body := strings.TrimSuffix(got, "…")
	if n := len([]rune(body)); n > maxTitleRunes {
		t.Errorf("title is %d runes, want <= %d", n, maxTitleRunes)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("truncation split a multi-byte rune")
		}
	}
}

func TestSessionTurnsAreOrdered(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	first := runOnce(t, svc, CreateTaskRequest{Input: "one", HarnessID: "echo"})
	second := runOnce(t, svc, CreateTaskRequest{Input: "two", HarnessID: "echo", PreviousResponseID: first.ID})
	third := runOnce(t, svc, CreateTaskRequest{Input: "three", HarnessID: "echo", PreviousResponseID: second.ID})

	turns, err := svc.SessionTurns(ctx, first.SessionID)
	if err != nil {
		t.Fatalf("SessionTurns: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("got %d turns, want 3", len(turns))
	}
	wantIDs := []string{first.ID, second.ID, third.ID}
	wantIn := []string{"one", "two", "three"}
	for i := range turns {
		if turns[i].ResponseID != wantIDs[i] {
			t.Errorf("turn %d response_id = %s, want %s", i, turns[i].ResponseID, wantIDs[i])
		}
		if turns[i].Input != wantIn[i] {
			t.Errorf("turn %d input = %q, want %q", i, turns[i].Input, wantIn[i])
		}
		if turns[i].Output == "" {
			t.Errorf("turn %d has no output, so a transcript cannot be rebuilt", i)
		}
	}
}

func TestSessionTurnsUnknownSession(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	if _, err := svc.SessionTurns(context.Background(), "sess_nope"); err == nil {
		t.Fatal("turns for an unknown session returned no error")
	}
}

// Paging must not require the client to infer the end from a short page: that
// heuristic is wrong exactly when a page is full, which is what this exercises
// by requesting a limit that divides the total evenly.
func TestSessionPagingIsExactAndTerminates(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()

	const total = 6
	want := map[string]bool{}
	for i := 0; i < total; i++ {
		task := runOnce(t, svc, CreateTaskRequest{Input: "x", HarnessID: "echo"})
		want[task.SessionID] = true
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	lastPageSize := -1
	for {
		page, err := svc.ListSessions(ctx, domain.SessionFilter{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		pages++
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
		for _, s := range page.Sessions {
			if seen[s.ID] {
				t.Fatalf("session %s returned on two pages", s.ID)
			}
			seen[s.ID] = true
		}
		lastPageSize = len(page.Sessions)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != total {
		t.Fatalf("paged over %d sessions, want %d", len(seen), total)
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("session %s was never returned", id)
		}
	}

	// This is the property UHP actually requires, and it is why "did I get
	// fewer than I asked for?" is not an acceptable end-of-pagination test:
	// 6 sessions at 3 per page means the final page is exactly full, and it
	// must still report the end. A client using the short-page heuristic would
	// have asked for a fourth page here and looped.
	if pages != 2 {
		t.Errorf("pages = %d, want 2 for %d sessions at 3 per page", pages, total)
	}
	if lastPageSize != 3 {
		t.Fatalf("last page had %d items; this test only proves anything if it is full", lastPageSize)
	}
}

func TestSessionListFilterByHarness(t *testing.T) {
	reg := newRegistryWith(echoAdapter{}, otherAdapter{})
	svc := NewTaskService(reg, newMemStore(), testLogger())
	ctx := context.Background()

	runOnce(t, svc, CreateTaskRequest{Input: "a", HarnessID: "echo"})
	runOnce(t, svc, CreateTaskRequest{Input: "b", HarnessID: "other"})

	page, err := svc.ListSessions(ctx, domain.SessionFilter{HarnessID: "echo"})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(page.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(page.Sessions))
	}
	if page.Sessions[0].HarnessID != "chrn_echo" {
		t.Errorf("filtered to the wrong harness: %s", page.Sessions[0].HarnessID)
	}
}

// Cancelling a session must not delete it: the conversation stays continuable.
func TestCancelSessionKeepsItContinuable(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	ctx := context.Background()
	task := runOnce(t, svc, CreateTaskRequest{Input: "a", HarnessID: "echo"})

	if err := svc.CancelSession(ctx, task.SessionID); err != nil {
		t.Fatalf("cancelling an idle session errored: %v", err)
	}
	if _, err := svc.GetSession(ctx, task.SessionID); err != nil {
		t.Fatalf("session was deleted by cancel: %v", err)
	}
	next := runOnce(t, svc, CreateTaskRequest{Input: "b", HarnessID: "echo", PreviousResponseID: task.ID})
	if next.SessionID != task.SessionID {
		t.Error("session was not continuable after cancel")
	}
}

func TestCancelUnknownSession(t *testing.T) {
	svc := newSvc(t, "echo", echoAdapter{})
	if err := svc.CancelSession(context.Background(), "sess_nope"); err == nil {
		t.Fatal("cancelling an unknown session returned no error")
	}
}
