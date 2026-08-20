package http

import (
	"encoding/json"
	"net/http"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
)

// harnessBody is the create and replace body (Harnesses §5.1 and §5.2).
//
// The top-level field names are snake_case going in and camelCase coming out.
// That asymmetry is the specification's, not a mistake here: `HarnessCreate`
// names `default_model` while the harness object returns `defaultModel`, and a
// server that quietly accepted either would be inventing a dialect no other
// implementation speaks.
//
// The asymmetry stops at the top level, which is why the nested objects are the
// domain types rather than transport copies of them: `McpServer` and `Skill`
// are spelled identically in both directions — `content_b64`, `enabled`, `auth`
// — so a second set of structs would be five more places to forget a field in
// exchange for nothing.
type harnessBody struct {
	Name           string             `json:"name"`
	Base           string             `json:"base"`
	DefaultModel   string             `json:"default_model"`
	SystemPrompt   string             `json:"system_prompt"`
	McpServers     []domain.McpServer `json:"mcp_servers"`
	Skills         []domain.Skill     `json:"skills"`
	DisabledTools  []string           `json:"disabled_tools"`
	MaxStep        *int               `json:"max_step"`
	TimeoutSeconds *int               `json:"timeout_seconds"`
}

func (b harnessBody) spec() service.HarnessSpec {
	return service.HarnessSpec{
		Name:           b.Name,
		Base:           b.Base,
		DefaultModel:   b.DefaultModel,
		SystemPrompt:   b.SystemPrompt,
		McpServers:     b.McpServers,
		Skills:         b.Skills,
		DisabledTools:  b.DisabledTools,
		MaxStep:        b.MaxStep,
		TimeoutSeconds: b.TimeoutSeconds,
	}
}

// handleCreateHarness answers POST /v1/harnesses.
//
// The created harness comes back with 200 rather than 201. A harness is not
// addressed by a Location header anywhere in this protocol — the id in the
// body is how a client refers to it — and the conformance suite reads 200 as
// the success of a create, so 201 would be a private convention with no
// upside.
func (s *Server) handleCreateHarness(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeHarnessBody(w, r)
	if !ok {
		return
	}
	if body.Base == "" {
		// Answered here rather than left to the service so the refusal can name
		// the wire field. Errors §1 requires the dotted path whenever there is
		// one, and only this layer knows the request spells it `base` while the
		// harness object spells its default model `defaultModel`.
		writeErrorParam(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"`base` is required and must name a harness runtime this server supports", "base")
		return
	}
	h, err := s.tasks.CreateHarness(r.Context(), body.spec())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// handleReplaceHarness answers PUT /v1/harnesses/{id}.
//
// Replace, not merge: Harnesses §5.2 defines the update as a replacement of
// the mutable configuration, so a field the client left out is one it no
// longer wants. A client that means to change one field reads the harness
// first and sends it back whole — which is why an unrelated rename must not
// destroy a skill folder, and why the MCP credential it was never given is
// carried forward for it.
func (s *Server) handleReplaceHarness(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeHarnessBody(w, r)
	if !ok {
		return
	}
	h, err := s.tasks.UpdateHarness(r.Context(), r.PathValue("id"), body.spec())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// handleDeleteHarness answers DELETE /v1/harnesses/{id}.
//
// The sessions and responses that ran on it are left alone (Harnesses §5.3):
// history that disappears when configuration changes cannot be audited.
func (s *Server) handleDeleteHarness(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.DeleteHarness(r.Context(), r.PathValue("id")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) decodeHarnessBody(w http.ResponseWriter, r *http.Request) (harnessBody, bool) {
	// A skill bundle carries file contents, so the body is bounded for the
	// same reason an upload is.
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	var body harnessBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if writeIfTooLarge(w, err, s.maxBodyBytes) {
			return harnessBody{}, false
		}
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the request body could not be parsed as JSON")
		return harnessBody{}, false
	}
	return body, true
}
