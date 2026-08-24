package http

import (
	"archive/zip"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
)

// handleUploadFile answers POST /v1/files (Files §1.2).
//
// Upload exists so a 40 MB input is not base64-inflated through every retry.
// The size limit is the server's configured body limit, and exceeding it is a
// 413 with `file_too_large` — never a truncated file, because a silently
// truncated input produces a confident, wrong answer.
func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	if !s.tasks.FilesEnabled() {
		writeFilesUnsupported(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	if err := r.ParseMultipartForm(uploadMemoryBytes); err != nil {
		if writeIfTooLarge(w, err, s.maxBodyBytes) {
			return
		}
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"the request body could not be parsed as multipart/form-data")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErrorParam(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input",
			"a multipart field named 'file' is required", "file")
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		if writeIfTooLarge(w, err, s.maxBodyBytes) {
			return
		}
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_input", "the upload could not be read")
		return
	}

	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = mime.TypeByExtension(filepath.Ext(header.Filename))
	}
	up, err := s.tasks.StoreUpload(r.Context(), header.Filename, mediaType, data)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, up)
}

// uploadMemoryBytes is how much of a multipart upload is buffered in memory
// before net/http spills the rest to a temporary file.
const uploadMemoryBytes = 8 << 20

// handleSessionFiles answers GET /v1/sessions/{id}/files (Files §2.2).
func (s *Server) handleSessionFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.tasks.SessionFiles(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if files == nil {
		files = []domain.Artifact{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// handleSessionArchive answers GET /v1/sessions/{id}/files/archive (Files §4):
// a session that produced forty files should not require forty requests.
func (s *Server) handleSessionArchive(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	files, err := s.tasks.SessionFiles(r.Context(), sessionID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	// Headers first: once bytes are on the wire an error cannot be reported as
	// a status code, so everything that can fail late is deliberately not
	// allowed to change the response shape.
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", contentDisposition(sessionID+".zip"))
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()
	for _, a := range files {
		_, f, err := s.tasks.OpenArtifact(r.Context(), a.ContainerID, a.ID)
		if err != nil {
			// A file recorded but since deleted is skipped: the rest of the
			// archive is still what the client asked for.
			s.log.Warn("artifact missing from archive", "session_id", sessionID, "file_id", a.ID)
			continue
		}
		entry, err := zw.Create(a.Filename)
		if err == nil {
			_, err = io.Copy(entry, f)
		}
		_ = f.Close()
		if err != nil {
			s.log.Error("write archive entry", "error", err, "session_id", sessionID)
			return
		}
	}
}

// handleDownloadArtifact answers
// GET /v1/containers/{container_id}/files/{file_id}/content (Files §3).
//
// Raw bytes, never JSON-wrapped, and always with `X-Content-Type-Options:
// nosniff`: an artifact is content an agent can be persuaded to write, so
// serving it without nosniff turns it into stored XSS against the client's own
// origin.
func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	a, f, err := s.tasks.OpenArtifact(r.Context(), r.PathValue("container_id"), r.PathValue("file_id"))
	if err != nil {
		// Through writeServiceError rather than straight to writeFileNotFound.
		// ErrArtifactNotFound still lands on exactly that answer, so the single
		// reply for "no such container", "no such file" and "not yours" is
		// untouched — but a store that could not be read is none of those three,
		// and calling it a missing file tells a client never to ask again for
		// the one condition where asking again is what works.
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
	// The header names one file, so it carries the base name even though the
	// listing's `filename` carries the path within the container.
	w.Header().Set("Content-Disposition", contentDisposition(a.BaseName()))
	w.Header().Set("Content-Length", strconv.FormatInt(a.Bytes, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		s.log.Debug("artifact download interrupted", "error", err)
	}
}

// handlePreviewArtifact answers
// GET /v1/containers/{container_id}/files/{file_id}/pdf (Files §3.1).
//
// This server does not convert documents, and the specification is explicit
// that the honest answer is 501 `preview_unavailable` rather than a broken or
// empty PDF: a client can then tell "this server never previews" from "this
// file would not convert", and only the second is worth retrying.
func (s *Server) handlePreviewArtifact(w http.ResponseWriter, r *http.Request) {
	_, f, err := s.tasks.OpenArtifact(r.Context(), r.PathValue("container_id"), r.PathValue("file_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	_ = f.Close()
	writeError(w, http.StatusNotImplemented, typeServerError, "preview_unavailable",
		"this server does not render document previews")
}

// contentDisposition names the download without letting a filename break out
// of the header. mime.FormatMediaType escapes what it can and refuses what it
// cannot, and an unnameable file is still a downloadable one.
func contentDisposition(filename string) string {
	filename = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, filename)
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": filename}); v != "" {
		return v
	}
	return "attachment"
}

func writeFileNotFound(w http.ResponseWriter) {
	// One answer for "no such container", "no such file", and "not yours".
	// Distinguishing them would let a caller enumerate other principals' files
	// (Files §5).
	writeError(w, http.StatusNotFound, typeInvalidRequest, "file_not_found",
		"no such file in this container")
}

func writeFilesUnsupported(w http.ResponseWriter) {
	writeError(w, http.StatusNotImplemented, typeServerError, vendorCodeFilesUnsupported,
		"this server is not configured with a workspace, so it cannot accept file input or produce artifacts")
}

// vendorCodeFilesUnsupported is namespaced because the specification's code
// list has no entry for "this deployment is not configured for files", and
// Errors §3 requires an additional code to carry a vendor prefix so a future
// version of the specification cannot collide with it.
const vendorCodeFilesUnsupported = "uhpgo_files_unsupported"
