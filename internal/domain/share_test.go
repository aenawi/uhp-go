package domain

import (
	"strings"
	"testing"
)

func TestNewShareIDIsUnguessable(t *testing.T) {
	seen := make(map[string]bool, 64)
	for range 64 {
		id, err := NewShareID()
		if err != nil {
			t.Fatalf("mint share id: %v", err)
		}
		if !ValidShareID(id) {
			t.Fatalf("minted id %q is not one this server accepts back", id)
		}
		if seen[id] {
			t.Fatalf("minted %q twice in 64 draws", id)
		}
		seen[id] = true
	}
}

// The id is the whole credential, so its length is a security property rather
// than a formatting choice. 32 bytes of entropy is 64 hex characters.
func TestShareIDCarriesFullEntropy(t *testing.T) {
	id, err := NewShareID()
	if err != nil {
		t.Fatalf("mint share id: %v", err)
	}
	body := strings.TrimPrefix(id, "shr_")
	if len(body) != shareIDBytes*2 {
		t.Fatalf("share id body is %d characters, want %d", len(body), shareIDBytes*2)
	}
}

func TestValidShareIDRefusesAnythingThisServerDidNotMint(t *testing.T) {
	valid := "shr_" + strings.Repeat("ab", shareIDBytes)
	if !ValidShareID(valid) {
		t.Fatalf("%q should be valid", valid)
	}
	for _, id := range []string{
		"",
		"shr_",
		"sess_1234",
		strings.Repeat("ab", shareIDBytes), // no prefix
		"shr_" + strings.Repeat("ab", shareIDBytes-1),      // too short
		"shr_" + strings.Repeat("ab", shareIDBytes) + "aa", // too long
		"shr_" + strings.Repeat("AB", shareIDBytes),        // upper case
		"shr_" + strings.Repeat("ag", shareIDBytes),        // not hex
		"shr_../../etc/passwd",
	} {
		if ValidShareID(id) {
			t.Errorf("ValidShareID(%q) = true, want false", id)
		}
	}
}

// A relative URL is the honest answer when nobody has told the server its own
// origin, and is what Artifact.Cite already does with the same input.
func TestShareURLFollowsTheConfiguredOrigin(t *testing.T) {
	sh := Share{ID: "shr_abc", SessionID: "sess_1", CreatedAt: 7}

	if got := sh.Wire("").URL; got != "/v1/shares/shr_abc" {
		t.Errorf("relative URL = %q", got)
	}
	if got := sh.Wire("https://uhp.example.com").URL; got != "https://uhp.example.com/v1/shares/shr_abc" {
		t.Errorf("absolute URL = %q", got)
	}
	if got := sh.Wire("https://uhp.example.com/").URL; got != "https://uhp.example.com/v1/shares/shr_abc" {
		t.Errorf("trailing-slash origin = %q", got)
	}
}

func TestShareWireCarriesTheObjectConstant(t *testing.T) {
	w := Share{ID: "shr_abc", SessionID: "sess_1", CreatedAt: 7}.Wire("")
	if w.Object != "session.share" {
		t.Errorf("object = %q, want session.share", w.Object)
	}
	if w.ID != "shr_abc" || w.SessionID != "sess_1" || w.CreatedAt != 7 {
		t.Errorf("wire share = %+v", w)
	}
}
