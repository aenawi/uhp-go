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
	switch {
	case errors.Is(err, service.ErrHarnessNotFound):
		writeError(w, http.StatusNotFound, typeInvalidRequest, "harness_not_found", err.Error())
	case errors.Is(err, service.ErrResponseNotFound):
		writeError(w, http.StatusNotFound, typeInvalidRequest, "response_not_found", err.Error())
	case errors.Is(err, service.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, typeInvalidRequest, "session_not_found", err.Error())
	case errors.Is(err, service.ErrHarnessMismatch):
		writeError(w, http.StatusConflict, typeInvalidRequest, "harness_mismatch", err.Error())
	case errors.Is(err, service.ErrSessionBusy):
		writeError(w, http.StatusConflict, typeInvalidRequest, "session_busy", err.Error())
	case errors.Is(err, harness.ErrUnsupportedModel):
		// Tasks §1.3: a server MUST NOT pretend it ran what was asked. This
		// server fails rather than substituting.
		writeError(w, http.StatusUnprocessableEntity, typeInvalidRequest, "model_unavailable", err.Error())
	default:
		writeError(w, http.StatusBadGateway, typeServerError, "harness_unavailable", err.Error())
	}
}
