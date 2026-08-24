package domain

import (
	"encoding/json"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
)

func TestContainerIDRoundTrips(t *testing.T) {
	sessionID := "sess_2f9c1b4a-7d3e-4c8a-9b0f-1e2d3c4b5a6f"
	cid := ContainerIDFor(sessionID)
	if cid == "" {
		t.Fatalf("no container id for a well-formed session id")
	}
	back, ok := SessionIDFromContainer(cid)
	if !ok || back != sessionID {
		t.Fatalf("round trip gave (%q, %v), want (%q, true)", back, ok, sessionID)
	}
}

// A container id is a key into server-owned records, so anything that is not a
// shape this server mints has to be refused before it can reach a lookup — let
// alone a filesystem path.
func TestContainerIDRejectsAnythingNotMinted(t *testing.T) {
	for _, bad := range []string{
		"", "cntr_", "sess_abc", "cntr_../../etc/passwd", "cntr_..%2f..%2fetc",
		"cntr_ABCDEF", "cntr_zz", "cntr_a/b", "cntr_a.b",
	} {
		if _, ok := SessionIDFromContainer(bad); ok {
			t.Errorf("container id %q was accepted", bad)
		}
	}
}

func TestArtifactMarshalsAsAFileObject(t *testing.T) {
	a := Artifact{
		File: uhp.File{
			ID:          "file_abc",
			ContainerID: "cntr_1",
			Filename:    "reports/q3.md",
			Bytes:       12,
			CreatedAt:   1786400240,
		},
		MimeType: "text/markdown",
	}
	var got map[string]any
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{
		"id":           "file_abc",
		"object":       "file",
		"container_id": "cntr_1",
		"filename":     "reports/q3.md",
		"mime_type":    "text/markdown",
		"bytes":        float64(12),
		"created_at":   float64(1786400240),
		"download_url": "/v1/containers/cntr_1/files/file_abc/content",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	// The whole key set, not just the keys wanted. Artifact marshals through a
	// struct that carries every field it has, so an internal one added later
	// reaches a client under its Go name — the same way it would have on
	// [Session], and with the same silence from a test that only asked whether
	// the fields it knew about were present.
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("artifact published %q, which is not part of the file object", k)
		}
	}
	if a.BaseName() != "q3.md" {
		t.Errorf("BaseName() = %q, want q3.md", a.BaseName())
	}
}

func TestCiteUsesTheConfiguredOrigin(t *testing.T) {
	a := Artifact{File: uhp.File{ID: "file_abc", ContainerID: "cntr_1", Filename: "q3.md"}}
	if got := a.Cite("https://uhp.example.com/").DownloadURL; got != "https://uhp.example.com/v1/containers/cntr_1/files/file_abc/content" {
		t.Errorf("absolute download url = %q", got)
	}
	if got := a.Cite("").DownloadURL; got != "/v1/containers/cntr_1/files/file_abc/content" {
		t.Errorf("relative download url = %q", got)
	}
	if got := a.Cite("").Type; got != uhp.AnnotationTypeFileCitation {
		t.Errorf("annotation type = %q, want %q", got, uhp.AnnotationTypeFileCitation)
	}
}
