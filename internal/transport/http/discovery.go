package http

import (
	"net/http"

	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// UHPVersion is the protocol version this server implements.
//
// Taken from the package whose types describe it, rather than spelled again
// here: a server that served one version and published the shapes of another
// would be wrong in a way no test in this package could see.
const UHPVersion = uhp.Version

// supportedVersions is every version this server can serve.
var supportedVersions = []string{UHPVersion}

// ConformanceClass is what this server claims.
//
// It is "core" and not "full" deliberately. The class MUST agree with the
// capability list, and claiming a class the server does not implement is a
// claim the conformance suite exists to falsify. This moves up only when the
// corresponding capabilities are genuinely true.
const ConformanceClass = "core"

// capabilities reports what this server actually implements.
//
// It returns [uhp.Capabilities] rather than the map[string]bool this used to
// build, and the difference is not cosmetic. Every capability must be present
// with an explicit boolean, never omitted, so that a client can tell "not
// supported" from "this server is older than the field" — and a map is a shape
// in which forgetting a key is possible. A struct reports the full set or does
// not compile.
//
// The two file capabilities are computed rather than asserted, because they
// depend on configuration: file input and artifact capture both need a
// per-session working directory, and without UHP_WORKSPACE there is nowhere to
// put a client's file and nothing to diff for artifacts.
func capabilities(files, harnessManagement bool) uhp.Capabilities {
	return uhp.Capabilities{
		Streaming:         true,
		Sessions:          true,
		Cancellation:      true,
		FilesInput:        files,
		FilesOutput:       files,
		SessionListing:    true,
		HarnessManagement: harnessManagement,
		SessionSharing:    false,
		// Unconditional, unlike the two file capabilities: an idempotency key
		// needs no configuration, only somewhere to remember it, and this
		// server always has that. It is remembered in memory, so a restart
		// forgets every key — and every task, in the default store, so a key
		// that survived would point at a response that did not.
		Idempotency: true,
	}
}

func (s *Server) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, uhp.Discovery{
		Object:           "uhp.discovery",
		Protocol:         "uhp",
		Versions:         supportedVersions,
		DefaultVersion:   UHPVersion,
		ConformanceClass: ConformanceClass,
		Capabilities:     capabilities(s.tasks.FilesEnabled(), s.tasks.HarnessManagementEnabled()),
		Implementation:   &uhp.Implementation{Name: "uhp-go", Version: Version},
	})
}

// Version is the implementation version, set at build time.
var Version = "dev"

// withVersion negotiates the protocol version and stamps it on every response,
// including errors.
//
// Lifecycle §1: a server MUST NOT silently serve a version other than the one
// asked for — a client that asked for a version it can parse should not
// receive one it cannot.
func withVersion(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requested := r.Header.Get("UHP-Version")
		if requested != "" && !supports(requested) {
			w.Header().Set("UHP-Version", UHPVersion)
			writeErrorDetail(w, http.StatusBadRequest, typeInvalidRequest,
				"unsupported_protocol_version",
				"this server cannot serve UHP version "+requested,
				map[string]any{"supported": supportedVersions})
			return
		}
		served := UHPVersion
		if requested != "" {
			served = requested
		}
		w.Header().Set("UHP-Version", served)
		next(w, r)
	}
}

func supports(v string) bool {
	for _, s := range supportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// handleListHarnesses answers GET /v1/harnesses.
func (s *Server) handleListHarnesses(w http.ResponseWriter, r *http.Request) {
	hs, err := s.tasks.ListHarnesses(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if hs == nil {
		hs = []uhpgo.Harness{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"harnesses": hs})
}

// handleGetHarness answers GET /v1/harnesses/{id}.
func (s *Server) handleGetHarness(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h, ok, err := s.tasks.GetHarness(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !ok {
		writeHarnessNotFound(w, id)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func writeHarnessNotFound(w http.ResponseWriter, id string) {
	writeError(w, http.StatusNotFound, typeInvalidRequest, "harness_not_found",
		"no harness with id "+id)
}

// modelsFor builds one harness's model entries.
//
// Available is computed rather than asserted, and the schema is unusually
// pointed about why: listing a model as available and then failing the task is
// the worst outcome for a client, because a user has already chosen it.
func (s *Server) modelsFor(r *http.Request, h uhpgo.Harness) []uhp.Model {
	entries := make([]uhp.Model, 0, len(h.Models))
	for _, m := range h.Models {
		entries = append(entries, uhp.Model{
			ID: m, Label: m, Backend: h.Base,
			Available: s.tasks.ModelAvailable(r.Context(), h.ID, m),
			Default:   m == h.DefaultModel,
		})
	}
	return entries
}

// handleListModels answers GET /v1/models with the catalogue, keyed by backend.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	harnesses, err := s.tasks.ListHarnesses(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	backends := map[string]uhp.ModelCatalogBackend{}
	for _, h := range harnesses {
		backends[h.Base] = uhp.ModelCatalogBackend{
			Default: h.DefaultModel,
			Models:  s.modelsFor(r, h),
		}
	}
	writeJSON(w, http.StatusOK, uhp.ModelCatalog{Backends: backends})
}

// handleHarnessModels answers GET /v1/harnesses/{id}/models.
func (s *Server) handleHarnessModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h, ok, err := s.tasks.GetHarness(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !ok {
		writeHarnessNotFound(w, id)
		return
	}
	writeJSON(w, http.StatusOK, uhp.HarnessModels{
		HarnessID: h.ID,
		Backend:   h.Base,
		Default:   h.DefaultModel,
		// This server does not substitute: an unavailable model is refused
		// with model_unavailable rather than quietly replaced, so the only
		// model it would ever fall back to is the default it already named.
		Fallback: h.DefaultModel,
		Models:   s.modelsFor(r, h),
	})
}
