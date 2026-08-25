package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
)

// The interface is declared at its consumer, so this is the only place that
// checks both engines still satisfy it. A production file in this package
// cannot make the assertion without importing service, which would invert the
// dependency the declaration site exists to establish.
var (
	_ service.Store = (*MemoryStore)(nil)
	_ service.Store = (*SQLiteStore)(nil)
)

// engines is every service.Store this package ships.
//
// One suite, run twice. A contract asserted against a single implementation is
// only a description of that implementation; the reason a second engine was
// worth building is that it turns the description into something both have to
// obey.
var engines = []struct {
	name string
	open func(t *testing.T) service.Store
}{
	{"memory", func(*testing.T) service.Store { return NewMemoryStore() }},
	{"sqlite", func(t *testing.T) service.Store {
		s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "uhp.db"))
		if err != nil {
			t.Fatalf("open sqlite store: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}},
}

func eachStore(t *testing.T, fn func(t *testing.T, s service.Store)) {
	t.Helper()
	for _, e := range engines {
		t.Run(e.name, func(t *testing.T) { fn(t, e.open(t)) })
	}
}

var storeEpoch = time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)

// sampleTask fills every field a task carries, so a round-trip that drops one
// fails here rather than in the endpoint that needed it.
//
// Metadata and IncompleteDetails hold only values JSON produces on the way in:
// a client's metadata reaches this server through a JSON decoder, so string,
// bool, float64, []any and map[string]any is the whole of what a store can be
// handed. An engine that serialises is entitled to hand back a float64 for a
// number, and pinning a Go `int` here would assert something no caller can
// actually observe.
func sampleTask(id, sessionID string, created time.Time) *domain.Task {
	return &domain.Task{
		Response: uhp.Response{
			ID:     id,
			Object: "response",
			Status: uhp.StatusCompleted,
			Model:  "claude-sonnet-5",
			Output: []uhp.OutputItem{
				{
					ID: "msg_" + id, Type: "message", Status: "completed", Role: "assistant",
					Content: []uhp.ContentPart{{
						Type: "output_text", Text: "hello",
						Annotations: []uhp.Annotation{{
							Type:        uhp.AnnotationTypeFileCitation,
							ContainerID: "cntr_1", FileID: "file_1", Filename: "out/report.md",
							DownloadURL: "/v1/containers/cntr_1/files/file_1/content",
						}},
					}},
				},
				{Type: "function_call", CallID: "call_1", Name: "bash", Arguments: `{"cmd":"ls"}`},
				// An object, not a bare string: the schema types a reasoning
				// summary's entries as objects, and tasks.md §3 shows them as
				// {"type": "summary_text", ...}.
				{Type: "reasoning", Summary: []map[string]any{{"type": "summary_text", "text": "thought"}}},
			},
			Usage:              &uhp.Usage{InputTokens: 11, OutputTokens: 22, TotalTokens: 33, CacheReadTokens: 4},
			Error:              &uhp.Error{Type: "harness_error", Code: "harness_failed", Message: "boom"},
			IncompleteDetails:  map[string]any{"reason": "max_steps"},
			PreviousResponseID: ptr("resp_prev"),
			Store:              true,
			Metadata:           map[string]any{"tenant": "acme", "attempt": 2.0, "tags": []any{"a", "b"}},
			CreatedAt:          created.Unix(),
		},
		UpdatedAt: created.Add(time.Second),
		HarnessID: "claude-code",
		SessionID: sessionID,
		Input:     "do the thing",
		InputItems: []json.RawMessage{
			json.RawMessage(`{"type":"input_text","text":"do the thing"}`),
			json.RawMessage(`{"type":"input_file","file_id":"file_1"}`),
		},
		RequestedModel: "claude-opus-5",
		Artifacts: []domain.Artifact{{
			File: uhp.File{
				ID: "file_1", Object: "file", ContainerID: "cntr_1",
				Filename: "out/report.md", Bytes: 42, CreatedAt: created.Unix(),
			},
			MimeType: "text/markdown",
		}},
		NativeSessionID: "native-abc",
	}
}

func ptr[T any](v T) *T { return &v }

func sampleSession(id, harnessID string, created time.Time) *domain.Session {
	return &domain.Session{
		Session: uhp.Session{
			ID: id, Object: "session", HarnessID: harnessID, Title: "a session",
			Status:    string(uhp.StatusInProgress),
			CreatedAt: created.Unix(), UpdatedAt: created.Add(time.Minute).Unix(),
		},
		NativeSessionID: "native-" + id, LastResponseID: "resp_last",
	}
}

func TestStoreTaskRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		want := sampleTask("resp_a", "sess_a", storeEpoch)
		if err := s.CreateTask(ctx, want); err != nil {
			t.Fatalf("create: %v", err)
		}

		assertTaskEqual(t, want, mustGetTask(t, s, "resp_a"))
	})
}

