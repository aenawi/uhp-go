package domain

import (
	"encoding/json"
	"strings"
	"time"
)

// The Files chapter names three identifier spaces, and this file is where they
// meet: a session owns a container, a container holds artifacts, and an upload
// is a file a client sent before any task existed.
//
// Ids are opaque on purpose. An artifact id derived from — or worse, equal to —
// a path is the single most likely serious vulnerability in a UHP server
// (Files §5), because every download handler then becomes a path-joining
// exercise against attacker-influenced input. Here an id carries no path at
// all: it is a digest, and resolving one means looking up a record the server
// itself wrote.

// ContainerIDFor returns the id of a session's file container.
//
// A session and its container are the same thing seen from two chapters, so the
// mapping is a prefix swap rather than a second table: it round-trips exactly,
// survives a restart, and cannot drift out of sync with the session it names.
func ContainerIDFor(sessionID string) string {
	if !ValidSessionID(sessionID) {
		return ""
	}
	return "cntr_" + strings.TrimPrefix(sessionID, "sess_")
}

// SessionIDFromContainer inverts ContainerIDFor. It reports false for anything
// that is not a container id this server could have minted, so a malformed or
// hostile id is refused before it reaches storage or the filesystem.
func SessionIDFromContainer(containerID string) (string, bool) {
	if !strings.HasPrefix(containerID, "cntr_") {
		return "", false
	}
	sessionID := "sess_" + strings.TrimPrefix(containerID, "cntr_")
	if !ValidSessionID(sessionID) {
		return "", false
	}
	return sessionID, true
}

// ValidSessionID allows only the shape the router itself mints: "sess_" plus
// hex and dashes. Session ids end up in filesystem paths and in container ids,
// so they are validated rather than trusted at every boundary that consumes
// one.
func ValidSessionID(id string) bool {
	if !strings.HasPrefix(id, "sess_") || len(id) > 128 {
		return false
	}
	rest := id[len("sess_"):]
	if rest == "" {
		return false
	}
	for _, r := range rest {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r == '-':
		default:
			return false
		}
	}
	return true
}

// Artifact is a file produced by a harness run (UHP "Files" chapter).
//
// Path is the artifact's location relative to its container's directory. It is
// server-derived — the result of walking the session's own working directory —
// and it is never taken from a client, which is what lets the download handler
// treat it as trusted after checking the id.
type Artifact struct {
	ID          string
	ContainerID string
	Path        string
	MimeType    string
	SizeBytes   int64
	CreatedAt   time.Time
}

// BaseName is the last element of the artifact's path, for the one place that
// needs a single element rather than a location: a download's
// Content-Disposition.
func (a Artifact) BaseName() string {
	if i := strings.LastIndex(a.Path, "/"); i >= 0 {
		return a.Path[i+1:]
	}
	return a.Path
}

// DownloadPath is where this artifact's bytes are served from (Files §3).
func (a Artifact) DownloadPath() string {
	return "/v1/containers/" + a.ContainerID + "/files/" + a.ID + "/content"
}

// MarshalJSON emits the wire `file` object (Files §2.2). `filename` carries the
// path within the container, not just the base name: two artifacts called
// report.md in different directories are different files, and a client that saw
// only the base name could not tell them apart.
func (a Artifact) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		ContainerID string `json:"container_id"`
		Filename    string `json:"filename"`
		MimeType    string `json:"mime_type"`
		Bytes       int64  `json:"bytes"`
		CreatedAt   int64  `json:"created_at"`
		DownloadURL string `json:"download_url"`
	}{
		ID:          a.ID,
		Object:      "file",
		ContainerID: a.ContainerID,
		Filename:    a.Path,
		MimeType:    a.MimeType,
		Bytes:       a.SizeBytes,
		CreatedAt:   a.CreatedAt.Unix(),
		DownloadURL: a.DownloadPath(),
	})
}

// Annotation cites an artifact from within assistant text (Files §2.1).
//
// Type is `container_file_citation`, which the schema pins as a constant: an
// annotation exists to point a client at a file it can then download, so it
// carries the container and file ids and the URL that serves the bytes.
type Annotation struct {
	Type        string `json:"type"`
	ContainerID string `json:"container_id,omitempty"`
	FileID      string `json:"file_id,omitempty"`
	Filename    string `json:"filename,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

// AnnotationTypeFileCitation is the only annotation type this server emits.
const AnnotationTypeFileCitation = "container_file_citation"

// Cite builds the annotation that points at this artifact. baseURL is the
// server's externally reachable origin; empty means "serve a relative URL",
// which is correct whenever the client and the API share an origin and is the
// only honest answer when the operator has not told us what the origin is.
func (a Artifact) Cite(baseURL string) Annotation {
	return Annotation{
		Type:        AnnotationTypeFileCitation,
		ContainerID: a.ContainerID,
		FileID:      a.ID,
		Filename:    a.Path,
		DownloadURL: strings.TrimSuffix(baseURL, "/") + a.DownloadPath(),
	}
}

// Upload is a file a client sent ahead of a task (Files §1.2), held until a
// task references it by id.
type Upload struct {
	ID        string
	Filename  string
	MimeType  string
	Data      []byte
	CreatedAt time.Time
}

func (u Upload) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID        string `json:"id"`
		Object    string `json:"object"`
		Filename  string `json:"filename"`
		MimeType  string `json:"mime_type"`
		Bytes     int    `json:"bytes"`
		CreatedAt int64  `json:"created_at"`
	}{
		ID:        u.ID,
		Object:    "file",
		Filename:  u.Filename,
		MimeType:  u.MimeType,
		Bytes:     len(u.Data),
		CreatedAt: u.CreatedAt.Unix(),
	})
}
