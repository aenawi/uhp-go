package http

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// producingAdapter is a harness that reads whatever it was given and writes a
// report, which is the shape of every task the Files chapter is about.
type producingAdapter struct {
	produce map[string]string
	seenDir string
}

func (a *producingAdapter) Info() uhpgo.Harness {
	return uhpgo.Harness{Harness: uhp.Harness{ID: "chrn_writer", Base: "writer", Name: "Writer", Object: "harness"}}
}
func (a *producingAdapter) HealthCheck(context.Context) error { return nil }
func (a *producingAdapter) Cancel(context.Context, string) error {
	return nil
}
func (a *producingAdapter) Run(_ context.Context, req harness.RunRequest) (<-chan harness.RunUpdate, error) {
	a.seenDir = req.WorkDir
	for name, content := range a.produce {
		if err := os.WriteFile(filepath.Join(req.WorkDir, name), []byte(content), 0o600); err != nil {
			return nil, err
		}
	}
	ch := make(chan harness.RunUpdate, 2)
	ch <- harness.RunUpdate{Type: harness.UpdateDelta, Delta: "wrote it"}
	ch <- harness.RunUpdate{Type: harness.UpdateCompleted}
	close(ch)
	return ch, nil
}

func newFileServer(t *testing.T) (*Server, *producingAdapter) {
	t.Helper()
	a := &producingAdapter{produce: map[string]string{"uhp-conformance.txt": "artifact-ok"}}
	reg := harness.NewRegistry()
	reg.Register(a)
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		service.WithWorkspace(t.TempDir()), service.WithUploads(store.NewMemoryUploads()))
	return NewServer(svc, slog.Default(), nil, 0), a
}

