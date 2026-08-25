package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
)

// These are the only tests in this repository that speak UHP over a socket.
//
// Everything else calls a handler directly, which means both halves of the wire
// format have only ever been checked against bytes their own package wrote.
// docs/conformance.md names that gap for SSE framing; it is just as real for
// headers, status codes, the error envelope and the version handshake, and this
// file is where the published client and the server in this repository are made
// to agree about all of them.
//
// The client under test is uhp.Client — the published one, the same code an
// external consumer imports. Using a test-local HTTP client instead would test
// nothing about what this repository ships.
func newLiveServer(t *testing.T, key string) (*uhp.Client, *service.TaskService) {
	t.Helper()
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		service.WithWorkspace(t.TempDir()), service.WithUploads(store.NewMemoryUploads()))

	var keys []string
	if key != "" {
		keys = []string{key}
	}
	httpSrv := httptest.NewServer(NewServer(svc, slog.Default(), keys, 0).Handler())
	t.Cleanup(httpSrv.Close)

	c := uhp.NewClient(httpSrv.URL, key)
	return c, svc
}

// The whole read surface, in one pass, against a real listener. What this
// catches that a handler test cannot is a shape mismatch between what the
// server writes and what the published client expects — the two halves have
// separate declarations of every envelope, and nothing else compares them.
func TestPublishedClientDrivesThisServer(t *testing.T) {
	c, _ := newLiveServer(t, "")
	ctx := context.Background()

	d, err := c.Discover(ctx)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if d.Object != "uhp.discovery" || d.DefaultVersion != uhp.Version {
		t.Fatalf("discovery = %+v", d)
	}

	harnesses, err := c.ListHarnesses(ctx)
	if err != nil {
		t.Fatalf("harnesses: %v", err)
	}
	if len(harnesses) != 1 || harnesses[0].ID != "chrn_echo" {
		t.Fatalf("harnesses = %+v", harnesses)
	}

	if _, err := c.HarnessModels(ctx, "chrn_echo"); err != nil {
		t.Fatalf("harness models: %v", err)
	}
	if _, err := c.Models(ctx); err != nil {
		t.Fatalf("models: %v", err)
	}

	resp, err := c.Create(ctx, uhp.CreateResponseRequest{
		Input:    "hello",
		Metadata: map[string]any{"harness_id": "chrn_echo"},
	}, "key-e2e-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.Status != uhp.StatusCompleted {
		t.Fatalf("status = %s, want completed", resp.Status)
	}

	// The endpoints added for #51 and #52, exercised the way a client will.
	items, err := c.InputItems(ctx, resp.ID)
	if err != nil {
		t.Fatalf("input items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("input items = %v, want the one item the string abbreviates", items)
	}

	sessionID, _ := resp.Metadata["session_id"].(string)
	if sessionID == "" {
		t.Fatal("the response carries no metadata.session_id")
	}
	if _, err := c.GetSession(ctx, sessionID); err != nil {
		t.Fatalf("session: %v", err)
	}
	turns, err := c.SessionTurns(ctx, sessionID)
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %v, want one", turns)
	}
	if _, err := c.ListSessions(ctx, uhp.SessionFilter{Limit: 10}); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if _, err := c.SessionFiles(ctx, sessionID); err != nil {
		t.Fatalf("session files: %v", err)
	}

	if err := c.Delete(ctx, resp.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Get(ctx, resp.ID); err == nil {
		t.Fatal("a deleted response is still readable")
	}
}

// The published error envelope decoded by the published client, off this
// server's own wire. The two have separate declarations of the shape, and this
// is the only thing that compares them.
func TestPublishedClientDecodesThisServersErrors(t *testing.T) {
	c, _ := newLiveServer(t, "")

	_, err := c.Get(context.Background(), "resp_does_not_exist")
	e, ok := uhp.AsError(err)
	if !ok {
		t.Fatalf("expected a *uhp.Error, got %T: %v", err, err)
	}
	if e.Code != uhp.CodeResponseNotFound {
		t.Errorf("code = %q, want %s", e.Code, uhp.CodeResponseNotFound)
	}
	if e.Type != uhp.ErrorTypeInvalidRequest {
		t.Errorf("type = %q", e.Type)
	}
}

// Security §1 and the client's half of it: a credential is presented, and a
// wrong one is refused as an authentication error rather than as a transport
// failure the caller has to guess about.
func TestPublishedClientAuthenticates(t *testing.T) {
	c, _ := newLiveServer(t, "right-key")
	ctx := context.Background()

	if _, err := c.ListHarnesses(ctx); err != nil {
		t.Fatalf("with the right key: %v", err)
	}

	// Discovery stays reachable without one, which is the point of it being
	// unauthenticated: a client learns what a server is before choosing a
	// credential for it.
	anonymous := uhp.NewClient(c.BaseURL, "")
	if _, err := anonymous.Discover(ctx); err != nil {
		t.Fatalf("anonymous discovery: %v", err)
	}
	_, err := anonymous.ListHarnesses(ctx)
	e, ok := uhp.AsError(err)
	if !ok {
		t.Fatalf("expected a *uhp.Error, got %T: %v", err, err)
	}
	if e.Type != uhp.ErrorTypeAuthentication {
		t.Errorf("type = %q, want %s", e.Type, uhp.ErrorTypeAuthentication)
	}
}

// The framing join, end to end and over a socket: this server writes SSE, the
// published decoder reads it, and the published Stream checks the sequence
// numbers as it goes. A gap or a renumbering would fail here rather than being
// rendered as a hole in whatever a client is drawing.
func TestPublishedClientReadsThisServersStream(t *testing.T) {
	c, _ := newLiveServer(t, "")

	s, err := c.Stream(context.Background(), uhp.CreateResponseRequest{
		Input:    "hello",
		Metadata: map[string]any{"harness_id": "chrn_echo"},
	}, "key-e2e-stream")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer func() { _ = s.Close() }()

	var text strings.Builder
	var terminal *uhp.Event
	for {
		ev, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if ev.Type == uhp.EventOutputTextDelta {
			text.WriteString(ev.Delta)
		}
		if ev.IsTerminal() {
			e := ev
			terminal = &e
		}
	}

	// Streaming §3: exactly one terminal event, carrying the complete final
	// response, so a client that missed everything before it still renders the
	// right answer.
	if terminal == nil {
		t.Fatal("the stream carried no terminal event")
	}
	if terminal.Response == nil || terminal.Response.Status != uhp.StatusCompleted {
		t.Fatalf("terminal event = %+v", terminal)
	}
	if text.String() == "" {
		t.Error("no text arrived as deltas")
	}
	// Both paths must produce the same output. A server where they disagree is
	// one where the answer depends on how you asked for it.
	if got := responseTextOf(terminal.Response); got != text.String() {
		t.Errorf("the streamed text %q and the final response %q disagree", text.String(), got)
	}
}

// Files, over the socket: upload, reference by id, and read the artifact back.
func TestPublishedClientRoundTripsAFile(t *testing.T) {
	c, _ := newLiveServer(t, "")
	ctx := context.Background()

	file, err := c.Upload(ctx, "notes.txt", strings.NewReader("some notes"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if file.ID == "" || file.Bytes != int64(len("some notes")) {
		t.Fatalf("upload = %+v", file)
	}

	resp, err := c.Create(ctx, uhp.CreateResponseRequest{
		Input: []any{
			map[string]any{"type": "input_text", "text": "read it"},
			map[string]any{"type": "input_file", "file_id": file.ID},
		},
		Metadata: map[string]any{"harness_id": "chrn_echo"},
	}, "key-e2e-file")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The file item survives to input_items, which is the property that makes
	// the endpoint worth having: it is exactly what a rebuild from the
	// flattened prompt would have lost.
	items, err := c.InputItems(ctx, resp.ID)
	if err != nil {
		t.Fatalf("input items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("input items = %v, want both the text and the file", items)
	}
	var second map[string]any
	if err := json.Unmarshal(items[1], &second); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	if second["file_id"] != file.ID {
		t.Errorf("second item = %v, want the uploaded file id", second)
	}
}

func responseTextOf(resp *uhp.Response) string {
	var b strings.Builder
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}
