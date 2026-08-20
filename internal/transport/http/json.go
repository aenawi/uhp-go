package http

import (
	"encoding/json"
	"net/http"
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
	writeJSON(w, status, errorEnvelope{Error: errorPayload{
		Type: errType, Code: code, Message: message,
	}})
}

// writeErrorDetail is writeError with structured extra context, used where the
// specification requires it — for example unsupported_protocol_version, whose
// detail MUST list the versions the server does support.
func writeErrorDetail(w http.ResponseWriter, status int, errType, code, message string, detail map[string]any) {
	writeJSON(w, status, errorEnvelope{Error: errorPayload{
		Type: errType, Code: code, Message: message, Detail: detail,
	}})
}
