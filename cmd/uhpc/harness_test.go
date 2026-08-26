package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
)

// harnessBody is what uhp-go answers a harness read with: the protocol's
// thirteen fields plus the three this server computes on every read.
const harnessBody = `{"id":"chrn_x","object":"harness","name":"Ex","base":"echo",
	"defaultModel":"m-1","systemPrompt":"","mcpServers":[],"skills":[],
	"disabledTools":[],"maxStep":null,"timeoutSeconds":null,"createdAt":0,
	"models":["m-1","m-2"],"capabilities":["streaming","sessions"],"status":"ready"}`

// harnessServer answers both harness reads with body, which a test varies to
// stand in for a server that extends the object and one that does not.
func harnessServer(t *testing.T, body string) *cli {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("UHP-Version", uhp.Version)
		if r.URL.Path == "/v1/harnesses" {
			fmt.Fprintf(w, `{"object":"list","harnesses":[%s]}`, body)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return &cli{client: uhp.NewClient(srv.URL, ""), out: &bytes.Buffer{}}
}

func (c *cli) output() string { return c.out.(*bytes.Buffer).String() }

// Whether a harness is reachable is the first thing anyone asks a router, and
// until this it was the one thing uhpc could not answer: `status` has no field
// on [uhp.Harness], so the decoder dropped it and the CLI printed a harness
// that might or might not run anything.
func TestHarnessShowsStatusAndModels(t *testing.T) {
	c := harnessServer(t, harnessBody)
	if err := c.harness(context.Background(), []string{"chrn_x"}); err != nil {
		t.Fatalf("harness: %v", err)
	}

	out := c.output()
	for _, want := range []string{"chrn_x", "ready", "m-2", "sessions"} {
		if !strings.Contains(out, want) {
			t.Errorf("harness output is missing %q:\n%s", want, out)
		}
	}
}

// -json is the form the issue was reported in — `uhpc -json harness … | jq
// '{status, models}'` answering two nulls — so it is asserted on the keys
// rather than on the rendering.
func TestHarnessJSONCarriesTheExtensions(t *testing.T) {
	c := harnessServer(t, harnessBody)
	c.json = true
	if err := c.harness(context.Background(), []string{"chrn_x"}); err != nil {
		t.Fatalf("harness: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(c.output()), &got); err != nil {
		t.Fatalf("decode %q: %v", c.output(), err)
	}
	if got["status"] != "ready" {
		t.Errorf("status = %v, want ready", got["status"])
	}
	if models, _ := got["models"].([]any); len(models) != 2 {
		t.Errorf("models = %v, want the two the server sent", got["models"])
	}
	// The protocol half has to survive the same round trip: the extension type
	// embeds uhp.Harness and marshals both halves, and a splice that lost the
	// protocol fields would be a worse bug than the one being fixed.
	if got["base"] != "echo" {
		t.Errorf("base = %v, want echo", got["base"])
	}
}

func TestHarnessesListsAStatusColumn(t *testing.T) {
	c := harnessServer(t, harnessBody)
	if err := c.harnesses(context.Background()); err != nil {
		t.Fatalf("harnesses: %v", err)
	}
	if out := c.output(); !strings.Contains(out, "ready") {
		t.Errorf("no status in the listing:\n%s", out)
	}
}

// uhpc drives any conformant server, and status is this one's extension. A
// server that sends none of the three is not broken and must not be rendered as
// though it were: the protocol fields still print, and the column that has no
// answer says so rather than claiming a state.
func TestHarnessAgainstAServerThatSendsNoExtensions(t *testing.T) {
	c := harnessServer(t, `{"id":"chrn_x","object":"harness","name":"Ex","base":"echo"}`)
	if err := c.harnesses(context.Background()); err != nil {
		t.Fatalf("harnesses: %v", err)
	}
	out := c.output()
	if !strings.Contains(out, "chrn_x") || !strings.Contains(out, "echo") {
		t.Errorf("the protocol fields did not survive:\n%s", out)
	}
	for _, unwanted := range []string{"ready", "unavailable", "degraded"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("invented a status %q for a server that sent none:\n%s", unwanted, out)
		}
	}
}

// The rendering promise the text column makes has to hold in -json too: a
// server that reported none of the three is rendered as the protocol object,
// not as this server's object with three empty answers filled in. `"status":
// ""` reads as a state, and `uhpc` was told no such thing.
func TestHarnessJSONInventsNothingForAServerThatSendsNoExtensions(t *testing.T) {
	c := harnessServer(t, `{"id":"chrn_x","object":"harness","name":"Ex","base":"echo"}`)
	c.json = true
	if err := c.harness(context.Background(), []string{"chrn_x"}); err != nil {
		t.Fatalf("harness: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(c.output()), &got); err != nil {
		t.Fatalf("decode %q: %v", c.output(), err)
	}
	if got["id"] != "chrn_x" {
		t.Errorf("the protocol half did not survive: %v", got)
	}
	for _, key := range []string{"status", "models", "capabilities"} {
		if _, ok := got[key]; ok {
			t.Errorf("invented %q = %v for a server that sent none", key, got[key])
		}
	}
}

// And the same for the listing, which renders each harness on its own terms —
// a router where one harness reports its status and another does not is a
// server misconfiguration, not something for uhpc to paper over.
func TestHarnessesJSONInventsNothingForAServerThatSendsNoExtensions(t *testing.T) {
	c := harnessServer(t, `{"id":"chrn_x","object":"harness","name":"Ex","base":"echo"}`)
	c.json = true
	if err := c.harnesses(context.Background()); err != nil {
		t.Fatalf("harnesses: %v", err)
	}

	var got []map[string]any
	if err := json.Unmarshal([]byte(c.output()), &got); err != nil {
		t.Fatalf("decode %q: %v", c.output(), err)
	}
	if len(got) != 1 {
		t.Fatalf("harnesses = %v, want the one listed", got)
	}
	for _, key := range []string{"status", "models", "capabilities"} {
		if _, ok := got[0][key]; ok {
			t.Errorf("invented %q = %v for a server that sent none", key, got[0][key])
		}
	}
}
