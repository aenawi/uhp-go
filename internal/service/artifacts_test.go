package service

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/store"
)

// writingAdapter stands in for a real harness: it writes files into the working
// directory it was given, the way an agent that "creates a report" does, and
// then completes.
type writingAdapter struct {
	writes map[string]string
	links  map[string]string
	seen   harness.RunRequest
}

// It declares neither file capability, exactly as every shipped harness now
// does, and that omission is part of what these tests check: file input and
// artifact capture are the router's, so an adapter that says nothing about them
// still receives its attachments and still has its output captured. An adapter
// that had to claim them would be a claim the router never reads.
func (a *writingAdapter) Info() domain.Harness {
	return domain.Harness{ID: "chrn_writer", Base: "writer", Object: "harness", Name: "Writer",
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapSessions, domain.CapCancellation,
		}}
}
func (a *writingAdapter) HealthCheck(context.Context) error { return nil }
func (a *writingAdapter) Cancel(context.Context, string) error {
	return nil
}

func (a *writingAdapter) Run(_ context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	a.seen = req
	for name, content := range a.writes {
		full := filepath.Join(req.WorkDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			return nil, err
		}
	}
	for name, target := range a.links {
		if err := os.Symlink(target, filepath.Join(req.WorkDir, name)); err != nil {
			return nil, err
		}
	}
	ch := make(chan harness.RunUpdate, 2)
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "done"}
	ch <- harness.RunUpdate{Type: harness.UpdateCompleted}
	close(ch)
	return ch, nil
}

func newWriterService(t *testing.T, a *writingAdapter) (*TaskService, string) {
	t.Helper()
	reg := harness.NewRegistry()
	reg.Register(a)
	root := t.TempDir()
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		WithWorkspace(root), WithUploads(store.NewMemoryUploads()))
	return svc, root
}

// runStored runs a task to completion and returns it as stored, which is what
// a client would later retrieve.
func runStored(t *testing.T, svc *TaskService, req CreateTaskRequest) *domain.Task {
	t.Helper()
	task, run, err := svc.StartTask(context.Background(), req)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	final, err := svc.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return final
}

func TestArtifactCaptureReportsWhatTheRunWrote(t *testing.T) {
	a := &writingAdapter{writes: map[string]string{
		"report.md":          "artifact-ok\n",
		"nested/summary.txt": "also produced\n",
	}}
	svc, _ := newWriterService(t, a)

	task := runStored(t, svc, CreateTaskRequest{Input: "write a report", HarnessID: "writer"})

	files, err := svc.SessionFiles(context.Background(), task.SessionID)
	if err != nil {
		t.Fatalf("session files: %v", err)
	}
	got := map[string]domain.Artifact{}
	for _, f := range files {
		got[f.Path] = f
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 artifacts, got %d: %v", len(got), got)
	}
	if got["report.md"].SizeBytes != int64(len("artifact-ok\n")) {
		t.Errorf("report.md size = %d", got["report.md"].SizeBytes)
	}
	if got["report.md"].ContainerID != domain.ContainerIDFor(task.SessionID) {
		t.Errorf("artifact container %q does not belong to session %q",
			got["report.md"].ContainerID, task.SessionID)
	}
	if got["nested/summary.txt"].BaseName() != "summary.txt" {
		t.Errorf("nested artifact base name = %q", got["nested/summary.txt"].BaseName())
	}
}

