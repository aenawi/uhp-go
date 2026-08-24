package domain

import (
	"encoding/json"
	"strings"

	"github.com/aenawi/uhp-go/uhp"
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

// Artifact is a file produced by a harness run: the internal word for one of
// the [uhp.File] objects a run reports. Every Artifact is reported as a File;
// not every File is an Artifact.
//
// Filename is the artifact's location relative to its container's directory,
// not just its base name — two files called report.md in different directories
// are different files, and a client shown only the base name could not tell
// them apart. It is server-derived, the result of walking the session's own
// working directory, and it is never taken from a client, which is what lets
// the download handler treat it as trusted after checking the id.
type Artifact struct {
	uhp.File

	// MimeType is an extension: the schema's file object has six properties
	// and this is not one of them. It is legal (every object there is
	// additionalProperties: true) and useful to a client deciding whether to
	// render or download, but a client must not expect it elsewhere.
	MimeType string `json:"mime_type,omitempty"`
}

// BaseName is the last element of the artifact's path, for the one place that
// needs a single element rather than a location: a download's
// Content-Disposition.
func (a Artifact) BaseName() string {
	if i := strings.LastIndex(a.Filename, "/"); i >= 0 {
		return a.Filename[i+1:]
	}
	return a.Filename
}

// DownloadPath is where this artifact's bytes are served from (Files §3).
func (a Artifact) DownloadPath() string {
	return "/v1/containers/" + a.ContainerID + "/files/" + a.ID + "/content"
}

// MarshalJSON emits the wire file object plus this server's two additions.
//
// download_url is derived from the two ids rather than stored, so it cannot
// disagree with them, and `object` is defaulted here rather than at every
// construction site for the same reason: a constant that every caller has to
// remember is a constant one caller will forget.
func (a Artifact) MarshalJSON() ([]byte, error) {
	// A distinct type so that marshalling the copy does not re-enter this
	// method. uhp.File carries no marshaller of its own, so nothing is
	// promoted onto it and the struct tags do the work.
	type artifact Artifact
	out := artifact(a)
	if out.Object == "" {
		out.Object = "file"
	}
	return json.Marshal(struct {
		artifact
		DownloadURL string `json:"download_url"`
	}{out, a.DownloadPath()})
}

// Cite builds the annotation that points at this artifact. baseURL is the
// server's externally reachable origin; empty means "serve a relative URL",
// which is correct whenever the client and the API share an origin and is the
// only honest answer when the operator has not told us what the origin is.
func (a Artifact) Cite(baseURL string) uhp.Annotation {
	return uhp.Annotation{
		Type:        uhp.AnnotationTypeFileCitation,
		ContainerID: a.ContainerID,
		FileID:      a.ID,
		Filename:    a.Filename,
		DownloadURL: strings.TrimSuffix(baseURL, "/") + a.DownloadPath(),
	}
}
