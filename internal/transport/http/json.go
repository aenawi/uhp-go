package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorEnvelope is the body UHP requires on every non-2xx response
// (Errors §1). Every field is required, and `param` and `detail` are present
// as explicit nulls rather than omitted, so a client can tell "no value" from
// "this server is older than the field".
type errorEnvelope struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Type    string         `json:"type"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Param   *string        `json:"param"`
	Detail  map[string]any `json:"detail"`
}

func writeError(w http.ResponseWriter, status int, errType, code, message string) {
	writeErrorFull(w, status, errType, code, message, "", nil)
}

// writeErrorFull is the one that actually writes; the three below name the
// common shapes so a call site does not have to pass two zero values to say it
// has neither a param nor a detail.
func writeErrorFull(w http.ResponseWriter, status int, errType, code, message, param string, detail map[string]any) {
	var p *string
	if param != "" {
		p = &param
	}
	writeJSON(w, status, errorEnvelope{Error: errorPayload{
		Type: errType, Code: code, Message: message, Param: p, Detail: detail,
	}})
}

// writeErrorParam is writeError plus the dotted path to the offending field,
// which Errors §1 requires whenever there is one: a client that is told only
// "invalid_input" has to guess which field it got wrong.
func writeErrorParam(w http.ResponseWriter, status int, errType, code, message, param string) {
	writeErrorFull(w, status, errType, code, message, param, nil)
}

// writeIfTooLarge answers a body that blew the configured limit and reports
// whether it did. Files §1.2: an oversized upload MUST be a 413 with
// `file_too_large` and the limit in `detail`, never a truncated file — a
// silently truncated input produces a confident, wrong answer.
func writeIfTooLarge(w http.ResponseWriter, err error, maxBytes int64) bool {
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		return false
	}
	writeErrorDetail(w, http.StatusRequestEntityTooLarge, typeInvalidRequest, "file_too_large",
		"the request exceeds this server's size limit",
		map[string]any{"max_bytes": maxBytes})
	return true
}

// writeErrorDetail is writeError with structured extra context, used where the
// specification requires it — for example unsupported_protocol_version, whose
// detail MUST list the versions the server does support.
func writeErrorDetail(w http.ResponseWriter, status int, errType, code, message string, detail map[string]any) {
	writeErrorFull(w, status, errType, code, message, "", detail)
}

// writeErrorRetryAfter is writeErrorDetail plus RFC 9110 §10.2.3's header,
// which is the only part of a refusal a client can act on without parsing the
// body. A retryable answer that says nothing about when to come back invites a
// hot loop against the condition it is complaining about.
//
// The header must be set before writeErrorFull, which writes the status line.
func writeErrorRetryAfter(w http.ResponseWriter, status int, errType, code, message string,
	detail map[string]any, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
	writeErrorFull(w, status, errType, code, message, "", detail)
}