// mustGetTask and mustGetSession read a row that has to be there.
//
// Both halves of the answer are checked. "found=false, err=nil" is how an
// engine says the row is absent, so a caller that looked only at err would
// nil-dereference the result instead of saying what went wrong — which is the
// mistake these helpers exist to stop every call site from making once.
func mustGetTask(t *testing.T, s service.Store, id string) *domain.Task {
	t.Helper()
	got, found, err := s.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("get task %s: %v", id, err)
	}
	if !found {
		t.Fatalf("get task %s: not found", id)
	}
	return got
}

func mustGetSession(t *testing.T, s service.Store, id string) *domain.Session {
	t.Helper()
	got, found, err := s.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("get session %s: %v", id, err)
	}
	if !found {
		t.Fatalf("get session %s: not found", id)
	}
	return got
}

// A store that serialises has to be told what a time is; one that keeps
// pointers gets it for free. Both are asked for the same instant back.
func assertTaskEqual(t *testing.T, want, got *domain.Task) {
	t.Helper()
	if got.ID != want.ID || got.Object != want.Object || got.Status != want.Status {
		t.Fatalf("identity fields differ: got %+v", got)
	}
	if got.Model != want.Model || got.RequestedModel != want.RequestedModel {
		t.Fatalf("model fields differ: got model=%q requested=%q", got.Model, got.RequestedModel)
	}
	if got.HarnessID != want.HarnessID || got.SessionID != want.SessionID ||
		got.NativeSessionID != want.NativeSessionID || got.Input != want.Input ||
		got.Store != want.Store {
		t.Fatalf("bookkeeping fields differ: got %+v", got)
	}
	// A pointer, so the comparison is of what it points at — and of whether it
	// points at anything, because null and "" are different answers to "does
	// this response continue one?".
	if (got.PreviousResponseID == nil) != (want.PreviousResponseID == nil) ||
		(got.PreviousResponseID != nil && *got.PreviousResponseID != *want.PreviousResponseID) {
		t.Fatalf("previous_response_id differs: got %v", got.PreviousResponseID)
	}
	if got.CreatedAt != want.CreatedAt || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("timestamps differ: got created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}
	if got.Usage == nil || *got.Usage != *want.Usage {
		t.Fatalf("usage differs: got %+v", got.Usage)
	}
	// Field-wise rather than ==: uhp.Error carries a map, so the struct is not
	// comparable. Collapsing the two renderings of this object onto one type is
	// what put `detail` on the error a task carries in the first place.
	if got.Error == nil || got.Error.Type != want.Error.Type ||
		got.Error.Code != want.Error.Code || got.Error.Message != want.Error.Message {
		t.Fatalf("error differs: got %+v", got.Error)
	}
	if got.IncompleteDetails["reason"] != want.IncompleteDetails["reason"] {
		t.Fatalf("incomplete_details differ: got %+v", got.IncompleteDetails)
	}
	if got.Metadata["tenant"] != want.Metadata["tenant"] || got.Metadata["attempt"] != want.Metadata["attempt"] {
		t.Fatalf("metadata differs: got %+v", got.Metadata)
	}
	if tags, ok := got.Metadata["tags"].([]any); !ok || len(tags) != 2 || tags[0] != "a" {
		t.Fatalf("nested metadata differs: got %#v", got.Metadata["tags"])
	}
	if len(got.Output) != len(want.Output) {
		t.Fatalf("output length differs: got %d want %d", len(got.Output), len(want.Output))
	}
	if got.Text() != want.Text() {
		t.Fatalf("assistant text differs: got %q want %q", got.Text(), want.Text())
	}
	ann := got.Output[0].Content[0].Annotations
	if len(ann) != 1 || ann[0].FileID != "file_1" || ann[0].Type != uhp.AnnotationTypeFileCitation {
		t.Fatalf("annotations differ: got %+v", ann)
	}
	if got.Output[1].CallID != "call_1" || got.Output[1].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("function_call item differs: got %+v", got.Output[1])
	}
	if len(got.Output[2].Summary) != 1 || got.Output[2].Summary[0]["text"] != "thought" {
		t.Fatalf("reasoning summary differs: got %+v", got.Output[2].Summary)
	}
	if len(got.Artifacts) != len(want.Artifacts) {
		t.Fatalf("artifact count differs: got %d", len(got.Artifacts))
	}
	a, w := got.Artifacts[0], want.Artifacts[0]
	if a.ID != w.ID || a.ContainerID != w.ContainerID || a.Filename != w.Filename ||
		a.MimeType != w.MimeType || a.Bytes != w.Bytes || a.CreatedAt != w.CreatedAt {
		t.Fatalf("artifact differs: got %+v want %+v", a, w)
	}
}

