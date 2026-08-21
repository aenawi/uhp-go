// Package http implements UHP's wire format: GET /v1/harnesses (discovery),
// POST /v1/responses (create + optional SSE stream), GET /v1/responses/{id}
// (retrieve), POST /v1/responses/{id}/cancel (cancellation). This layer only
// knows HTTP <-> domain mapping; all logic lives in internal/service.
package http

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aenawi/uhp-go/internal/service"
)

type Server struct {
	mux          *http.ServeMux
	tasks        *service.TaskService
	log          *slog.Logger
	apiKeys      []string
	maxBodyBytes int64

	// keepAlive is how long a stream may stay silent before it writes a
	// comment line. It is a field rather than a constant only so a test does
	// not have to wait out the real interval; nothing configures it, because
	// the number it has to beat is fixed by the protocol and not by a
	// deployment.
	keepAlive time.Duration
}

// defaultMaxBodyBytes bounds a request body when none is configured.
const defaultMaxBodyBytes = 8 << 20

// defaultKeepAlive is how often a stream with nothing to say writes a comment
// line. Errors §5 asks for one at least every 30 seconds; half of that leaves
// room for a comment to be delayed or dropped without the client's inactivity
// timeout firing on a run that is still working.
const defaultKeepAlive = 15 * time.Second

func NewServer(tasks *service.TaskService, log *slog.Logger, apiKeys []string, maxBodyBytes int64) *Server {
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	s := &Server{
		mux:          http.NewServeMux(),
		tasks:        tasks,
		log:          log,
		apiKeys:      apiKeys,
		maxBodyBytes: maxBodyBytes,
		keepAlive:    defaultKeepAlive,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	// Discovery is deliberately unauthenticated: a client must be able to
	// learn whether this is a UHP server before deciding what credential to
	// present (Lifecycle §2). The document carries nothing principal-specific.
	s.mux.HandleFunc("GET /v1/uhp", withVersion(s.handleDiscovery))

	s.mux.HandleFunc("GET /v1/harnesses", withVersion(s.withAuth(s.handleListHarnesses)))
	s.mux.HandleFunc("POST /v1/harnesses", withVersion(s.withAuth(s.handleCreateHarness)))
	s.mux.HandleFunc("PUT /v1/harnesses/{id}", withVersion(s.withAuth(s.handleReplaceHarness)))
	s.mux.HandleFunc("PATCH /v1/harnesses/{id}", withVersion(s.withAuth(s.handlePatchHarness)))
	s.mux.HandleFunc("DELETE /v1/harnesses/{id}", withVersion(s.withAuth(s.handleDeleteHarness)))
	s.mux.HandleFunc("GET /v1/harnesses/{id}", withVersion(s.withAuth(s.handleGetHarness)))
	s.mux.HandleFunc("GET /v1/harnesses/{id}/models", withVersion(s.withAuth(s.handleHarnessModels)))
	s.mux.HandleFunc("GET /v1/harnesses/{id}/skills/{skill_id}/files",
		withVersion(s.withAuth(s.handleHarnessSkillFiles)))
	s.mux.HandleFunc("GET /v1/models", withVersion(s.withAuth(s.handleListModels)))

	s.mux.HandleFunc("GET /v1/sessions", withVersion(s.withAuth(s.handleListSessions)))
	s.mux.HandleFunc("GET /v1/sessions/{id}", withVersion(s.withAuth(s.handleGetSession)))
	s.mux.HandleFunc("GET /v1/sessions/{id}/turns", withVersion(s.withAuth(s.handleSessionTurns)))
	s.mux.HandleFunc("POST /v1/sessions/{id}/cancel", withVersion(s.withAuth(s.handleCancelSession)))

	s.mux.HandleFunc("GET /v1/sessions/{id}/files", withVersion(s.withAuth(s.handleSessionFiles)))
	s.mux.HandleFunc("GET /v1/sessions/{id}/files/archive", withVersion(s.withAuth(s.handleSessionArchive)))

	s.mux.HandleFunc("POST /v1/files", withVersion(s.withAuth(s.handleUploadFile)))
	s.mux.HandleFunc("GET /v1/containers/{container_id}/files/{file_id}/content",
		withVersion(s.withAuth(s.handleDownloadArtifact)))
	s.mux.HandleFunc("GET /v1/containers/{container_id}/files/{file_id}/pdf",
		withVersion(s.withAuth(s.handlePreviewArtifact)))

	s.mux.HandleFunc("POST /v1/responses", withVersion(s.withAuth(s.handleCreateTask)))
	s.mux.HandleFunc("GET /v1/responses/{id}", withVersion(s.withAuth(s.handleGetTask)))
	s.mux.HandleFunc("POST /v1/responses/{id}/cancel", withVersion(s.withAuth(s.handleCancelTask)))

	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
}

// Handler returns the routed handler, wrapped so that a path containing a dot
// segment never reaches routing.
func (s *Server) Handler() http.Handler { return refuseDotSegments(s.mux) }

// refuseDotSegments answers 404 to any request whose path contains a "." or
// ".." segment, or an empty one.
//
// Without it, net/http's own router answers a traversal probe such as
// /v1/containers/cntr_x/files/../../etc/passwd/content with a 301 to the
// cleaned path. That is not an exploit, but it is not a refusal either: a
// client — or a conformance suite — sees a redirect where it asked whether the
// server refuses traversal, and the honest answer to "is there a file called
// ../../etc/passwd here" is that there is not.
//
// Empty interior segments are refused for the same reason: "....//...." cleans
// to "../.." and would otherwise be answered with the same misleading redirect.
// A trailing slash is left alone, because it is not a traversal and net/http's
// redirect for it is the behaviour clients already have.
func refuseDotSegments(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(r.URL.EscapedPath(), "/")
		for i, seg := range segments {
			decoded, err := url.PathUnescape(seg)
			if err != nil {
				decoded = seg
			}
			interior := i > 0 && i < len(segments)-1
			if decoded == "." || decoded == ".." || (decoded == "" && interior) {
				// The guard covers every route, so the code cannot claim the
				// request was about a file. Errors §3 has no entry for "that
				// path shape is refused", hence the vendor prefix.
				writeError(w, http.StatusNotFound, typeInvalidRequest, "uhpgo_invalid_path",
					"the request path contains a dot or empty segment")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withAuth enforces bearer-token auth (UHP "Security" chapter). If no keys
// are configured, auth is skipped — useful for local dev only.
//
// Every configured key is equivalent: this server has one principal, so
// "scope file access to the owning principal" (Files §5) is satisfied by
// requiring a key at all. A deployment that needs several tenants needs a
// principal on the credential first, and artifact lookup would then have to
// filter by it.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.apiKeys) == 0 {
			next(w, r)
			return
		}
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, typeAuthentication, "missing_credential",
				"an Authorization: Bearer <key> header is required")
			return
		}
		if !s.validKey(token) {
			writeError(w, http.StatusUnauthorized, typeAuthentication, "invalid_credential",
				"the provided API key is not recognized")
			return
		}
		next(w, r)
	}
}