// A file the task did not touch is not something the task produced, and neither
// is a file the router itself wrote as the task's input.
func TestArtifactCaptureIgnoresPreexistingFilesAndItsOwnInput(t *testing.T) {
	a := &writingAdapter{writes: map[string]string{"new.txt": "new\n"}}
	svc, root := newWriterService(t, a)

	first := runStored(t, svc, CreateTaskRequest{Input: "first", HarnessID: "writer"})
	// Everything the first task produced is now pre-existing for the second,
	// which writes nothing of its own and only receives an input file.
	a.writes = nil
	second, run, err := svc.StartTask(context.Background(), CreateTaskRequest{
		Input:              "second",
		HarnessID:          "writer",
		PreviousResponseID: first.ID,
		Attachments:        []Attachment{{Filename: "token.txt", Data: []byte("the secret token")}},
	})
	if err != nil {
		t.Fatalf("start second: %v", err)
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	stored, err := svc.GetTask(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(stored.Artifacts) != 0 {
		t.Fatalf("second task reported %d artifacts, want none: %v", len(stored.Artifacts), stored.Artifacts)
	}
	// The input file really is there for the harness to read.
	data, err := os.ReadFile(filepath.Join(root, second.SessionID, "token.txt"))
	if err != nil || string(data) != "the secret token" {
		t.Fatalf("input file not materialized: %v %q", err, data)
	}
	// And the harness was told it is there. The file on disk is the delivery
	// mechanism, but a model asked to "read the attached file" cannot know a
	// file was attached or what it is called unless the prompt says so.
	if !strings.Contains(a.seen.Input, "token.txt") {
		t.Errorf("prompt does not name the attached file: %q", a.seen.Input)
	}
}

// An agent can be persuaded to write a symlink. Following one would let an
// artifact download serve any file the server can read.
func TestArtifactCaptureNeverFollowsASymlink(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(secret, []byte("root:x:0:0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &writingAdapter{
		writes: map[string]string{"ok.txt": "fine\n"},
		links:  map[string]string{"escape.txt": secret},
	}
	svc, _ := newWriterService(t, a)

	task := runStored(t, svc, CreateTaskRequest{Input: "write", HarnessID: "writer"})
	files, err := svc.SessionFiles(context.Background(), task.SessionID)
	if err != nil {
		t.Fatalf("session files: %v", err)
	}
	for _, f := range files {
		if f.Path == "escape.txt" {
			t.Fatalf("a symlink was captured as an artifact")
		}
	}
	if len(files) != 1 {
		t.Fatalf("expected only the regular file, got %v", files)
	}
}

func TestSessionFilesIncludeEarlierTasks(t *testing.T) {
	a := &writingAdapter{writes: map[string]string{"first.txt": "1\n"}}
	svc, _ := newWriterService(t, a)

	first := runStored(t, svc, CreateTaskRequest{Input: "one", HarnessID: "writer"})
	a.writes = map[string]string{"second.txt": "2\n"}
	runStored(t, svc, CreateTaskRequest{Input: "two", HarnessID: "writer", PreviousResponseID: first.ID})

	files, err := svc.SessionFiles(context.Background(), first.SessionID)
	if err != nil {
		t.Fatalf("session files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected both tasks' files, got %v", files)
	}
}

// A file a later task rewrites is the same file, not a second one.
func TestRewrittenArtifactKeepsOneEntry(t *testing.T) {
	a := &writingAdapter{writes: map[string]string{"report.md": "v1\n"}}
	svc, _ := newWriterService(t, a)

	first := runStored(t, svc, CreateTaskRequest{Input: "one", HarnessID: "writer"})
	a.writes = map[string]string{"report.md": "v2-longer\n"}
	runStored(t, svc, CreateTaskRequest{Input: "two", HarnessID: "writer", PreviousResponseID: first.ID})

	files, err := svc.SessionFiles(context.Background(), first.SessionID)
	if err != nil {
		t.Fatalf("session files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one entry for one file, got %v", files)
	}
	if files[0].SizeBytes != int64(len("v2-longer\n")) {
		t.Errorf("listing reports the stale size %d", files[0].SizeBytes)
	}
}

func TestArtifactsAreCitedOnTheAssistantMessage(t *testing.T) {
	a := &writingAdapter{writes: map[string]string{"report.md": "artifact-ok\n"}}
	svc, _ := newWriterService(t, a)

	task := runStored(t, svc, CreateTaskRequest{Input: "write a report", HarnessID: "writer"})
	_, item := task.MessageItem()
	if item == nil || len(item.Content) == 0 {
		t.Fatalf("no assistant message on the response")
	}
	if item.Content[0].Text != "done" {
		t.Errorf("annotations replaced the text: %q", item.Content[0].Text)
	}
	ans := item.Content[0].Annotations
	if len(ans) != 1 {
		t.Fatalf("expected one annotation, got %v", ans)
	}
	if ans[0].Type != domain.AnnotationTypeFileCitation {
		t.Errorf("annotation type = %q", ans[0].Type)
	}
	if ans[0].Filename != "report.md" || ans[0].FileID == "" || ans[0].DownloadURL == "" {
		t.Errorf("annotation does not identify a downloadable file: %+v", ans[0])
	}
}

func TestOpenArtifactServesOnlyRecordedFiles(t *testing.T) {
	a := &writingAdapter{writes: map[string]string{"report.md": "artifact-ok\n"}}
	svc, _ := newWriterService(t, a)
	task := runStored(t, svc, CreateTaskRequest{Input: "write", HarnessID: "writer"})

	files, err := svc.SessionFiles(context.Background(), task.SessionID)
	if err != nil || len(files) != 1 {
		t.Fatalf("session files: %v %v", files, err)
	}
	cid := files[0].ContainerID

	a2, f, err := svc.OpenArtifact(context.Background(), cid, files[0].ID)
	if err != nil {
		t.Fatalf("open artifact: %v", err)
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(f)
	if string(b) != "artifact-ok\n" {
		t.Errorf("served %q", b)
	}
	if a2.MimeType == "" {
		t.Errorf("no mime type on the artifact")
	}

	for _, tc := range []struct{ name, container, file string }{
		{"unknown file id", cid, "file_deadbeef"},
		{"traversal as a file id", cid, "../../etc/passwd"},
		{"traversal as a container id", "cntr_../../etc", files[0].ID},
		{"unknown container", "cntr_deadbeef", files[0].ID},
		{"a path as a container id", "/etc", files[0].ID},
	} {
		if _, f, err := svc.OpenArtifact(context.Background(), tc.container, tc.file); err == nil {
			_ = f.Close()
			t.Errorf("%s: was served instead of refused", tc.name)
		}
	}
}

func TestAttachmentFilenamesCannotEscapeTheWorkspace(t *testing.T) {
	a := &writingAdapter{}
	svc, root := newWriterService(t, a)

	task := runStored(t, svc, CreateTaskRequest{
		Input:     "read them",
		HarnessID: "writer",
		Attachments: []Attachment{
			{Filename: "../../etc/passwd", Data: []byte("nope")},
			{Filename: "report.md", Data: []byte("one")},
			{Filename: "report.md", Data: []byte("two")},
			{Filename: "", Data: []byte("unnamed")},
		},
	})

	dir := filepath.Join(root, task.SessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := map[string]bool{"passwd": true, "report.md": true, "report-2.md": true, "input-4": true}
	if len(names) != len(want) {
		t.Fatalf("materialized %v, want %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected file %q", n)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "..", "etc", "passwd")); err == nil {
		t.Fatal("an attachment escaped the workspace")
	}
	// The harness is told what it was given, or it has no way to know.
	if got := task.Input; !strings.Contains(got, "report.md") || !strings.Contains(got, "Attached files") {
		t.Errorf("prompt does not mention the attachments: %q", got)
	}
}

func TestFileInputWithoutAWorkspaceIsRefusedNotIgnored(t *testing.T) {
	reg := harness.NewRegistry()
	reg.Register(&writingAdapter{})
	svc := NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	if svc.FilesEnabled() {
		t.Fatal("files reported as enabled without a workspace")
	}
	_, _, err := svc.StartTask(context.Background(), CreateTaskRequest{
		Input:       "read it",
		HarnessID:   "writer",
		Attachments: []Attachment{{Filename: "a.txt", Data: []byte("x")}},
	})
	if err == nil {
		t.Fatal("a file input was silently dropped")
	}
}

func TestUnknownUploadIDIsARequestError(t *testing.T) {
	svc, _ := newWriterService(t, &writingAdapter{})
	_, _, err := svc.StartTask(context.Background(), CreateTaskRequest{
		Input:       "read it",
		HarnessID:   "writer",
		Attachments: []Attachment{{FileID: "file_nope"}},
	})
	if err == nil {
		t.Fatal("an unknown file id was accepted")
	}
}

func TestUploadRoundTripsAndFeedsATask(t *testing.T) {
	svc, root := newWriterService(t, &writingAdapter{})
	up, err := svc.StoreUpload(context.Background(), "q3.pdf", "application/pdf", []byte("%PDF-1.4"))
	if err != nil {
		t.Fatalf("store upload: %v", err)
	}
	task := runStored(t, svc, CreateTaskRequest{
		Input:       "summarise it",
		HarnessID:   "writer",
		Attachments: []Attachment{{FileID: up.ID}},
	})
	data, err := os.ReadFile(filepath.Join(root, task.SessionID, "q3.pdf"))
	if err != nil || string(data) != "%PDF-1.4" {
		t.Fatalf("uploaded file not materialized: %v %q", err, data)
	}
}