// A row that is not there is found=false and **no error**.
//
// This is the contract that decides a status code two layers up. An engine that
// reported absence as an error would leave the service with one answer for two
// conditions, and it picked the wrong one: a store that could not be read was
// answered to the client as a response that does not exist — 404, which says
// the id is wrong and retrying never helps, for a disk where retrying is
// exactly what works. Asserting err == nil, not just err != nil somewhere, is
// the half that keeps a new engine honest.
//
// The writes below still fail outright, and correctly: an update or an append
// aimed at a row that is not there did not do what was asked, and there is no
// second answer for the caller to pick between.
func TestStoreTaskNotFound(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		got, found, err := s.GetTask(ctx, "resp_missing")
		if err != nil {
			t.Fatalf("an absent task is not a failed read: %v", err)
		}
		if found || got != nil {
			t.Fatalf("GetTask invented a task: found=%v task=%+v", found, got)
		}
		if err := s.UpdateTask(ctx, sampleTask("resp_missing", "sess_a", storeEpoch)); err == nil {
			t.Fatal("UpdateTask must not create a task that was never created")
		}
		if err := s.AppendArtifact(ctx, "resp_missing", domain.Artifact{File: uhp.File{ID: "file_x"}}); err == nil {
			t.Fatal("AppendArtifact on an unknown task must fail")
		}
		// Deleting is the exception among the writes: an id that is not there
		// is found=false and not an error. A DELETE for a response that is
		// already gone did what the caller wanted, and the transport turns
		// found=false into the 404 that says so.
		found, err = s.DeleteTask(ctx, "resp_missing")
		if err != nil {
			t.Fatalf("deleting an absent task is not a failed write: %v", err)
		}
		if found {
			t.Fatal("DeleteTask reported deleting a task that was never created")
		}
	})
}

