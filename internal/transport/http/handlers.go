// Package http implements UHP's wire format: GET /v1/harnesses (discovery),
// POST /v1/responses (create + optional SSE stream), GET /v1/responses/{id}
// (retrieve), POST /v1/responses/{id}/cancel (cancellation). This layer only
// knows HTTP <-> domain mapping; all logic lives in internal/service.
package http

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aenawi/uhp-go/internal/service"
)

type Server struct {
	mux          *http.ServeMux
	tasks        TaskService
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

func NewServer(tasks TaskService, log *slog.Logger, apiKeys []string, maxBodyBytes int64) *Server {
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
	s.mux.HandleFunc("GET /v1/harnesses/{id}/events", withVersion(s.withAuth(s.handleHarnessEvents)))
	s.mux.HandleFunc("GET /v1/harnesses/{id}/skills/{skill_id}/files",
		withVersion(s.withAuth(s.handleHarnessSkillFiles)))
	s.mux.HandleFunc("GET /v1/models", withVersion(s.withAuth(s.handleListModels)))

	s.mux.HandleFunc("GET /v1/sessions", withVersion(s.withAuth(s.handleListSessions)))
	s.mux.HandleFunc("GET /v1/sessions/{id}", withVersion(s.withAuth(s.handleGetSession)))
	s.mux.HandleFunc("GET /v1/sessions/{id}/turns", withVersion(s.withAuth(s.handleSessionTurns)))
	s.mux.HandleFunc("POST /v1/sessions/{id}/cancel", withVersion(s.withAuth(s.handleCancelSession)))
	// Deletion lives under /v1/traces/{session_id}, not /v1/sessions/{id}.
	// That is the specification's path (Sessions §6) and not a second resource:
	// the id is the one every route above reads with, and what disappears is
	// what they were reading. The two spellings are UHP's, not this server's to
	// reconcile.
	s.mux.HandleFunc("DELETE /v1/traces/{id}", withVersion(s.withAuth(s.handleDeleteSession)))

	s.mux.HandleFunc("GET /v1/sessions/{id}/files", withVersion(s.withAuth(s.handleSessionFiles)))
	s.mux.HandleFunc("GET /v1/sessions/{id}/files/archive", withVersion(s.withAuth(s.handleSessionArchive)))

	// Sharing (Sessions §5). Minting, reading and revoking a share belong to
	// the principal that owns the session, so all three are behind auth.
	// DELETE is this server's reading: the chapter requires revocation and
	// names no endpoint for it.
	s.mux.HandleFunc("POST /v1/sessions/{id}/share", withVersion(s.withAuth(s.handleShareSession)))
	s.mux.HandleFunc("GET /v1/sessions/{id}/share", withVersion(s.withAuth(s.handleGetSessionShare)))
	s.mux.HandleFunc("DELETE /v1/sessions/{id}/share", withVersion(s.withAuth(s.handleRevokeShare)))

	// The shared views, and the only routes in this table that answer a caller
	// who presented no credential.
	//
	// They are in one block, under one prefix, and every one of them is a GET.
	// That is what makes "shared views must be read-only" a property of the
	// routing table rather than a rule spread over four handlers: there is no
	// write path here to refuse, so POST, PATCH and DELETE under /v1/shares/
	// are answered 405 by the router itself, and a share id presented as a
	// bearer token to any of the routes above is an unknown credential. A
	// future endpoint cannot forget a check that does not exist. Discovery is
	// unauthenticated too, but carries nothing principal-specific; these carry
	// one conversation, which is why the id that reaches them is 256 bits.
	s.mux.HandleFunc("GET /v1/shares/{share_id}",
		withVersion(withSharedHeaders(s.handleSharedSession)))
	s.mux.HandleFunc("GET /v1/shares/{share_id}/turns",
		withVersion(withSharedHeaders(s.handleSharedTurns)))
	s.mux.HandleFunc("GET /v1/shares/{share_id}/files",
		withVersion(withSharedHeaders(s.handleSharedFiles)))
	s.mux.HandleFunc("GET /v1/shares/{share_id}/files/{file_id}/content",
		withVersion(withSharedHeaders(s.handleSharedArtifact)))

	s.mux.HandleFunc("POST /v1/files", withVersion(s.withAuth(s.handleUploadFile)))
	s.mux.HandleFunc("GET /v1/containers/{container_id}/files/{file_id}/content",
		withVersion(s.withAuth(s.handleDownloadArtifact)))
	s.mux.HandleFunc("GET /v1/containers/{container_id}/files/{file_id}/pdf",
		withVersion(s.withAuth(s.handlePreviewArtifact)))

	s.mux.HandleFunc("POST /v1/responses", withVersion(s.withAuth(s.handleCreateTask)))
	s.mux.HandleFunc("GET /v1/responses/{id}", withVersion(s.withAuth(s.handleGetTask)))
	s.mux.HandleFunc("GET /v1/responses/{id}/input_items", withVersion(s.withAuth(s.handleTaskInputItems)))
	s.mux.HandleFunc("DELETE /v1/responses/{id}", withVersion(s.withAuth(s.handleDeleteTask)))
	s.mux.HandleFunc("POST /v1/responses/{id}/cancel", withVersion(s.withAuth(s.handleCancelTask)))

	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
}

// Handler returns the routed handler, wrapped so that a path containing a dot
// segment never reaches routing, and so the router's own 404 and 405 carry the
// error envelope every non-2xx owes a client.
func (s *Server) Handler() http.Handler {
	return refuseDotSegments(withRoutingErrors(s.mux))
}

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
// A trailing slash is left alone, because it is not a traversal: net/http
// matches it against the routing table like any other path, and since no
// pattern here is registered with one, it is answered as the unrouted path it
// is. (An earlier note here said net/http redirects for it. That is true only
// of a table that registers the trailing-slash form, which this one does not.)
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
				writeError(w, http.StatusNotFound, typeInvalidRequest, vendorCodeInvalidPath,
					"the request path contains a dot or empty segment")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withAuth enforces bearer-token auth (UHP "Security" chapter). If no keys
// are configured, auth is skipped — a local tool rather than a conformant
// server, since Security §1 requires a credential on every endpoint but
// `GET /v1/uhp`.
//
// That skip is not a decision this function is allowed to make on its own.
// `uhpd` refuses to start unauthenticated on anything but a loopback address,
// and warns when it starts unauthenticated at all, so by the time a request
// reaches here an empty key list means an operator asked for a local tool and
// was told what they asked for. See config.CheckAuthPosture and issue #55.
//
// Every configured key is equivalent: this server has one principal, so
// "scope file access to the owning principal" (Files §5) is satisfied by
// requiring a key at all, and there is no second principal for anything to be
// outside the scope of. That is a decision rather than an omission — a
// deployment needing several tenants runs one `uhpd` per tenant — and the
// alternative it was chosen over is recorded in ADR-0006, along with why
// `insufficient_scope` is a code this server can never return. See issue #56.
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

	// TimeoutSeconds is the wall-clock budget for this task, and a pointer
	// because absent and zero are different requests: absent asks for whatever
	// the harness and the deployment allow, and zero asks for a task that stops
	// before it starts. Only a positive value is accepted, and how far it gets
	// is service.resolveBudget's answer — the deployment's own bound is not
	// something the transport knows, so a budget longer than the server allows
	// is narrowed there and reported back rather than refused here.
	//
	// The sixth of thirteen schema properties this server reads. Four are
	// still accepted and dropped, which Tasks §1.1 permits — `max_step` (#72)
	// and the three in #48 whose meaning across five heterogeneous CLI
	// harnesses is still undecided. `max_step` is the one that carries the
	// same MUST this field does; see docs/conformance.md. What a dropped one
	// now gets is a mention in `metadata.ignored_fields`; see
	// ignored_fields.go.
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`

	// Instructions is additional system guidance for this task, and is
	// appended to the harness's standing instructions rather than replacing
	// them. The service composes the two; the transport only carries it.
	//
	// Not a pointer, because absent and empty are the same request here: both
	// mean "no guidance of my own", and there is nothing an explicit empty
	// string could ask for that omission does not already say.
	Instructions string `json:"instructions,omitempty"`

	// Store is whether this response is retained for later reads, and a
	// pointer because its default is true — a plain bool could not express
	// `store: false` at all, since the zero value and the omitted field would
	// be the same thing and the server would apply the opposite of what was
	// asked.
	Store *bool `json:"store,omitempty"`

	// Background asks for the POST to be answered as soon as the task is
	// accepted rather than held open until the run is over (Tasks §1.1). The
	// ninth of thirteen, and the one that is a lifecycle choice rather than a
	// parameter of the run — nothing about the work changes, only when this
	// request stops waiting for it. See ADR-0005 and issue #78.
	//
	// Not a pointer, because absent and false are the same request: the schema
	// gives it a default of `false`, and holding the POST open is what a
	// request that says nothing gets.
	Background bool `json:"background,omitempty"`
}

// maxIdempotencyKeyBytes bounds the `Idempotency-Key` header.
//
// The specification leaves the key opaque and says nothing about its length,
// but a key is remembered for a day, so an unbounded one is a string this
// server keeps rather than one it forwards. 255 is comfortably above anything
// a client generates — a UUID is 36 — and refusing above it is honest, where
// silently truncating would collapse two distinct keys into one and answer the
// second request with the first one's task.
const maxIdempotencyKeyBytes = 255

// idempotencyKey reads the header, reporting whether it is usable.
//
// Surrounding whitespace is trimmed and a key that is empty afterwards is
// treated as absent: `Idempotency-Key: ` asks for nothing, and binding every
// such request to one shared empty key would make them all the same task.
func idempotencyKey(r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	return key, len(key) <= maxIdempotencyKeyBytes
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	// Without a bound, a single request can drive the server out of memory,
	// and auth is off by default.
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)

	// Read before the body, so an unusable key is refused before the server
	// does any of the work the key exists to avoid doing twice.
	idempotency, ok := idempotencyKey(r)
	if !ok {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the Idempotency-Key header is longer than this server accepts")
		return
	}

	// A resume point only means something against a stream this server has
	// already started, and the only way a POST reaches one is by repeating an
	// idempotency key whose first request is still remembered. Anything else
	// starts a fresh task whose stream begins at 0, and skipping into that
	// would silently swallow its opening events — the `response.created` a
	// client needs, and every delta before the resume point.
	//
	// Checking the key is known, rather than merely present, is what makes the
	// refusal true of the case that actually happens: keys live in memory and
	// expire, so a retry can arrive with a perfectly well-formed key that no
	// longer names anything.
	at, usable := resumeFrom(r)
	if !usable {
		writeInvalidLastEventID(w)
		return
	}
	if at.present && !s.tasks.ResumableStream(idempotency) {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"Last-Event-ID resumes a stream this server already started; send it with "+
				"the Idempotency-Key of the request that started that stream")
		return
	}

	// Read whole and decoded twice, rather than streamed straight into the
	// struct. The typed decode cannot see a field the struct has no name for,
	// and `metadata.ignored_fields` is a report of exactly those — so the raw
	// key set is the only thing that can answer which of them this request
	// sent. decodeHarnessBody does the same for the same reason, one level of
	// detail further: there it tells `{"max_step": null}` from `{}`, and here
	// it tells "sent and dropped" from "never mentioned".
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if writeIfTooLarge(w, err, s.maxBodyBytes) {
			return
		}
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the request body could not be read")
		return
	}
	var body createTaskBody
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", "the request body could not be parsed as JSON")
		return
	}
	present := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &present); err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the request body must be a JSON object")
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
	// Refused rather than ignored. Quietly substituting the server's own budget
	// would be the defect #54 is about in a quieter form — a field the client
	// set and the server discarded without saying so — and "a budget of no
	// seconds" is not something a caller could have meant.
	if body.TimeoutSeconds != nil && *body.TimeoutSeconds <= 0 {
		writeErrorParam(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"timeout_seconds must be a positive number of seconds", "timeout_seconds")
		return
	}
	// The one combination of the two that has no honest answer, refused rather
	// than half-done. `background` says "do not deliver the result here, I will
	// come back for it"; `store: false` says "there will be nothing here to come
	// back to", because the record is dropped the moment the run is terminal.
	// Together, on a request that is not streaming, they ask this server to send
	// the answer nowhere at all — the POST carries an `in_progress` object and
	// every later read is a 404.
	//
	// Refused for the same reason `timeout_seconds: 0` is: it is not something a
	// caller could have meant, and quietly picking a winner would drop a field
	// the client set. A stream is not refused, because the terminal event
	// delivers the whole response before the record goes.
	//
	// This one is a pre-flight check and its whole job is to refuse before a CLI
	// is forked, so it can only see the body that arrived. A request carrying a
	// key this server already knows is left to writeAccepted, which decides the
	// same question against the accepted task: Tasks §6 owes that request the
	// first one's answer, and for a run that has already finished the answer
	// exists and can be delivered. Refusing it here would make the retry that
	// faithfully repeats its original body the one that is turned away, while
	// the retry that dropped a field got served.
	//
	// A key that expires between this check and the claim behind it starts a
	// fresh task and is then refused by writeAccepted instead — a run nobody
	// collects, and the same 400, for a window narrower than the request itself.
	if body.Background && !body.Stream && body.Store != nil && !*body.Store &&
		!s.tasks.ResumableStream(idempotency) {
		writeErrorParam(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"background: true with store: false has nowhere to deliver the result — the response "+
				"is dropped when the run ends and this request will not be here to receive it; "+
				"send stream: true to be given it on the stream, or drop store: false to read it "+
				"back with GET /v1/responses/{id}", "background")
		return
	}
	// Absent is not invalid. Tasks §1.2: "If `harness_id` is absent, the server
	// MUST use a default harness and MUST report which one it used in the
	// response `metadata`." This used to refuse with `invalid_input`, which
	// made `{"input":"hi"}` — the smallest body the schema permits — a 400
	// (issue #53). The choosing is service.DefaultHarness's, because the
	// transport cannot see which harnesses are ready.
	harnessID, _ := body.Metadata["harness_id"].(string)

	task, run, err := s.tasks.StartTask(r.Context(), service.CreateTaskRequest{
		Input:              input.Text,
		Model:              body.Model,
		HarnessID:          harnessID,
		PreviousResponseID: body.PreviousResponseID,
		Metadata:           body.Metadata,
		Attachments:        input.Attachments,
		InputItems:         input.Items,
		IdempotencyKey:     idempotency,
		TimeoutSeconds:     body.TimeoutSeconds,
		Instructions:       body.Instructions,
		Store:              body.Store,
		IgnoredFields:      ignoredFields(present),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	if !body.Stream {
		if body.Background {
			s.writeAccepted(w, r, task, run)
			return
		}
		final, err := s.waitForResult(r.Context(), task, run)
		if err != nil {
			// The client went away. The run is unaffected and still on its way
			// to a terminal state; there is simply nobody left to tell.
			return
		}
		writeJSON(w, http.StatusOK, final)
		return
	}

	// Checked only now, because until StartTask returns there is no stream to
	// check a resume point against. A run retains its log whole, so the lower
	// bound is always zero and only the upper one can bite: a `Last-Event-ID`
	// past what the original produced would otherwise open a stream that stays
	// empty.
	from := service.FromOldest
	if at.present {
		if !resumable(w, at.from, run.Oldest(), run.Head()) {
			return
		}
		from = at.from
	}
	s.streamSSE(w, r, from, run.Events)
}
