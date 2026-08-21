package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
)

// UHP error types (Errors §2). The broad class; `code` carries the specific
// reason.
const (
	typeInvalidRequest = "invalid_request_error"
	typeAuthentication = "authentication_error"
	typeServerError    = "server_error"
)

// retryAfterNoCapacity is the floor this server asks a refused client to wait,
// not a prediction of when a slot frees.
//
// It cannot be a prediction: a run holds its slot for as long as the agent
// works, which is minutes, and the server has no idea which of the runs in
// flight ends first. What it can honestly assert is a minimum — come back, but
// not immediately — and that is the whole job here, because the alternative to
// an imperfect number is a client that reads no header and retries in a loop.
const retryAfterNoCapacity = 5 * time.Second

// writeServiceError maps a service-layer error onto the UHP status code and
// error code the specification requires.
//
// Everything used to become 502 harness_dispatch_failed, which told a client
// "the server broke, retry" for conditions that were actually the client's own
// request being wrong and would fail identically forever.
func writeServiceError(w http.ResponseWriter, err error) {
	// Handled before the switch because it is the only refusal that has to
	// carry structured context: F-01 of the conformance suite discovers which
	// bases a server supports by reading `detail.supported` off this very
	// response, and a client has the same problem it does.
	var unsupportedBase *service.UnsupportedBaseError
	if errors.As(err, &unsupportedBase) {
		writeErrorFull(w, http.StatusUnprocessableEntity, typeInvalidRequest, "unsupported_base",
			"this server cannot run harness base "+unsupportedBase.Base,
			"base", map[string]any{"supported": unsupportedBase.Supported})
		return
	}

	// 422 rather than 501: the server is configured perfectly well, and it is
	// the harness this particular request named that cannot do what was asked.
	// The detail names the capability so a client can match the refusal against
	// the `capabilities` list it was already given for that harness, instead of
	// guessing which of its assumptions was wrong.
	var unsupportedCapability *service.CapabilityError
	if errors.As(err, &unsupportedCapability) {
		writeErrorFull(w, http.StatusUnprocessableEntity, typeInvalidRequest,
			vendorCodeCapabilityUnsupported, unsupportedCapability.Error(),
			paramForCapability(unsupportedCapability.Capability),
			map[string]any{
				"harness":    unsupportedCapability.HarnessID,
				"capability": string(unsupportedCapability.Capability),
			})
		return
	}

	// 503, not a 4xx. Errors §4 makes the class the retry signal, and nothing
	// about this request is wrong — it arrived while the server was already
	// running as many harness processes as it is configured for, and retrying
	// is exactly what will work. A 4xx would tell the client the opposite.
	//
	// The status is what separates this from the 502 in the default arm below,
	// which carries the same `harness_unavailable` code for a harness that is
	// unavailable permanently rather than momentarily. The code is the one the
	// specification gives for both; only 503 plus Retry-After says "later".
	var noCapacity *service.NoCapacityError
	if errors.As(err, &noCapacity) {
		writeErrorRetryAfter(w, http.StatusServiceUnavailable, typeServerError, "harness_unavailable",
			"No capacity to run this harness right now",
			map[string]any{"max_concurrent_runs": noCapacity.Limit},
			retryAfterNoCapacity)
		return
	}

	switch {
	case errors.Is(err, service.ErrHarnessNotFound):
		writeError(w, http.StatusNotFound, typeInvalidRequest, "harness_not_found", err.Error())
	case errors.Is(err, service.ErrResponseNotFound):
		writeError(w, http.StatusNotFound, typeInvalidRequest, "response_not_found", err.Error())
	case errors.Is(err, service.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, typeInvalidRequest, "session_not_found", err.Error())
	case errors.Is(err, service.ErrHarnessMismatch):
		writeError(w, http.StatusConflict, typeInvalidRequest, "harness_mismatch", err.Error())
	case errors.Is(err, service.ErrArtifactNotFound):
		writeFileNotFound(w)
	case errors.Is(err, service.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", err.Error())
	case errors.Is(err, service.ErrFilesUnsupported):
		writeFilesUnsupported(w)
	case errors.Is(err, service.ErrSessionBusy):
		writeError(w, http.StatusConflict, typeInvalidRequest, "session_busy", err.Error())
	case errors.Is(err, service.ErrHarnessManagementUnsupported):
		writeHarnessManagementUnsupported(w)
	case errors.Is(err, service.ErrHarnessNotManaged):
		// 409 rather than 403: nothing about the credential is wrong, the
		// harness simply is not the API's to change.
		writeError(w, http.StatusConflict, typeInvalidRequest, vendorCodeHarnessNotManaged, err.Error())
	case errors.Is(err, service.ErrBaseImmutable):
		writeErrorParam(w, http.StatusUnprocessableEntity, typeInvalidRequest,
			vendorCodeImmutableField, err.Error(), "base")
	case errors.Is(err, service.ErrInvalidSkill):
		writeErrorParam(w, http.StatusUnprocessableEntity, typeInvalidRequest,
			vendorCodeInvalidSkill, err.Error(), "skills")
	case errors.Is(err, service.ErrInvalidMcpServer):
		writeErrorParam(w, http.StatusUnprocessableEntity, typeInvalidRequest,
			vendorCodeInvalidMcpServer, err.Error(), "mcp_servers")
	case errors.Is(err, service.ErrMcpUndeliverable):
		writeErrorParam(w, http.StatusUnprocessableEntity, typeInvalidRequest,
			vendorCodeMcpUndeliverable, err.Error(), "mcp_servers")
	case errors.Is(err, service.ErrStorage):
		// The server failed, not the request. Errors §4: this is the class a
		// client may retry, and calling it a 4xx would tell it not to.
		writeError(w, http.StatusInternalServerError, typeServerError, vendorCodeStorageFailure,
			"this server could not read or write its own state")
	case errors.Is(err, harness.ErrUnsupportedModel):
		// Tasks §1.3: a server MUST NOT pretend it ran what was asked. This
		// server fails rather than substituting.
		writeError(w, http.StatusUnprocessableEntity, typeInvalidRequest, "model_unavailable", err.Error())
	default:
		writeError(w, http.StatusBadGateway, typeServerError, "harness_unavailable", err.Error())
	}
}

// paramForCapability names the request field a client can drop to stop being
// refused, or nothing when the request has no such field.
//
// Errors §1 asks for the dotted path "whenever there is one", and the second
// half of that is load-bearing here. A continuation is refused over
// `previous_response_id` and can be sent again without it; a cancel carries no
// body at all, and naming a field that is not in the request would send a
// client looking for something it cannot find, which is worse than naming
// nothing. The wire names live here rather than in the service layer, which
// knows Go fields and not JSON paths.
func paramForCapability(c domain.Capability) string {
	if c == domain.CapSessions {
		return "previous_response_id"
	}
	return ""
}

func writeHarnessManagementUnsupported(w http.ResponseWriter) {
	writeError(w, http.StatusNotImplemented, typeServerError, vendorCodeHarnessManagementUnsupported,
		"this server is not configured with a harness store, so harnesses cannot be created or changed")
}

// Vendor-prefixed because Errors §3 has no entry for any of these conditions,
// and requires an additional code to be namespaced so a future version of the
// specification cannot collide with it.
//
// `invalid_input` is the near miss, and it does not fit: §3.1 defines it as
// "the body could not be parsed, or a field has the wrong type". A skill bundle
// with no SKILL.md parses fine and has every field's type right — it is
// semantically wrong — and answering `invalid_input` would send a client
// looking for a type error it will never find.
const (
	vendorCodeCapabilityUnsupported        = "uhpgo_capability_unsupported"
	vendorCodeHarnessManagementUnsupported = "uhpgo_harness_management_unsupported"
	vendorCodeHarnessNotManaged            = "uhpgo_harness_not_managed"
	vendorCodeImmutableField               = "uhpgo_immutable_field"
	vendorCodeInvalidSkill                 = "uhpgo_invalid_skill"
	vendorCodeInvalidMcpServer             = "uhpgo_invalid_mcp_server"
	vendorCodeMcpUndeliverable             = "uhpgo_mcp_undeliverable"
	vendorCodeSkillNotFound                = "uhpgo_skill_not_found"
	vendorCodeStorageFailure               = "uhpgo_storage_failure"
)
