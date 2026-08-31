package main

import (
	"encoding/json"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
)

// TestTurnFieldsFallBackToThePreSpecNames covers the case uhpc exists for: a
// server that is not this one.
//
// Sessions §3 named a turn's response id `id` in harnessrouter#53. Servers that
// answered the endpoint before that answer `response_id`, and they are
// conformant — the shape they were written against had none. A client that read
// only the new name would print a blank column against them and make its own
// age look like their defect.
func TestTurnFieldsFallBackToThePreSpecNames(t *testing.T) {
	for _, tc := range []struct {
		name             string
		body             string
		wantID, wantUser string
	}{{
		name:     "a server that has adopted §3",
		body:     `{"id":"resp_new","status":"completed","user":"hello","assistant":"hi"}`,
		wantID:   "resp_new",
		wantUser: "hello",
	}, {
		name:     "a server that predates it",
		body:     `{"response_id":"resp_old","status":"completed","input":"hello","output":"hi"}`,
		wantID:   "resp_old",
		wantUser: "hello",
	}, {
		name:     "a server answering both, as uhpd does for one release",
		body:     `{"id":"resp_both","response_id":"resp_both","status":"completed","user":"hello","input":"hello"}`,
		wantID:   "resp_both",
		wantUser: "hello",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			var turn uhp.TurnItem
			if err := json.Unmarshal([]byte(tc.body), &turn); err != nil {
				t.Fatalf("unmarshal turn: %v", err)
			}
			if got := turnID(turn); got != tc.wantID {
				t.Errorf("turnID = %q, want %q", got, tc.wantID)
			}
			if got := turnUser(turn); got != tc.wantUser {
				t.Errorf("turnUser = %q, want %q", got, tc.wantUser)
			}
		})
	}
}