// bearerToken extracts the credential from an Authorization header.
//
// RFC 7235 defines the auth scheme as case-insensitive, so a conformant client
// sending "bearer <key>" must be accepted; matching "Bearer " exactly rejected
// it.
func bearerToken(header string) (string, bool) {
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	token := strings.TrimSpace(header[len(scheme):])
	return token, token != ""
}

// validKey compares in constant time.
//
// A map lookup returns as soon as it finds a mismatching byte, which leaks the
// length of the shared prefix between the presented token and a real key. Every
// configured key is compared so the work does not depend on which one matches.
func (s *Server) validKey(token string) bool {
	var ok bool
	for _, k := range s.apiKeys {
		if subtle.ConstantTimeCompare([]byte(token), []byte(k)) == 1 {
			ok = true
		}
	}
	return ok
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// createTaskBody is the OpenAI-Responses-shaped request body UHP requires
// conformant servers to accept, plus the metadata.harness_id extension.
type createTaskBody struct {
	// Input is left raw: UHP allows either a string or an array of items, and
	// a `string` field here made every task carrying a file fail to unmarshal
	// and come back 400 invalid_input. See input.go.
	Input              json.RawMessage `json:"input"`
	Model              string          `json:"model"`
	Stream             bool            `json:"stream"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Metadata           map[string]any  `json:"metadata,omitempty"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	// Without a bound, a single request can drive the server out of memory,
	// and auth is off by default.
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)

	var body createTaskBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if writeIfTooLarge(w, err, s.maxBodyBytes) {
			return
		}
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", "the request body could not be parsed as JSON")
		return
	}
	input, err := parseInput(body.Input)
	if err != nil {
		var bad badInputError
		if errors.As(err, &bad) {
			writeErrorParam(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", bad.msg, bad.param)
			return
		}
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", err.Error())
		return
	}
	harnessID, _ := body.Metadata["harness_id"].(string)
	if harnessID == "" {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", "metadata.harness_id is required")
		return
	}

	task, run, err := s.tasks.StartTask(r.Context(), service.CreateTaskRequest{
		Input:              input.Text,
		Model:              body.Model,
		HarnessID:          harnessID,
		PreviousResponseID: body.PreviousResponseID,
		Metadata:           body.Metadata,
		Attachments:        input.Attachments,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	if !body.Stream {
		final, err := s.waitForResult(r.Context(), task, run)
		if err != nil {
			// The client went away. The run is unaffected and still on its way
			// to a terminal state; there is simply nobody left to tell.
			return
		}
		writeJSON(w, http.StatusOK, final)
		return
	}

	s.streamSSE(w, r, run)
}
