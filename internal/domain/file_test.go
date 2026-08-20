package domain

import (
	"encoding/json"
	"testing"
	"time"
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
		ID:          "file_abc",
		ContainerID: "cntr_1",
		Path:        "reports/q3.md",
		MimeType:    "text/markdown",
		SizeBytes:   12,
		CreatedAt:   time.Unix(1786400240, 0),
	}
	var got map[string]any
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for k, want := range map[string]any{
		"id":           "file_abc",
		"object":       "file",
		"container_id": "cntr_1",
		"filename":     "reports/q3.md",
		"bytes":        float64(12),
		"created_at":   float64(1786400240),
		"download_url": "/v1/containers/cntr_1/files/file_abc/content",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if a.BaseName() != "q3.md" {
		t.Errorf("BaseName() = %q, want q3.md", a.BaseName())
	}
}

func TestCiteUsesTheConfiguredOrigin(t *testing.T) {
	a := Artifact{ID: "file_abc", ContainerID: "cntr_1", Path: "q3.md"}
	if got := a.Cite("https://uhp.example.com/").DownloadURL; got != "https://uhp.example.com/v1/containers/cntr_1/files/file_abc/content" {
		t.Errorf("absolute download url = %q", got)
	}
	if got := a.Cite("").DownloadURL; got != "/v1/containers/cntr_1/files/file_abc/content" {
		t.Errorf("relative download url = %q", got)
	}
	if got := a.Cite("").Type; got != AnnotationTypeFileCitation {
		t.Errorf("annotation type = %q, want %q", got, AnnotationTypeFileCitation)
	}
}