// Tasks §4. The engines have to agree on this one too: a task that is deleted
// is unreadable afterwards, and deleting it again reports that it was not there.
func TestStoreDeleteTask(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		if err := s.CreateTask(ctx, sampleTask("resp_a", "sess_a", storeEpoch)); err != nil {
			t.Fatalf("create: %v", err)
		}

		found, err := s.DeleteTask(ctx, "resp_a")
		if err != nil || !found {
			t.Fatalf("delete: found=%v err=%v", found, err)
		}

		got, found, err := s.GetTask(ctx, "resp_a")
		if err != nil {
			t.Fatalf("read after delete: %v", err)
		}
		if found || got != nil {
			t.Fatalf("a deleted task is still readable: found=%v task=%+v", found, got)
		}

		if found, err := s.DeleteTask(ctx, "resp_a"); err != nil || found {
			t.Fatalf("second delete: found=%v err=%v, want false and no error", found, err)
		}
	})
}

// Sessions §6. A session and the tasks that ran in it are deleted together, and
// that coupling is the contract rather than a convenience: a task row left
// behind stays readable at GET /v1/responses/{id}, artifacts and all, for a
// conversation whose owner has just disposed of it. The turns are the
// conversation, so "the session is gone" and "its turns are gone" are one fact.
//
// The neighbouring session is the other half. Deleting by session_id is one
// statement away from deleting every task in the table, and nothing else in
// this suite would notice.
func TestStoreDeleteSession(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		seed(t, s, sampleSession("sess_a", "chrn_a", storeEpoch))
		seed(t, s, sampleSession("sess_b", "chrn_a", storeEpoch))
		mustCreate(t, s, sampleTask("resp_a1", "sess_a", storeEpoch))
		mustCreate(t, s, sampleTask("resp_a2", "sess_a", storeEpoch))
		mustCreate(t, s, sampleTask("resp_b1", "sess_b", storeEpoch))

		found, err := s.DeleteSession(ctx, "sess_a")
		if err != nil || !found {
			t.Fatalf("delete: found=%v err=%v", found, err)
		}

		if got, found, err := s.GetSession(ctx, "sess_a"); err != nil || found {
			t.Fatalf("a deleted session is still readable: found=%v sess=%+v err=%v", found, got, err)
		}
		for _, id := range []string{"resp_a1", "resp_a2"} {
			if _, found, err := s.GetTask(ctx, id); err != nil || found {
				t.Fatalf("task %s outlived its session: found=%v err=%v", id, found, err)
			}
		}
		tasks, err := s.ListSessionTasks(ctx, "sess_a")
		if err != nil || len(tasks) != 0 {
			t.Fatalf("ListSessionTasks after delete: %d tasks, err=%v, want none", len(tasks), err)
		}

		if _, found, err := s.GetSession(ctx, "sess_b"); err != nil || !found {
			t.Fatalf("deleting sess_a took sess_b with it: found=%v err=%v", found, err)
		}
		if _, found, err := s.GetTask(ctx, "resp_b1"); err != nil || !found {
			t.Fatalf("deleting sess_a took sess_b's task with it: found=%v err=%v", found, err)
		}

		if found, err := s.DeleteSession(ctx, "sess_a"); err != nil || found {
			t.Fatalf("second delete: found=%v err=%v, want false and no error", found, err)
		}
	})
}

func TestStoreUpdateTask(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		task := sampleTask("resp_a", "sess_a", storeEpoch)
		task.Status = uhp.StatusInProgress
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("create: %v", err)
		}

		// An update that changes nothing still has to succeed. A run reaches
		// the same task with the same payload whenever a delta adds no text,
		// and an engine that reports "not found" for a row it declined to
		// rewrite would fail the run over a no-op.
		if err := s.UpdateTask(ctx, task); err != nil {
			t.Fatalf("no-op update: %v", err)
		}

		task.Status = uhp.StatusCompleted
		task.Output[0].Content[0].Text = "hello world"
		task.UpdatedAt = storeEpoch.Add(time.Hour)
		if err := s.UpdateTask(ctx, task); err != nil {
			t.Fatalf("update: %v", err)
		}

		got := mustGetTask(t, s, "resp_a")
		if got.Status != uhp.StatusCompleted || got.Text() != "hello world" {
			t.Fatalf("update did not land: status=%v text=%q", got.Status, got.Text())
		}
		if !got.UpdatedAt.Equal(storeEpoch.Add(time.Hour)) {
			t.Fatalf("updated_at did not land: %v", got.UpdatedAt)
		}
	})
}

