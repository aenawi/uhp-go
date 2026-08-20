package http

import (
	"errors"
	"net/http"

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
	vendorCodeHarnessManagementUnsupported = "uhpgo_harness_management_unsupported"
	vendorCodeHarnessNotManaged            = "uhpgo_harness_not_managed"
	vendorCodeImmutableField               = "uhpgo_immutable_field"
	vendorCodeInvalidSkill                 = "uhpgo_invalid_skill"
	vendorCodeInvalidMcpServer             = "uhpgo_invalid_mcp_server"
	vendorCodeMcpUndeliverable             = "uhpgo_mcp_undeliverable"
	vendorCodeSkillNotFound                = "uhpgo_skill_not_found"
	vendorCodeStorageFailure               = "uhpgo_storage_failure"
)
