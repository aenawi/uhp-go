package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
)

// The tests that matter here are the negative ones.
//
// A share is a bearer capability handed to someone who has no credential, so
// the happy path — "the link shows the conversation" — would pass just as well
// against an implementation that shares write access, serves a revoked link, or
// hands a viewer another session's files. Each of those is below.

// newShareServer is a server with sharing on, an API key required, and a
// workspace, so that the anonymous paths are genuinely anonymous rather than
// anonymous on a server that never asks anyone for anything.
func newShareServer(t *testing.T, key string) (*Server, *producingAdapter) {
	t.Helper()
	a := &producingAdapter{produce: map[string]string{"report.md": "artifact-ok"}}
	reg := harness.NewRegistry()
	reg.Register(a)
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		service.WithWorkspace(t.TempDir()),
		service.WithSessionSharing(),
		service.WithPublicBaseURL("https://uhp.example.com"))
	var keys []string
	if key != "" {
		keys = []string{key}
	}
	return NewServer(svc, slog.Default(), keys, 0), a
}

// asOwner issues a request with the API key, the way the session's owner does.
func asOwner(t *testing.T, srv *Server, key, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

// anonymously issues a request with no credential at all, the way a viewer
// holding a link does.
func anonymously(t *testing.T, srv *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

// runSharedTask runs one task on the share server and returns its session id.
func runSharedTask(t *testing.T, srv *Server, key string) string {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/responses",
		strings.NewReader(`{"input":"hi","metadata":{"harness_id":"writer"}}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("create task: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return sessionID(t, resp)
}

// share mints a share and returns its id and the URL the server published.
func share(t *testing.T, srv *Server, key, sessID string) (string, string) {
	t.Helper()
	w := asOwner(t, srv, key, "POST", "/v1/sessions/"+sessID+"/share")
	if w.Code != 200 {
		t.Fatalf("share session: %d %s", w.Code, w.Body.String())
	}
	var sh map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &sh); err != nil {
		t.Fatalf("unmarshal share: %v", err)
	}
	id, _ := sh["id"].(string)
	url, _ := sh["url"].(string)
	if id == "" {
		t.Fatalf("share carries no id: %v", sh)
	}
	return id, url
}

func TestSharedViewServesTheConversationToSomeoneWithNoCredential(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")
	shareID, url := share(t, srv, "k1", sessID)

	if want := "https://uhp.example.com/v1/shares/" + shareID; url != want {
		t.Errorf("share url = %q, want %q", url, want)
	}

	w := anonymously(t, srv, "GET", "/v1/shares/"+shareID)
	if w.Code != 200 {
		t.Fatalf("anonymous read: %d %s", w.Code, w.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal view: %v", err)
	}
	if view["object"] != "session.shared" {
		t.Errorf("object = %v", view["object"])
	}
	sess, _ := view["session"].(map[string]any)
	if sess == nil || sess["id"] != sessID {
		t.Fatalf("shared view names session %v, want %s", view["session"], sessID)
	}

	turns := anonymously(t, srv, "GET", "/v1/shares/"+shareID+"/turns")
	if turns.Code != 200 || !strings.Contains(turns.Body.String(), `"turns"`) {
		t.Fatalf("anonymous turns: %d %s", turns.Code, turns.Body.String())
	}
	files := anonymously(t, srv, "GET", "/v1/shares/"+shareID+"/files")
	if files.Code != 200 || !strings.Contains(files.Body.String(), "report.md") {
		t.Fatalf("anonymous files: %d %s", files.Code, files.Body.String())
	}
}

// The load-bearing half of "shared views must be read-only": a share id is not
// a credential, so presenting it as one buys nothing at all.
func TestAShareIDIsNotACredential(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")
	shareID, _ := share(t, srv, "k1", sessID)

	// The three things Sessions §5 says a shared view must not be able to do,
	// attempted with the share id in the place a credential goes.
	for _, tc := range []struct{ name, method, path string }{
		{"continue the conversation", "POST", "/v1/responses"},
		{"cancel the session", "POST", "/v1/sessions/" + sessID + "/cancel"},
		{"upload a file", "POST", "/v1/files"},
		{"delete the trace", "DELETE", "/v1/traces/" + sessID},
		{"read the session directly", "GET", "/v1/sessions/" + sessID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path,
				strings.NewReader(`{"input":"hi","metadata":{"harness_id":"writer"}}`))
			req.Header.Set("Authorization", "Bearer "+shareID)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != 401 {
				t.Fatalf("%s %s with a share id = %d, want 401: %s",
					tc.method, tc.path, w.Code, w.Body.String())
			}
		})
	}
}

// The other half: there is no write route under /v1/shares/ to reach, so the
// router refuses the method rather than a handler refusing the intent.
func TestTheSharedSurfaceHasNoWriteRoutes(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")
	shareID, _ := share(t, srv, "k1", sessID)

	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		w := anonymously(t, srv, method, "/v1/shares/"+shareID)
		if w.Code != 405 {
			t.Errorf("%s /v1/shares/{id} = %d, want 405: %s", method, w.Code, w.Body.String())
		}
	}
	// And nothing hangs off the shared prefix that was not registered.
	if w := anonymously(t, srv, "GET", "/v1/shares/"+shareID+"/cancel"); w.Code != 404 {
		t.Errorf("GET a route that does not exist = %d, want 404", w.Code)
	}
}

// Sessions §5: "Servers must support revocation."
func TestRevokingAShareStopsTheLinkWorking(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")
	shareID, _ := share(t, srv, "k1", sessID)

	if w := asOwner(t, srv, "k1", "DELETE", "/v1/sessions/"+sessID+"/share"); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	} else {
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal revoke: %v", err)
		}
		// The id in the envelope is the share's: the session is still there.
		if body["id"] != shareID || body["deleted"] != true {
			t.Errorf("revoke body = %v, want the share id and deleted:true", body)
		}
	}

	for _, path := range []string{"", "/turns", "/files"} {
		w := anonymously(t, srv, "GET", "/v1/shares/"+shareID+path)
		if w.Code != 404 {
			t.Errorf("a revoked share still serves %q: %d", path, w.Code)
		}
	}
	// The session itself is untouched.
	if w := asOwner(t, srv, "k1", "GET", "/v1/sessions/"+sessID); w.Code != 200 {
		t.Errorf("revoking a share deleted the session: %d", w.Code)
	}
	// And revoking again says there was nothing left to revoke.
	if w := asOwner(t, srv, "k1", "DELETE", "/v1/sessions/"+sessID+"/share"); w.Code != 404 {
		t.Errorf("second revoke = %d, want 404", w.Code)
	}
}

func TestDeletingTheTraceRevokesItsShare(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")
	shareID, _ := share(t, srv, "k1", sessID)

	if w := asOwner(t, srv, "k1", "DELETE", "/v1/traces/"+sessID); w.Code != 200 {
		t.Fatalf("delete trace: %d %s", w.Code, w.Body.String())
	}
	if w := anonymously(t, srv, "GET", "/v1/shares/"+shareID); w.Code != 404 {
		t.Fatalf("the deleted session's link still works: %d %s", w.Code, w.Body.String())
	}
}

// A second POST returns the share that exists. Two live ids for one session
// would mean a client revoking the one it was told about and leaving another
// behind.
func TestSharingASessionTwiceReturnsOneShare(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")

	first, _ := share(t, srv, "k1", sessID)
	second, _ := share(t, srv, "k1", sessID)
	if first != second {
		t.Fatalf("two POSTs minted two ids: %s and %s", first, second)
	}

	// And GET reports the same one.
	w := asOwner(t, srv, "k1", "GET", "/v1/sessions/"+sessID+"/share")
	if w.Code != 200 {
		t.Fatalf("read share: %d %s", w.Code, w.Body.String())
	}
	var sh map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &sh); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sh["id"] != first {
		t.Errorf("GET reports %v, want %s", sh["id"], first)
	}
}

// An unshared session is a 404 with a code that says which thing was not there,
// so a client can tell "you have not shared this" from "no such session".
func TestReadingTheShareOfAnUnsharedSession(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")

	w := asOwner(t, srv, "k1", "GET", "/v1/sessions/"+sessID+"/share")
	if w.Code != 404 {
		t.Fatalf("unshared session = %d, want 404", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := errorCode(t, body); got != "uhpgo_share_not_found" {
		t.Errorf("code = %q", got)
	}

	if w := asOwner(t, srv, "k1", "GET", "/v1/sessions/sess_nope/share"); w.Code != 404 {
		t.Errorf("unknown session = %d, want 404", w.Code)
	}
}

// A share id is a secret in a URL, so a malformed one must be refused before it
// reaches a lookup, and every miss must look the same from outside.
func TestAMalformedShareIDIsRefusedTheSameWayAnUnknownOneIs(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	for _, id := range []string{
		"shr_short",
		"sess_1234",
		"shr_" + strings.Repeat("z", 64),
		strings.Repeat("a", 200),
	} {
		w := anonymously(t, srv, "GET", "/v1/shares/"+id)
		if w.Code != 404 {
			t.Errorf("GET /v1/shares/%s = %d, want 404: %s", id, w.Code, w.Body.String())
		}
	}
}

// The share's artifact path is scoped by the share, not by a container id the
// caller supplies — so another session's file id, which is a real id this
// server minted, resolves to nothing through this link.
func TestASharedLinkCannotReachAnotherSessionsFiles(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sharedSession := runSharedTask(t, srv, "k1")
	otherSession := runSharedTask(t, srv, "k1")

	shareID, _ := share(t, srv, "k1", sharedSession)

	ownFile := firstFileID(t, srv, "k1", sharedSession)
	otherFile := firstFileID(t, srv, "k1", otherSession)
	if ownFile == otherFile {
		t.Fatal("the two sessions produced the same file id; the test proves nothing")
	}

	own := anonymously(t, srv, "GET", "/v1/shares/"+shareID+"/files/"+ownFile+"/content")
	if own.Code != 200 || own.Body.String() != "artifact-ok" {
		t.Fatalf("the share's own file: %d %q", own.Code, own.Body.String())
	}
	if own.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("a shared artifact was served without nosniff")
	}

	leak := anonymously(t, srv, "GET", "/v1/shares/"+shareID+"/files/"+otherFile+"/content")
	if leak.Code != 404 {
		t.Fatalf("a share served another session's file: %d %q", leak.Code, leak.Body.String())
	}
}

// The credential is in the URL, so the three headers that keep a URL from
// spreading are on every anonymous response.
func TestSharedResponsesAreNotCachedOrIndexed(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")
	shareID, _ := share(t, srv, "k1", sessID)
	fileID := firstFileID(t, srv, "k1", sessID)

	for _, path := range []string{
		"/v1/shares/" + shareID,
		"/v1/shares/" + shareID + "/turns",
		"/v1/shares/" + shareID + "/files",
		"/v1/shares/" + shareID + "/files/" + fileID + "/content",
	} {
		w := anonymously(t, srv, "GET", path)
		if w.Code != 200 {
			t.Fatalf("GET %s = %d", path, w.Code)
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Errorf("%s: Cache-Control = %q", path, w.Header().Get("Cache-Control"))
		}
		if !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
			t.Errorf("%s: X-Robots-Tag = %q", path, w.Header().Get("X-Robots-Tag"))
		}
		if w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q", path, w.Header().Get("Referrer-Policy"))
		}
	}
}

// Harnesses §4.1 — "a server must never return a resolved credential to a
// client" — where "a client" includes someone who presented nothing.
func TestASharedViewCarriesNoMcpCredential(t *testing.T) {
	hs, err := store.NewFileHarnesses(filepath.Join(t.TempDir(), "harnesses.json"))
	if err != nil {
		t.Fatalf("harness store: %v", err)
	}
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default(),
		service.WithHarnessStore(hs), service.WithSessionSharing(),
		service.WithWorkspace(t.TempDir()))
	srv := NewServer(svc, slog.Default(), nil, 0)

	// Both spellings of a credential. `auth` is the one Harnesses §4.1 names;
	// `headers` is the same secret one field along, and is where writeMcpConfig
	// materialises the resolved `auth` anyway — so a projection that stripped
	// only the first would still hand a link holder a working key.
	body := `{"name":"x","base":"echo","system_prompt":"the operator's standing instructions",
		"skills":[{"name":"internal","content":"# internal\nprivate runbook"}],
		"mcp_servers":[{"name":"vault","url":"https://mcp.example.invalid/mcp","transport":"http",
		"auth":"secret-auth-token","headers":{"X-Api-Key":"secret-header-token"}}]}`
	status, created := callJSON(t, srv, "POST", "/v1/harnesses", body)
	if status != 200 {
		t.Fatalf("create harness: %d %v", status, created)
	}
	harnessID, _ := created["id"].(string)

	resp := runFileTask(t, srv, `{"input":"hi","metadata":{"harness_id":"`+harnessID+`"}}`)
	sessID := sessionID(t, resp)

	w := do(t, srv, "POST", "/v1/sessions/"+sessID+"/share", nil)
	if w.Code != 200 {
		t.Fatalf("share: %d %s", w.Code, w.Body.String())
	}
	var sh map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &sh); err != nil {
		t.Fatalf("unmarshal share: %v", err)
	}

	view := anonymously(t, srv, "GET", "/v1/shares/"+sh["id"].(string))
	if view.Code != 200 {
		t.Fatalf("shared view: %d %s", view.Code, view.Body.String())
	}
	// The harness is in the answer, so the checks below are looking at
	// something rather than at an absent field.
	if !strings.Contains(view.Body.String(), harnessID) {
		t.Fatalf("the shared view carries no harness to check: %s", view.Body.String())
	}
	for _, secret := range []string{
		"secret-auth-token",     // the credential the chapter names
		"secret-header-token",   // the credential one field along
		"X-Api-Key",             // and the header it was carried under
		"standing instructions", // the operator's prompt
		"private runbook",       // a skill bundle's contents
		"mcp.example.invalid",   // where this deployment reaches out to
	} {
		if strings.Contains(view.Body.String(), secret) {
			t.Errorf("a shared view carried %q: %s", secret, view.Body.String())
		}
	}

	// And what it does keep, so an over-eager projection that answered with an
	// empty harness object would not pass this test by carrying nothing.
	var decoded struct {
		Harness struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Base       string   `json:"base"`
			Status     string   `json:"status"`
			McpServers []any    `json:"mcpServers"`
			Skills     []any    `json:"skills"`
			Prompt     string   `json:"systemPrompt"`
			Disabled   []string `json:"disabledTools"`
		} `json:"harness"`
	}
	if err := json.Unmarshal(view.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal view: %v", err)
	}
	h := decoded.Harness
	if h.ID != harnessID || h.Name != "x" || h.Base != "echo" || h.Status == "" {
		t.Errorf("the shared harness lost its identity: %+v", h)
	}
	// Empty arrays rather than absent fields: a viewer is told this view
	// carries none, not left to wonder whether they were dropped in transit.
	if len(h.McpServers) != 0 || len(h.Skills) != 0 || len(h.Disabled) != 0 || h.Prompt != "" {
		t.Errorf("the shared harness carried configuration: %+v", h)
	}
}

// A deployment that has not opted in reports the capability as false and
// refuses every share endpoint the same way, rather than one of them 404ing
// because the route happens not to exist.
func TestSharingIsRefusedWhenTheServerHasNotEnabledIt(t *testing.T) {
	srv, _ := newFileServer(t)
	resp := runFileTask(t, srv, `{"input":"hi","metadata":{"harness_id":"writer"}}`)
	sessID := sessionID(t, resp)

	d := do(t, srv, "GET", "/v1/uhp", nil)
	var discovery map[string]any
	if err := json.Unmarshal(d.Body.Bytes(), &discovery); err != nil {
		t.Fatalf("unmarshal discovery: %v", err)
	}
	caps, _ := discovery["capabilities"].(map[string]any)
	if caps["session_sharing"] != false {
		t.Fatalf("session_sharing = %v, want false", caps["session_sharing"])
	}

	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/sessions/" + sessID + "/share"},
		{"GET", "/v1/sessions/" + sessID + "/share"},
		{"DELETE", "/v1/sessions/" + sessID + "/share"},
		{"GET", "/v1/shares/shr_" + strings.Repeat("ab", 32)},
	} {
		w := do(t, srv, tc.method, tc.path, nil)
		if w.Code != 501 {
			t.Errorf("%s %s = %d, want 501: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := errorCode(t, body); got != "uhpgo_session_sharing_unsupported" {
			t.Errorf("%s %s: code = %q", tc.method, tc.path, got)
		}
	}
}

func TestDiscoveryReportsSharingWhenItIsOn(t *testing.T) {
	srv, _ := newShareServer(t, "")
	w := do(t, srv, "GET", "/v1/uhp", nil)
	var discovery map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &discovery); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	caps, _ := discovery["capabilities"].(map[string]any)
	if caps["session_sharing"] != true {
		t.Fatalf("session_sharing = %v, want true", caps["session_sharing"])
	}
}

// firstFileID reads a session's artifact listing as its owner and returns the
// first file's id.
func firstFileID(t *testing.T, srv *Server, key, sessID string) string {
	t.Helper()
	w := asOwner(t, srv, key, "GET", "/v1/sessions/"+sessID+"/files")
	if w.Code != 200 {
		t.Fatalf("session files: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal files: %v", err)
	}
	if len(body.Files) == 0 {
		t.Fatalf("session %s produced no artifacts", sessID)
	}
	return body.Files[0].ID
}

// orphanShareStore resolves one share id to a session it does not hold.
//
// It exists because the state cannot be reached through the API any more:
// CreateShare refuses a share for an absent session, and DeleteSession revokes
// the share it finds. What is left is a repair, a restore from a half-old
// backup, or a future engine with a bug — and the point of the test below is
// that the service checks the session behind a share on every read rather than
// trusting the store to have kept the two in step.
type orphanShareStore struct {
	service.Store
	share domain.Share
}

func (o orphanShareStore) GetShare(_ context.Context, id string) (*domain.Share, bool, error) {
	if id != o.share.ID {
		return nil, false, nil
	}
	cp := o.share
	return &cp, true, nil
}

// A share whose session is gone is the same 404, with the same code, on every
// one of the four anonymous paths.
//
// The turns and files paths are the ones this catches. Both resolve the share
// and then call a session method, which looks the session up a second time — so
// without a rewrite they would answer `session_not_found` where every other
// miss on this surface answers `uhpgo_share_not_found`. That is an error a
// client was told could not happen here, and a statement about a session the
// caller never named.
func TestAShareWhoseSessionVanishedIsJustAMiss(t *testing.T) {
	orphan := "shr_" + strings.Repeat("ab", 32)
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	svc := service.NewTaskService(reg,
		orphanShareStore{
			Store: store.NewMemoryStore(),
			share: domain.Share{ID: orphan, SessionID: "sess_gone", CreatedAt: 1},
		},
		slog.Default(), service.WithSessionSharing())
	srv := NewServer(svc, slog.Default(), nil, 0)

	for _, path := range []string{"", "/turns", "/files"} {
		w := anonymously(t, srv, "GET", "/v1/shares/"+orphan+path)
		if w.Code != 404 {
			t.Fatalf("an orphaned share resolved %q: %d %s", path, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal %q: %v", path, err)
		}
		if got := errorCode(t, body); got != "uhpgo_share_not_found" {
			t.Errorf("%q: code = %q, want uhpgo_share_not_found", path, got)
		}
	}
}

// The three headers are on failures too, and that is the case that matters
// most: an error response is reached by the same URL, so a crawler indexing the
// address of a revoked link is exactly what X-Robots-Tag is there to stop.
func TestSharedErrorResponsesCarryTheSameHeaders(t *testing.T) {
	srv, _ := newShareServer(t, "k1")
	sessID := runSharedTask(t, srv, "k1")
	shareID, _ := share(t, srv, "k1", sessID)

	// Revoked, so every path below is a miss on a URL that used to work.
	if w := asOwner(t, srv, "k1", "DELETE", "/v1/sessions/"+sessID+"/share"); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}

	for _, path := range []string{
		"/v1/shares/" + shareID,
		"/v1/shares/" + shareID + "/turns",
		"/v1/shares/" + shareID + "/files",
		"/v1/shares/" + shareID + "/files/file_whatever/content",
		"/v1/shares/shr_not_a_real_id",
	} {
		w := anonymously(t, srv, "GET", path)
		if w.Code != 404 {
			t.Fatalf("GET %s = %d, want 404", path, w.Code)
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Errorf("%s: Cache-Control = %q", path, w.Header().Get("Cache-Control"))
		}
		if !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
			t.Errorf("%s: X-Robots-Tag = %q", path, w.Header().Get("X-Robots-Tag"))
		}
		if w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q", path, w.Header().Get("Referrer-Policy"))
		}
	}
}