// Callers may mutate what they hand over and what they are handed back.
//
// This is the contract MemoryStore's copyTask exists to keep and a serialising
// engine gets for free, which is exactly why it belongs in the shared suite:
// the two engines arrive at it by opposite routes, so only a test both run can
// say they arrive.
func TestStoreTaskIsolatedFromCaller(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		task := sampleTask("resp_a", "sess_a", storeEpoch)
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("create: %v", err)
		}

		// Mutate everything reachable through the value that was handed over.
		task.Output[0].Content[0].Text = "tampered"
		task.Output[0].Content[0].Annotations[0].FileID = "file_tampered"
		task.Metadata["tenant"] = "tampered"
		task.Metadata["tags"].([]any)[0] = "tampered"
		task.IncompleteDetails["reason"] = "tampered"
		task.Artifacts[0].Filename = "tampered"
		task.Usage.TotalTokens = 999
		task.Error.Code = "tampered"

		got := mustGetTask(t, s, "resp_a")
		if got.Text() != "hello" {
			t.Fatalf("caller edited stored output text: %q", got.Text())
		}
		if got.Output[0].Content[0].Annotations[0].FileID != "file_1" {
			t.Fatalf("caller edited a stored annotation: %+v", got.Output[0].Content[0].Annotations[0])
		}
		if got.Metadata["tenant"] != "acme" {
			t.Fatalf("caller edited stored metadata: %+v", got.Metadata)
		}
		// Nested too. An engine that isolates the top level and shares the
		// array under it has left one door open, and only on itself.
		if tags := got.Metadata["tags"].([]any); tags[0] != "a" {
			t.Fatalf("caller edited nested stored metadata: %#v", tags)
		}
		if got.IncompleteDetails["reason"] != "max_steps" {
			t.Fatalf("caller edited stored incomplete_details: %+v", got.IncompleteDetails)
		}
		if got.Artifacts[0].Filename != "out/report.md" {
			t.Fatalf("caller edited a stored artifact: %+v", got.Artifacts[0])
		}
		if got.Usage.TotalTokens != 33 {
			t.Fatalf("caller edited stored usage: %+v", got.Usage)
		}
		if got.Error.Code != "harness_failed" {
			t.Fatalf("caller edited stored error: %+v", got.Error)
		}

		// And now the other direction: what a reader was handed is its own.
		got.Metadata["tenant"] = "tampered"
		got.Output[0].Content[0].Text = "tampered"
		got.Artifacts[0].Filename = "tampered"
		again := mustGetTask(t, s, "resp_a")
		if again.Metadata["tenant"] != "acme" || again.Text() != "hello" || again.Artifacts[0].Filename != "out/report.md" {
			t.Fatalf("a reader's edits reached storage: %+v", again)
		}
	})
}

