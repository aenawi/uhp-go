package http

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
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
	Name           string          `json:"name"`
	Base           string          `json:"base"`
	DefaultModel   string          `json:"default_model"`
	SystemPrompt   string          `json:"system_prompt"`
	McpServers     []uhp.McpServer `json:"mcp_servers"`
	Skills         []uhp.Skill     `json:"skills"`
	DisabledTools  []string        `json:"disabled_tools"`
	MaxStep        *int            `json:"max_step"`
	TimeoutSeconds *int            `json:"timeout_seconds"`
}

// patch reads the same body as a partial update. `present` carries which keys
// the client actually sent, which is the only way to tell `{"max_step": null}`
// — clear it — from a body that never mentioned it.
func (b harnessBody) patch(present map[string]json.RawMessage) service.HarnessPatch {
	set := func(key string) bool { _, ok := present[key]; return ok }
	return service.HarnessPatch{
		Name:           service.Optional[string]{Set: set("name"), Value: b.Name},
		Base:           service.Optional[string]{Set: set("base"), Value: b.Base},
		DefaultModel:   service.Optional[string]{Set: set("default_model"), Value: b.DefaultModel},
		SystemPrompt:   service.Optional[string]{Set: set("system_prompt"), Value: b.SystemPrompt},
		McpServers:     service.Optional[[]uhp.McpServer]{Set: set("mcp_servers"), Value: b.McpServers},
		Skills:         service.Optional[[]uhp.Skill]{Set: set("skills"), Value: b.Skills},
		DisabledTools:  service.Optional[[]string]{Set: set("disabled_tools"), Value: b.DisabledTools},
		MaxStep:        service.Optional[*int]{Set: set("max_step"), Value: b.MaxStep},
		TimeoutSeconds: service.Optional[*int]{Set: set("timeout_seconds"), Value: b.TimeoutSeconds},
	}
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
	body, _, ok := s.decodeHarnessBody(w, r)
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
	body, _, ok := s.decodeHarnessBody(w, r)
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

// handlePatchHarness answers PATCH /v1/harnesses/{id}.
//
// An extension, not a protocol requirement: §5.2 defines only the PUT. It is
// offered because replacement alone makes the safe edit the expensive one — a
// rename means resending every skill file the harness owns, and a client that
// gets that wrong empties a folder nobody notices is gone. `base` is refused
// here exactly as it is on PUT.
func (s *Server) handlePatchHarness(w http.ResponseWriter, r *http.Request) {
	body, present, ok := s.decodeHarnessBody(w, r)
	if !ok {
		return
	}
	h, err := s.tasks.PatchHarness(r.Context(), r.PathValue("id"), body.patch(present))
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

// decodeHarnessBody reads the body twice: once into the typed struct, and once
// into a map of raw keys so a partial update can tell an absent field from one
// explicitly set to null. The typed decode alone cannot answer that.
func (s *Server) decodeHarnessBody(
	w http.ResponseWriter, r *http.Request,
) (harnessBody, map[string]json.RawMessage, bool) {
	// A skill bundle carries file contents, so the body is bounded for the
	// same reason an upload is.
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if writeIfTooLarge(w, err, s.maxBodyBytes) {
			return harnessBody{}, nil, false
		}
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the request body could not be read")
		return harnessBody{}, nil, false
	}
	var body harnessBody
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the request body could not be parsed as JSON")
		return harnessBody{}, nil, false
	}
	present := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &present); err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the request body must be a JSON object")
		return harnessBody{}, nil, false
	}
	return body, present, true
}

// handleHarnessSkillFiles answers GET /v1/harnesses/{id}/skills/{skill_id}/files
// with the complete bundle (Harnesses §4.2).
//
// A skill that exists but is empty is a 200 with an empty list, not a 404: the
// client asked whether the folder round-tripped, and "it is there and it is
// empty" is the answer that lets it find out.
func (s *Server) handleHarnessSkillFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	files, ok, err := s.tasks.HarnessSkillFiles(r.Context(), id, r.PathValue("skill_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !ok {
		// One answer for "no such harness" and "no such skill in it": the
		// client's next move is the same either way, and separating them lets
		// a caller enumerate harness ids it was not given.
		writeError(w, http.StatusNotFound, typeInvalidRequest, vendorCodeSkillNotFound,
			"no such skill on harness "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
