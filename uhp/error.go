package uhp

// Error types are the broad failure class (Errors §2). The schema constrains
// [Error].Type to exactly these six values and makes the field required.
//
// The type is what a client falls back to. UHP's fourth client rule says an
// unrecognised code MUST be treated as its type — an unknown code with
// ErrorTypeServerError is still retryable — so a response carrying a code the
// client has never seen and no type at all leaves a client that followed the
// specification with nothing to read.
const (
	// ErrorTypeInvalidRequest: 400, 404, 409, 413, 422. The request was wrong;
	// retrying it unchanged will fail the same way.
	ErrorTypeInvalidRequest = "invalid_request_error"
	// ErrorTypeAuthentication: 401. The credential is missing, malformed or
	// unknown.
	ErrorTypeAuthentication = "authentication_error"
	// ErrorTypePermission: 403. Authenticated, but not allowed to do this.
	ErrorTypePermission = "permission_error"
	// ErrorTypeRateLimit: 429. Too many requests, or a quota is exhausted.
	ErrorTypeRateLimit = "rate_limit_error"
	// ErrorTypeHarness: reported inside a failed response at HTTP 200. The
	// harness ran and could not complete the work — which is a task failing,
	// not a request failing.
	ErrorTypeHarness = "harness_error"
	// ErrorTypeServerError: 500, 502, 503, 504. The server failed, and
	// retrying may succeed.
	ErrorTypeServerError = "server_error"
)

// The error codes Errors §3 defines. A code is specific and machine-readable
// where the type is broad, and a client branches on the code when it knows it
// and on the type when it does not.
//
// This list is the specification's, not this server's. A server MAY define
// codes for conditions the specification does not cover and MUST namespace them
// with a vendor prefix so a future version cannot collide with them; this
// repository's own are in
// [github.com/aenawi/uhp-go/uhp/uhpgo], not here, because a vendor code sitting
// in this file would be indistinguishable from a protocol one.
//
// # These are the protocol's codes, not one server's
//
// Every code the specification defines is here, because this package models the
// protocol. Which of them a *given* server can actually emit is a property of
// that server: one with no quotas never sends `quota_exhausted`, and one that
// does not convert documents never sends `preview_failed`. So a client should
// switch on the codes it cares about and fall back to [Error].Type for the rest,
// rather than assuming the whole set is live — writing an arm per constant
// produces code that mostly never runs. The server in this repository is
// narrower than this list in seven places, and docs/conformance.md says which.
const (
	CodeUnsupportedProtocolVersion = "unsupported_protocol_version"
	CodeInvalidInput               = "invalid_input"
	CodeHarnessNotFound            = "harness_not_found"
	CodeResponseNotFound           = "response_not_found"
	CodeSessionNotFound            = "session_not_found"
	CodeFileNotFound               = "file_not_found"
	CodeSessionExpired             = "session_expired"
	CodeHarnessMismatch            = "harness_mismatch"
	CodeSessionBusy                = "session_busy"
	CodeFileTooLarge               = "file_too_large"
	CodeModelUnavailable           = "model_unavailable"
	CodeUnsupportedBase            = "unsupported_base"
	CodeMissingCredential          = "missing_credential"
	CodeInvalidCredential          = "invalid_credential"
	CodeInsufficientScope          = "insufficient_scope"
	CodeRateLimited                = "rate_limited"
	CodeQuotaExhausted             = "quota_exhausted"
	CodeHarnessError               = "harness_error"
	CodeHarnessUnavailable         = "harness_unavailable"
	CodeProviderError              = "provider_error"
	CodeTimeout                    = "timeout"
	CodeCancelled                  = "cancelled"
	CodePreviewUnavailable         = "preview_unavailable"
	CodePreviewFailed              = "preview_failed"
)

// Error is the protocol's one error object. It appears in two places and is the
// same object in both: inside [ErrorEnvelope] on a non-2xx response, and as
// [Response].Error on a task that failed at HTTP 200.
//
// That those are one object and not two is worth stating, because this
// repository shipped them as two divergent structs — one carrying a field the
// schema does not define, the other carrying two the schema does — and a client
// reading the same field off both found it present in one and missing from the
// other for no reason the specification explains.
//
// Param and Detail are present as explicit nulls rather than omitted (Errors
// §1 lists both as required), for the same reason the null fields on [Response]
// are.
type Error struct {
	// Type is one of the six ErrorType constants above and is required.
	Type string `json:"type"`
	// Code is specific and machine-readable, and is required. It comes from
	// the Code constants above where one applies, and is vendor-prefixed
	// otherwise.
	Code string `json:"code"`
	// Message is one sentence, safe to show a user. It MUST NOT contain
	// credentials, internal hostnames, file paths or stack traces.
	Message string `json:"message"`
	// Param is the dotted path to the offending field, when there is one —
	// "metadata.harness_id", not "harness". Null when the request has no such
	// field, which is not the same as the server declining to say: naming a
	// field that is not in the request sends a client looking for something it
	// cannot find.
	Param *string `json:"param"`
	// Detail is structured extra context, or null. Some codes specify their
	// own: unsupported_protocol_version carries the versions the server does
	// support, and unsupported_base the bases it can run.
	Detail map[string]any `json:"detail"`
}

// Error implements the error interface, so a decoded protocol error can be
// returned, wrapped and matched with errors.As like any other Go error.
//
// It renders code and message rather than message alone, because the code is
// the half a caller can branch on and a bare sentence loses it the moment the
// value is wrapped into a string.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// ErrorEnvelope is the body UHP requires on every non-2xx response (Errors §1).
//
// A server MUST NOT return 200 with an error inside. A *task* that fails is a
// [Response] with StatusFailed at HTTP 200, because the request succeeded; a
// *request* that fails is a non-2xx carrying this. They are different events
// and conflating them costs a client the ability to tell "your input was wrong"
// from "the agent tried and could not".
type ErrorEnvelope struct {
	Error Error `json:"error"`

	// Detail is a deprecated human-readable alias of Error.Message, retained
	// for clients written against implementations that predate this envelope.
	// It carries no information that is not in Error, will be removed in a
	// future version, and nothing new should read it.
	Detail string `json:"detail,omitempty"`
}