// An empty slice is not a missing one, and a store must not turn either into
// the other.
//
// This is not pedantry about Go types: ContentPart.Annotations is rendered
// without `omitempty` on purpose, so nil reaches a client as `null` and empty
// reaches it as `[]`, and the specification wants "no annotations"
// distinguishable from "this server predates the field". Task.AppendText mints
// an empty one on the first delta of every run, so the case is on the hot path
// rather than hypothetical.
//
// The two engines can only fail this in opposite directions — a copying store
// by collapsing empty to nil, a serialising one by an `omitempty` tag — which
// is why it is asserted here rather than against either.
func TestStoreNilAndEmptyStayDistinct(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()

		empty := sampleTask("resp_empty", "sess_a", storeEpoch)
		empty.Output = []uhp.OutputItem{{
			ID: "msg_resp_empty", Type: "message", Role: "assistant",
			Content: []uhp.ContentPart{{
				Type: "output_text", Text: "hello",
				Annotations: []uhp.Annotation{},
			}},
		}}
		empty.Artifacts = []domain.Artifact{}
		empty.Metadata = map[string]any{}
		empty.IncompleteDetails = map[string]any{}
		if err := s.CreateTask(ctx, empty); err != nil {
			t.Fatalf("create empty: %v", err)
		}

		got := mustGetTask(t, s, "resp_empty")
		if got.Output[0].Content[0].Annotations == nil {
			t.Fatal("an empty annotation list came back nil, which a client reads as null")
		}
		if got.Artifacts == nil {
			t.Fatal("an empty artifact list came back nil")
		}
		if got.Metadata == nil {
			t.Fatal("empty metadata came back nil")
		}
		if got.IncompleteDetails == nil {
			t.Fatal("empty incomplete_details came back nil")
		}

		// And the other direction: nothing invents a list where there was none.
		absent := sampleTask("resp_nil", "sess_a", storeEpoch)
		absent.Output = nil
		absent.Artifacts = nil
		absent.Metadata = nil
		absent.IncompleteDetails = nil
		if err := s.CreateTask(ctx, absent); err != nil {
			t.Fatalf("create nil: %v", err)
		}
		got = mustGetTask(t, s, "resp_nil")
		if got.Output != nil || got.Artifacts != nil || got.Metadata != nil || got.IncompleteDetails != nil {
			t.Fatalf("a store invented a value that was never set: %+v", got)
		}
	})
}

func TestStoreAppendArtifact(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		task := sampleTask("resp_a", "sess_a", storeEpoch)
		task.Artifacts = nil
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("create: %v", err)
		}

		for _, id := range []string{"file_1", "file_2", "file_3"} {
			a := domain.Artifact{
				File: uhp.File{
					ID: id, Object: "file", ContainerID: "cntr_a",
					Filename: "out/" + id + ".md", Bytes: 7, CreatedAt: storeEpoch.Unix(),
				},
				MimeType: "text/markdown",
			}
			if err := s.AppendArtifact(ctx, "resp_a", a); err != nil {
				t.Fatalf("append %s: %v", id, err)
			}
		}

		got := mustGetTask(t, s, "resp_a")
		if len(got.Artifacts) != 3 {
			t.Fatalf("want 3 artifacts, got %d", len(got.Artifacts))
		}
		// Order is the order they were produced, which is what a client sees
		// when it lists a container's files.
		for i, id := range []string{"file_1", "file_2", "file_3"} {
			if got.Artifacts[i].ID != id {
				t.Fatalf("artifact %d is %s, want %s", i, got.Artifacts[i].ID, id)
			}
		}
	})
}

// A run produces files while it is still streaming, so two appends can land at
// once. An engine that reads the list, appends and writes it back without
// holding anything loses one of the two files.
func TestStoreAppendArtifactConcurrently(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		task := sampleTask("resp_a", "sess_a", storeEpoch)
		task.Artifacts = nil
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("create: %v", err)
		}

		const n = 16
		errs := make(chan error, n)
		var start sync.WaitGroup
		start.Add(1)
		for i := 0; i < n; i++ {
			go func(i int) {
				start.Wait()
				errs <- s.AppendArtifact(ctx, "resp_a", domain.Artifact{File: uhp.File{
					ID:          fmt.Sprintf("file_%02d", i),
					Object:      "file",
					ContainerID: "cntr_a",
					Filename:    fmt.Sprintf("out/%02d.md", i),
					CreatedAt:   storeEpoch.Unix(),
				}})
			}(i)
		}
		start.Done()
		for i := 0; i < n; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent append: %v", err)
			}
		}

		got := mustGetTask(t, s, "resp_a")
		if len(got.Artifacts) != n {
			t.Fatalf("want %d artifacts, got %d — an append was lost", n, len(got.Artifacts))
		}
		seen := make(map[string]bool, n)
		for _, a := range got.Artifacts {
			seen[a.ID] = true
		}
		if len(seen) != n {
			t.Fatalf("want %d distinct artifacts, got %d", n, len(seen))
		}
	})
}

func TestStoreSessionRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		want := sampleSession("sess_a", "claude-code", storeEpoch)
		if err := s.CreateSession(ctx, want); err != nil {
			t.Fatalf("create: %v", err)
		}

		got := mustGetSession(t, s, "sess_a")
		if got.ID != want.ID || got.HarnessID != want.HarnessID || got.Title != want.Title ||
			got.Status != want.Status || got.NativeSessionID != want.NativeSessionID ||
			got.LastResponseID != want.LastResponseID {
			t.Fatalf("session round-trip lost fields: %+v", got)
		}
		if got.CreatedAt != want.CreatedAt || got.UpdatedAt != want.UpdatedAt {
			t.Fatalf("session timestamps differ: %v / %v", got.CreatedAt, got.UpdatedAt)
		}

		// Absence is found=false and no error, for the reason TestStoreTaskNotFound
		// gives: the service two layers up turns these two into different status
		// codes, and an engine that merged them would leave it guessing.
		missing, found, err := s.GetSession(ctx, "sess_missing")
		if err != nil {
			t.Fatalf("an absent session is not a failed read: %v", err)
		}
		if found || missing != nil {
			t.Fatalf("GetSession invented a session: found=%v session=%+v", found, missing)
		}
		if err := s.UpdateSession(ctx, sampleSession("sess_missing", "claude-code", storeEpoch)); err == nil {
			t.Fatal("UpdateSession must not create a session that was never created")
		}

		want.Title = "renamed"
		want.Status = string(uhp.StatusCompleted)
		want.LastResponseID = "resp_z"
		if err := s.UpdateSession(ctx, want); err != nil {
			t.Fatalf("update: %v", err)
		}
		got = mustGetSession(t, s, "sess_a")
		if got.Title != "renamed" || got.Status != string(uhp.StatusCompleted) || got.LastResponseID != "resp_z" {
			t.Fatalf("session update did not land: %+v", got)
		}
	})
}

// The order is newest first and total: ties on CreatedAt break on id. Cursor
// paging over anything less silently skips and repeats rows.
func TestStoreListSessionsOrder(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		// Two sessions share an instant, so the tie-break is exercised rather
		// than assumed.
		seed(t, s, sampleSession("sess_c", "claude-code", storeEpoch.Add(2*time.Minute)))
		seed(t, s, sampleSession("sess_b", "codex", storeEpoch.Add(time.Minute)))
		seed(t, s, sampleSession("sess_a", "claude-code", storeEpoch.Add(time.Minute)))

		page, err := s.ListSessions(ctx, domain.SessionFilter{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if ids := sessionIDs(page); ids != "sess_c,sess_a,sess_b" {
			t.Fatalf("order is %s, want sess_c,sess_a,sess_b", ids)
		}
		if page.NextCursor != "" {
			t.Fatalf("a complete listing must not offer a next cursor, got %q", page.NextCursor)
		}
	})
}