func do(t *testing.T, srv *Server, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if body != nil && method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

// runFileTask posts a task and returns the response body.
func runFileTask(t *testing.T, srv *Server, body string) map[string]any {
	t.Helper()
	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(body))
	if w.Code != 200 {
		t.Fatalf("create task: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

func sessionID(t *testing.T, resp map[string]any) string {
	t.Helper()
	meta, _ := resp["metadata"].(map[string]any)
	id, _ := meta["session_id"].(string)
	if id == "" {
		t.Fatalf("no session_id in %v", resp)
	}
	return id
}

// The check the conformance suite calls X-05: a task whose input is an item
// array with an inline file used to fail to unmarshal and come back 400.
func TestTaskWithAnInlineFileRuns(t *testing.T) {
	srv, a := newFileServer(t)
	data := base64.StdEncoding.EncodeToString([]byte("The secret token is uhp-abc123."))
	body := `{"input":[{"role":"user","content":[
		{"type":"input_text","text":"Reply with only the secret token from the attached file."},
		{"type":"input_file","filename":"token.txt","file_data":"data:text/plain;base64,` + data + `"}]}],
		"metadata":{"harness_id":"writer"}}`

	resp := runFileTask(t, srv, body)
	if resp["status"] != "completed" {
		t.Fatalf("status = %v (%v)", resp["status"], resp)
	}
	got, err := os.ReadFile(filepath.Join(a.seenDir, "token.txt"))
	if err != nil || !strings.Contains(string(got), "uhp-abc123") {
		t.Fatalf("the harness was not given the file: %v %q", err, got)
	}
}

func TestSessionFilesListAndDownload(t *testing.T) {
	srv, _ := newFileServer(t)
	resp := runFileTask(t, srv, `{"input":"write a file","metadata":{"harness_id":"writer"}}`)
	sid := sessionID(t, resp)

	w := do(t, srv, "GET", "/v1/sessions/"+sid+"/files", nil)
	if w.Code != 200 {
		t.Fatalf("list files: %d %s", w.Code, w.Body.String())
	}
	var listing struct {
		Files []struct {
			ID          string `json:"id"`
			Object      string `json:"object"`
			ContainerID string `json:"container_id"`
			Filename    string `json:"filename"`
			Bytes       int64  `json:"bytes"`
			DownloadURL string `json:"download_url"`
		} `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
		t.Fatalf("unmarshal listing: %v", err)
	}
	if len(listing.Files) != 1 {
		t.Fatalf("files = %v", listing.Files)
	}
	f := listing.Files[0]
	if f.Object != "file" || f.Filename != "uhp-conformance.txt" || f.Bytes != int64(len("artifact-ok")) {
		t.Fatalf("file object = %+v", f)
	}

	dl := do(t, srv, "GET", "/v1/containers/"+f.ContainerID+"/files/"+f.ID+"/content", nil)
	if dl.Code != 200 {
		t.Fatalf("download: %d %s", dl.Code, dl.Body.String())
	}
	if dl.Body.String() != "artifact-ok" {
		t.Errorf("body = %q, want the raw bytes", dl.Body.String())
	}
	if got := dl.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q; artifacts served without it are stored XSS", got)
	}
	if ct := dl.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := dl.Header().Get("Content-Disposition"); !strings.Contains(cd, "uhp-conformance.txt") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if f.DownloadURL != "/v1/containers/"+f.ContainerID+"/files/"+f.ID+"/content" {
		t.Errorf("download_url = %q", f.DownloadURL)
	}
}

// The response that reports the task complete already cites its files, so a
// client never has to poll a second endpoint to discover them.
func TestArtifactsAreAnnotatedOnTheResponse(t *testing.T) {
	srv, _ := newFileServer(t)
	resp := runFileTask(t, srv, `{"input":"write a file","metadata":{"harness_id":"writer"}}`)

	out, _ := resp["output"].([]any)
	if len(out) == 0 {
		t.Fatalf("no output items")
	}
	msg, _ := out[0].(map[string]any)
	content, _ := msg["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content parts: %v", msg)
	}
	part, _ := content[0].(map[string]any)
	anns, _ := part["annotations"].([]any)
	if len(anns) != 1 {
		t.Fatalf("annotations = %v", anns)
	}
	an, _ := anns[0].(map[string]any)
	if an["type"] != "container_file_citation" || an["filename"] != "uhp-conformance.txt" {
		t.Errorf("annotation = %v", an)
	}
	if part["text"] != "wrote it" {
		t.Errorf("the message text was lost: %v", part["text"])
	}
}

// X-08: artifact ids must not traverse out of their container. The endpoint
// exists now, so this stops passing vacuously.
func TestTraversalProbesAreRefused(t *testing.T) {
	srv, _ := newFileServer(t)
	resp := runFileTask(t, srv, `{"input":"write a file","metadata":{"harness_id":"writer"}}`)
	cid := domain.ContainerIDFor(sessionID(t, resp))

	for _, probe := range []string{
		"../../etc/passwd",
		"..%2f..%2fetc%2fpasswd",
		"%2e%2e/%2e%2e/etc/passwd",
		"....//....//etc/passwd",
	} {
		w := do(t, srv, "GET", "/v1/containers/"+cid+"/files/"+probe+"/content", nil)
		if w.Code != 400 && w.Code != 403 && w.Code != 404 {
			t.Errorf("probe %q returned %d, expected a refusal", probe, w.Code)
		}
		if bytes.Contains(w.Body.Bytes(), []byte("root:")) {
			t.Fatalf("probe %q returned passwd content", probe)
		}
	}
	// A container id from another shape is refused too.
	for _, cid := range []string{"cntr_../..", "..", "cntr_%2e%2e"} {
		w := do(t, srv, "GET", "/v1/containers/"+cid+"/files/file_abc/content", nil)
		if w.Code != 400 && w.Code != 403 && w.Code != 404 {
			t.Errorf("container probe %q returned %d", cid, w.Code)
		}
	}
}

func TestUploadThenReferenceByFileID(t *testing.T) {
	srv, a := newFileServer(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "q3.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("quarterly numbers")); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var up struct {
		ID       string `json:"id"`
		Object   string `json:"object"`
		Filename string `json:"filename"`
		Bytes    int    `json:"bytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &up); err != nil {
		t.Fatalf("unmarshal upload: %v", err)
	}
	if up.ID == "" || up.Object != "file" || up.Bytes != len("quarterly numbers") {
		t.Fatalf("upload response = %+v", up)
	}

	runFileTask(t, srv, `{"input":[{"role":"user","content":[
		{"type":"input_text","text":"Summarise it."},
		{"type":"input_file","file_id":"`+up.ID+`"}]}],"metadata":{"harness_id":"writer"}}`)

	got, err := os.ReadFile(filepath.Join(a.seenDir, "q3.txt"))
	if err != nil || string(got) != "quarterly numbers" {
		t.Fatalf("uploaded file not delivered to the harness: %v %q", err, got)
	}
}

func TestUnknownFileIDIsARequestError(t *testing.T) {
	srv, _ := newFileServer(t)
	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(
		`{"input":[{"type":"input_file","file_id":"file_nope"}],"metadata":{"harness_id":"writer"}}`))
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_input") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestSessionArchiveContainsEveryArtifact(t *testing.T) {
	srv, a := newFileServer(t)
	a.produce = map[string]string{"one.txt": "1", "two.txt": "22"}
	resp := runFileTask(t, srv, `{"input":"write files","metadata":{"harness_id":"writer"}}`)
	sid := sessionID(t, resp)

	w := do(t, srv, "GET", "/v1/sessions/"+sid+"/files/archive", nil)
	if w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q", got)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["one.txt"] || !names["two.txt"] {
		t.Fatalf("archive holds %v", names)
	}
}

func TestPreviewIsRefusedHonestly(t *testing.T) {
	srv, _ := newFileServer(t)
	resp := runFileTask(t, srv, `{"input":"write a file","metadata":{"harness_id":"writer"}}`)
	sid := sessionID(t, resp)

	w := do(t, srv, "GET", "/v1/sessions/"+sid+"/files", nil)
	var listing struct {
		Files []struct {
			ID          string `json:"id"`
			ContainerID string `json:"container_id"`
		} `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil || len(listing.Files) == 0 {
		t.Fatalf("listing: %v %v", err, w.Body.String())
	}
	f := listing.Files[0]

	got := do(t, srv, "GET", "/v1/containers/"+f.ContainerID+"/files/"+f.ID+"/pdf", nil)
	if got.Code != 501 || !strings.Contains(got.Body.String(), "preview_unavailable") {
		t.Fatalf("preview = %d %s", got.Code, got.Body.String())
	}
}

func TestFilesOfAnUnknownSessionAre404(t *testing.T) {
	srv, _ := newFileServer(t)
	w := do(t, srv, "GET", "/v1/sessions/sess_deadbeef/files", nil)
	if w.Code != 404 {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// Discovery must report what this deployment can actually do, not what the
// implementation is capable of in principle.
func TestDiscoveryReportsFileCapabilitiesFromConfiguration(t *testing.T) {
	withFiles, _ := newFileServer(t)
	if got := discoveryCaps(t, withFiles); !got["files_input"] || !got["files_output"] {
		t.Errorf("a server with a workspace reports %v", got)
	}
	if got := discoveryCaps(t, newTestServer()); got["files_input"] || got["files_output"] {
		t.Errorf("a server without a workspace reports %v", got)
	}
}

func discoveryCaps(t *testing.T, srv *Server) map[string]bool {
	t.Helper()
	w := do(t, srv, "GET", "/v1/uhp", nil)
	var doc struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	return doc.Capabilities
}

// Without a workspace there is nowhere to put a client's file, and accepting
// one anyway would mean writing it into the router's own directory.
func TestFileInputWithoutAWorkspaceIsRefused(t *testing.T) {
	srv := newTestServer()
	w := do(t, srv, "POST", "/v1/responses", strings.NewReader(
		`{"input":[{"type":"input_file","filename":"a.txt","file_data":"data:text/plain,hi"}],
		  "metadata":{"harness_id":"echo"}}`))
	if w.Code != 501 {
		t.Fatalf("expected 501, got %d: %s", w.Code, w.Body.String())
	}
}
