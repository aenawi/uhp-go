package http

import (
	"net/http"

	"github.com/aenawi/uhp-go/internal/domain"
)

// UHPVersion is the protocol version this server implements.
const UHPVersion = "2026-08-11"

// supportedVersions is every version this server can serve.
var supportedVersions = []string{UHPVersion}

// ConformanceClass is what this server claims.
//
// It is "core" and not "full" deliberately. The class MUST agree with the
// capability list, and claiming a class the server does not implement is a
// claim the conformance suite exists to falsify. This moves up only when the
// corresponding capabilities are genuinely true.
const ConformanceClass = "core"

type discoveryDoc struct {
	Object           string          `json:"object"`
	Protocol         string          `json:"protocol"`
	Versions         []string        `json:"versions"`
	DefaultVersion   string          `json:"default_version"`
	ConformanceClass string          `json:"conformance_class"`
	Capabilities     map[string]bool `json:"capabilities"`
	Implementation   map[string]any  `json:"implementation"`
}

// capabilities reports what this server actually implements.
//
// Every key is present with an explicit boolean, never omitted: a client must
// be able to tell "not supported" from "this server is older than the field".
// Reporting false for something unimplemented is the honest answer and is what
// keeps conformance_class consistent with reality.
// The two file capabilities are computed rather than asserted, because they
// depend on configuration: file input and artifact capture both need a
// per-session working directory, and without UHP_WORKSPACE there is nowhere to
// put a client's file and nothing to diff for artifacts.
func capabilities(files, harnessManagement bool) map[string]bool {
	return map[string]bool{
		"streaming":          true,
		"sessions":           true,
		"cancellation":       true,
		"files_input":        files,
		"files_output":       files,
		"session_listing":    true,
		"harness_management": harnessManagement,
		"session_sharing":    false,
		"idempotency":        false,
	}
}

func (s *Server) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, discoveryDoc{
		Object:           "uhp.discovery",
		Protocol:         "uhp",
		Versions:         supportedVersions,
		DefaultVersion:   UHPVersion,
		ConformanceClass: ConformanceClass,
		Capabilities:     capabilities(s.tasks.FilesEnabled(), s.tasks.HarnessManagementEnabled()),
		Implementation:   map[string]any{"name": "uhp-go", "version": Version},
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
		hs = []domain.Harness{}
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

type modelEntry struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Backend   string `json:"backend"`
	Available bool   `json:"available"`
	Default   bool   `json:"default"`
}

// handleListModels answers GET /v1/models with the catalogue, keyed by backend.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	harnesses, err := s.tasks.ListHarnesses(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	backends := map[string]any{}
	for _, h := range harnesses {
		entries := make([]modelEntry, 0, len(h.Models))
		for _, m := range h.Models {
			entries = append(entries, modelEntry{
				ID: m, Label: m, Backend: h.Base,
				// Computed, not asserted: this reflects whether the CLI is
				// actually reachable right now.
				Available: s.tasks.ModelAvailable(r.Context(), h.ID, m),
				Default:   m == h.DefaultModel,
			})
		}
		backends[h.Base] = map[string]any{"default": h.DefaultModel, "models": entries}
	}
	writeJSON(w, http.StatusOK, map[string]any{"backends": backends})
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
	entries := make([]modelEntry, 0, len(h.Models))
	for _, m := range h.Models {
		entries = append(entries, modelEntry{
			ID: m, Label: m, Backend: h.Base,
			Available: s.tasks.ModelAvailable(r.Context(), h.ID, m),
			Default:   m == h.DefaultModel,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"harness_id": h.ID,
		"backend":    h.Base,
		"default":    h.DefaultModel,
		"fallback":   h.DefaultModel,
		"models":     entries,
	})
}
