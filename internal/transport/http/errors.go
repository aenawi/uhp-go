package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// UHP error types (Errors §2). The broad class; `code` carries the specific
// reason.
//
// Aliases rather than a second set of literals: the service layer sets the same
// `type` on a task's error and cannot see an unexported constant here, and two
// independent spellings of one vocabulary is what let this object's two
// renderings disagree.
const (
	typeInvalidRequest = uhp.ErrorTypeInvalidRequest
	typeAuthentication = uhp.ErrorTypeAuthentication
	typeServerError    = uhp.ErrorTypeServerError
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

// retryAfterSessionBusy is the floor for the other refusal that means "later":
// a second task in a session that already has one (Lifecycle §5, issue #60).
//
// Errors §4 makes this one retryable "once in-flight task reaches terminal
// state", so the client is being told to wait, and `retry_after_ms` is the only
// part of that it can act on. Lifecycle §6 also says to omit the field rather
// than guess — and this is not a guess about when the agent finishes, which
// nothing here can know. It is the same honest minimum the capacity refusal
// sends: come back, but not immediately.
//
// #60 expected better than a floor here, and why there is none is worth
// writing down. The reasoning was that #54 bounds every run by a wall
// clock, so the server now knows when the session is free whatever the agent
// does — the holder's remaining budget — and could send a real number instead.
// It knows that, and the number is still wrong to send. It is an upper bound on
// a wait that is usually over in seconds: quoting the half hour a run has left
// would have a client sleep through the answer. Quoting it only when it falls
// below this floor is no better, because that is precisely the moment the
// budget is about to fire and the teardown behind it takes a further moment
// nothing here can size — so the number would be knowably too short in the one
// case it applied. A bound that is too long to sleep for and too short to
// retry on is not a wait, and the floor is what is left.
const retryAfterSessionBusy = 5 * time.Second

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

	// A task that named no harness on a server that cannot choose one. 400
	// rather than 422: the request is well-formed and permitted — Tasks §1.2
	// entitles a client to omit the field — so what is wrong is this
	// deployment's inability to answer it, and `invalid_input` is the closest
	// the vocabulary comes. The param is what makes it actionable, and
	// `detail.harnesses` is the list the client should have chosen from.
	var noDefault *service.NoDefaultHarnessError
	if errors.As(err, &noDefault) {
		writeErrorFull(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			noDefault.Error(), "metadata.harness_id",
			map[string]any{"harnesses": noDefault.Candidates})
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

	// 409 with the wait in the body rather than in a header. RFC 9110 §10.2.3
	// defines Retry-After for 503, 429 and the 3xx redirects, and this is none
	// of those; `retry_after_ms` is the field Lifecycle §6 names for it, and
	// `detail` is where the schema has room for it.
	//
	// `response_id` is the other half, and the more useful one: a client told
	// which response holds the session can watch that one go terminal instead
	// of asking this endpoint again, which is the difference between a wait and
	// a poll. It is not a key the specification defines — `detail` is an open
	// object — so it is a courtesy from this implementation, on the same
	// footing ADR-0004 puts `metadata.ignored_fields`: namespaced by nothing
	// and promised by nobody.
	var busy *service.SessionBusyError
	if errors.As(err, &busy) {
		writeErrorDetail(w, http.StatusConflict, typeInvalidRequest, "session_busy",
			"this session already has a task in flight",
			map[string]any{
				"retry_after_ms": retryAfterSessionBusy.Milliseconds(),
				"response_id":    busy.TaskID,
			})
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
		// The bare sentinel, which nothing produces today: startTask returns
		// the typed error above and it is the only refusal of this kind there
		// is. The arm stays because the alternative for anything that later
		// does is the default arm below, and answering "busy session" with a
		// `502` would tell a client the server is broken.
		writeError(w, http.StatusConflict, typeInvalidRequest, "session_busy", err.Error())
	case errors.Is(err, service.ErrHarnessManagementUnsupported):
		writeHarnessManagementUnsupported(w)
	case errors.Is(err, service.ErrSessionSharingUnsupported):
		// 501, like harness management: the request is well-formed and the
		// endpoint is one the protocol defines — what is missing is this
		// deployment's willingness to serve unauthenticated read paths at all.
		// A 4xx would tell the client its request was wrong.
		writeSessionSharingUnsupported(w)
	case errors.Is(err, service.ErrShareNotFound):
		writeShareNotFound(w)
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
	case errors.Is(err, service.ErrStepBudgetUnsupported):
		// 422 rather than 400: the request is well-formed and `max_step` is a
		// number this server understands — what cannot be done is holding that
		// bound on the harness the client named. The parameter is `max_step`
		// rather than `metadata.harness_id`, because dropping the budget is the
		// change that makes the request work and switching harness is a
		// different task.
		//
		// Vendor-prefixed because the specification has no entry for it, and
		// nothing registered today produces it: all five bases either narrate a
		// countable tool call or bound themselves (#72).
		writeErrorParam(w, http.StatusUnprocessableEntity, typeInvalidRequest,
			vendorCodeStepBudgetUnsupported, err.Error(), "max_step")
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
func paramForCapability(c uhpgo.Capability) string {
	if c == uhpgo.CapSessions {
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
//
// The two routing codes miss for their own reason: `harness_not_found` is
// about a harness this server does not have, where these are about a path no
// route claims and a method no route accepts, on any route at all.
//
// `uhpgo_invalid_path` is a third case again: the dot-segment guard runs ahead
// of routing on every route, so the refusal is about the shape of the path
// itself rather than about anything the path names.
const (
	vendorCodeCapabilityUnsupported        = "uhpgo_capability_unsupported"
	vendorCodeHarnessManagementUnsupported = "uhpgo_harness_management_unsupported"
	vendorCodeHarnessNotManaged            = "uhpgo_harness_not_managed"
	vendorCodeImmutableField               = "uhpgo_immutable_field"
	vendorCodeInvalidPath                  = "uhpgo_invalid_path"
	vendorCodeInvalidSkill                 = "uhpgo_invalid_skill"
	vendorCodeInvalidMcpServer             = "uhpgo_invalid_mcp_server"
	vendorCodeMcpUndeliverable             = "uhpgo_mcp_undeliverable"
	vendorCodeStepBudgetUnsupported        = "uhpgo_step_budget_unsupported"
	vendorCodeMethodNotAllowed             = "uhpgo_method_not_allowed"
	vendorCodeRouteNotFound                = "uhpgo_route_not_found"
	vendorCodeSessionSharingUnsupported    = "uhpgo_session_sharing_unsupported"
	vendorCodeShareNotFound                = "uhpgo_share_not_found"
	vendorCodeSkillNotFound                = "uhpgo_skill_not_found"
	vendorCodeStorageFailure               = "uhpgo_storage_failure"
)
