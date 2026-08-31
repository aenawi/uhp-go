package http

import (
	"io"
	"net/http"
	"strconv"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
)

// The two halves of Sessions §5 live in this file and are deliberately not
// symmetrical.
//
// The first three handlers belong to the principal that owns the session: they
// mint, read and withdraw a capability, and they are reached the way every
// other endpoint here is, with a bearer token. The four below them are reached
// by whoever holds the link and nothing else, so they answer with the session's
// content and never with anything about this server's configuration, its other
// sessions, or why an id did not resolve.

// handleShareSession answers POST /v1/sessions/{id}/share.
//
// 200 rather than 201, on both the first call and the second. The endpoint is
// idempotent per session — a second POST returns the share that already exists
// — so a 201 would be a claim about what this request did that is true only the
// first time, and a client cannot tell which time it is.
func (s *Server) handleShareSession(w http.ResponseWriter, r *http.Request) {
	sh, err := s.tasks.ShareSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sh)
}

// handleGetSessionShare answers GET /v1/sessions/{id}/share.
func (s *Server) handleGetSessionShare(w http.ResponseWriter, r *http.Request) {
	sh, err := s.tasks.SessionShare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sh)
}

// handleRevokeShare answers DELETE /v1/sessions/{id}/share.
//
// The body is the `{id, deleted}` envelope the other two DELETEs use, and the
// id in it is the share's, not the session's: the session is still there, and
// naming it in a response that says `deleted: true` would read as though it
// were not.
func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	// The id in the answer comes back from the revoke itself rather than from a
	// read before it, so it names the link this request actually withdrew — see
	// [service.TaskService.RevokeShare].
	shareID, err := s.tasks.RevokeShare(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeDeleted(w, shareID)
}

// handleSharedSession answers GET /v1/shares/{share_id}: the conversation and
// the harness that ran it, to whoever holds the id.
func (s *Server) handleSharedSession(w http.ResponseWriter, r *http.Request) {
	view, err := s.tasks.SharedSession(r.Context(), r.PathValue("share_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSharedTurns answers GET /v1/shares/{share_id}/turns.
func (s *Server) handleSharedTurns(w http.ResponseWriter, r *http.Request) {
	turns, err := s.tasks.SharedTurns(r.Context(), r.PathValue("share_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if turns == nil {
		turns = []uhp.TurnItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"turns": turns})
}

// handleSharedFiles answers GET /v1/shares/{share_id}/files.
//
// The listing is [domain.Artifact], the same object GET /v1/sessions/{id}/files
// renders, which means each entry carries a `download_url` under
// /v1/containers/ — a path that needs a credential. That is not a mistake and
// it is not a leak: the ids in it are already in the response, the URL is the
// canonical place those bytes live for a client that has one, and a viewer
// holding only a link fetches them from this share's own file path instead.
// Rewriting the field per-viewer would mean two renderings of one object.
func (s *Server) handleSharedFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.tasks.SharedFiles(r.Context(), r.PathValue("share_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if files == nil {
		files = []domain.Artifact{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// handleSharedArtifact answers GET /v1/shares/{share_id}/files/{file_id}/content.
//
// There is no container id in the path, and that absence is the scoping: the
// container is derived from the session the share names, so a file id from a
// different session resolves to nothing rather than to a check someone has to
// remember. The `nosniff` header matters more here than on the authenticated
// download for the obvious reason — these bytes are whatever an agent wrote,
// and this is the path a stranger with a link fetches them from.
func (s *Server) handleSharedArtifact(w http.ResponseWriter, r *http.Request) {
	a, f, err := s.tasks.OpenSharedArtifact(r.Context(), r.PathValue("share_id"), r.PathValue("file_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer func() { _ = f.Close() }()

	mediaType := a.MimeType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", contentDisposition(a.BaseName()))
	w.Header().Set("Content-Length", strconv.FormatInt(a.Bytes, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		s.log.Debug("shared artifact download interrupted", "error", err)
	}
}

// withSharedHeaders stamps the three headers every anonymous share response
// carries — success and failure alike.
//
// All three exist because the credential is in the URL, which is the one thing
// about this design that cannot be changed: a link is what a share is. That
// makes the URL itself the secret, and a secret in a URL leaks through the
// three channels these close — a cache that keeps the body, a crawler that
// indexes the address, and a Referer header that forwards it to whatever the
// viewer clicks next.
//
// It is middleware rather than a call inside each handler, and that is the
// whole point of the change that introduced it. Set per-handler, the headers
// went on the 200 and were skipped on every error return — and an error
// response is the same URL, so a crawler that reached a revoked link would
// still have been free to index the address it reached it by. Middleware runs
// before the handler picks a status, so there is no path that can forget.
func withSharedHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, private")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next(w, r)
	}
}

func writeSessionSharingUnsupported(w http.ResponseWriter) {
	writeError(w, http.StatusNotImplemented, typeServerError, vendorCodeSessionSharingUnsupported,
		"this server does not offer session sharing")
}

// writeShareNotFound is the single answer for an id that never existed, an id
// that was revoked, and an id whose session has been deleted.
//
// One answer for all three, the way writeFileNotFound is: the caller here
// presented no credential, so telling it which of the three it hit would let it
// learn that an id it guessed was once real.
func writeShareNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, typeInvalidRequest, vendorCodeShareNotFound,
		"no such share")
}