func TestStoreListSessionsFilterAndPaging(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		for i, id := range []string{"sess_a", "sess_b", "sess_c", "sess_d"} {
			harness := "claude-code"
			if i%2 == 1 {
				harness = "codex"
			}
			seed(t, s, sampleSession(id, harness, storeEpoch.Add(time.Duration(i)*time.Minute)))
		}

		filtered, err := s.ListSessions(ctx, domain.SessionFilter{HarnessID: "codex"})
		if err != nil {
			t.Fatalf("filtered list: %v", err)
		}
		if ids := sessionIDs(filtered); ids != "sess_d,sess_b" {
			t.Fatalf("harness filter returned %s, want sess_d,sess_b", ids)
		}

		first, err := s.ListSessions(ctx, domain.SessionFilter{Limit: 2})
		if err != nil {
			t.Fatalf("page one: %v", err)
		}
		if ids := sessionIDs(first); ids != "sess_d,sess_c" {
			t.Fatalf("page one is %s, want sess_d,sess_c", ids)
		}
		// A full page that happens to be the last one still has to say so,
		// which is the whole reason NextCursor exists.
		if first.NextCursor != "sess_c" {
			t.Fatalf("page one cursor is %q, want sess_c", first.NextCursor)
		}

		second, err := s.ListSessions(ctx, domain.SessionFilter{Limit: 2, Cursor: first.NextCursor})
		if err != nil {
			t.Fatalf("page two: %v", err)
		}
		if ids := sessionIDs(second); ids != "sess_b,sess_a" {
			t.Fatalf("page two is %s, want sess_b,sess_a", ids)
		}
		if second.NextCursor != "" {
			t.Fatalf("the last page must not offer a cursor, got %q", second.NextCursor)
		}

		// A limit outside the accepted band falls back to the default rather
		// than letting a client ask for the whole table.
		big, err := s.ListSessions(ctx, domain.SessionFilter{Limit: 5000})
		if err != nil {
			t.Fatalf("oversized limit: %v", err)
		}
		if len(big.Sessions) != 4 {
			t.Fatalf("oversized limit returned %d sessions", len(big.Sessions))
		}

		// A cursor naming a session this filter cannot see is not a page
		// boundary, so the listing starts at the beginning rather than
		// guessing where the client meant.
		stray, err := s.ListSessions(ctx, domain.SessionFilter{HarnessID: "codex", Cursor: "sess_c"})
		if err != nil {
			t.Fatalf("stray cursor: %v", err)
		}
		if ids := sessionIDs(stray); ids != "sess_d,sess_b" {
			t.Fatalf("stray cursor returned %s, want sess_d,sess_b", ids)
		}
	})
}

func TestStoreListSessionTasks(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		// Two of a session's tasks share an instant, and one belongs to
		// another session entirely.
		//
		// The pair is created in reverse id order on purpose, and the id that
		// sorts *later* is created first. A response's created_at is Unix
		// seconds, so a shared instant is the ordinary case rather than a
		// contrived one — two tasks in a conversation are usually seconds
		// apart, sometimes less — and the answer has to be the order they ran.
		// A store that fell back to comparing ids would pass this with the
		// operands the other way round and shuffle every real transcript.
		mustCreate(t, s, sampleTask("resp_c", "sess_a", storeEpoch.Add(2*time.Minute)))
		mustCreate(t, s, sampleTask("resp_b", "sess_a", storeEpoch))
		mustCreate(t, s, sampleTask("resp_a", "sess_a", storeEpoch))
		mustCreate(t, s, sampleTask("resp_other", "sess_b", storeEpoch.Add(time.Minute)))

		tasks, err := s.ListSessionTasks(ctx, "sess_a")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var ids string
		for i, task := range tasks {
			if i > 0 {
				ids += ","
			}
			ids += task.ID
		}
		// Oldest first: this is a transcript, not a feed.
		if ids != "resp_b,resp_a,resp_c" {
			t.Fatalf("order is %s, want resp_b,resp_a,resp_c", ids)
		}

		// A session with no tasks is an empty list, not an error: a session
		// exists before its first task finishes.
		empty, err := s.ListSessionTasks(ctx, "sess_empty")
		if err != nil {
			t.Fatalf("empty list: %v", err)
		}
		if len(empty) != 0 {
			t.Fatalf("want no tasks, got %d", len(empty))
		}
	})
}

func seed(t *testing.T, s service.Store, sess *domain.Session) {
	t.Helper()
	if err := s.CreateSession(context.Background(), sess); err != nil {
		t.Fatalf("create session %s: %v", sess.ID, err)
	}
}

func mustCreate(t *testing.T, s service.Store, task *domain.Task) {
	t.Helper()
	if err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task %s: %v", task.ID, err)
	}
}

func sessionIDs(p domain.SessionPage) string {
	var out string
	for i, sess := range p.Sessions {
		if i > 0 {
			out += ","
		}
		out += sess.ID
	}
	return out
}
