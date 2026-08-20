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
	"strings"

	"github.com/aenawi/uhp-go/internal/service"
)

type Server struct {
	mux          *http.ServeMux
	tasks        *service.TaskService
	log          *slog.Logger
	apiKeys      []string
	maxBodyBytes int64
}

// defaultMaxBodyBytes bounds a request body when none is configured.
const defaultMaxBodyBytes = 8 << 20

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
	s.mux.HandleFunc("GET /v1/harnesses/{id}", withVersion(s.withAuth(s.handleGetHarness)))
	s.mux.HandleFunc("GET /v1/harnesses/{id}/models", withVersion(s.withAuth(s.handleHarnessModels)))
	s.mux.HandleFunc("GET /v1/models", withVersion(s.withAuth(s.handleListModels)))

	s.mux.HandleFunc("POST /v1/responses", withVersion(s.withAuth(s.handleCreateTask)))
	s.mux.HandleFunc("GET /v1/responses/{id}", withVersion(s.withAuth(s.handleGetTask)))
	s.mux.HandleFunc("POST /v1/responses/{id}/cancel", withVersion(s.withAuth(s.handleCancelTask)))

	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
}

func (s *Server) Handler() http.Handler { return s.mux }

// withAuth enforces bearer-token auth (UHP "Security" chapter). If no keys
// are configured, auth is skipped — useful for local dev only.
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
	Input              string         `json:"input"`
	Model              string         `json:"model"`
	Stream             bool           `json:"stream"`
	PreviousResponseID string         `json:"previous_response_id,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	// Without a bound, a single request can drive the server out of memory,
	// and auth is off by default.
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)

	var body createTaskBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErrorDetail(w, http.StatusRequestEntityTooLarge, typeInvalidRequest, "file_too_large",
				"the request body exceeds this server's limit",
				map[string]any{"max_bytes": s.maxBodyBytes})
			return
		}
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", "the request body could not be parsed as JSON")
		return
	}
	if body.Input == "" {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", "field 'input' is required")
		return
	}
	harnessID, _ := body.Metadata["harness_id"].(string)
	if harnessID == "" {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", "metadata.harness_id is required")
		return
	}

	task, run, err := s.tasks.StartTask(r.Context(), service.CreateTaskRequest{
		Input:              body.Input,
		Model:              body.Model,
		HarnessID:          harnessID,
		PreviousResponseID: body.PreviousResponseID,
		Metadata:           body.Metadata,
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
