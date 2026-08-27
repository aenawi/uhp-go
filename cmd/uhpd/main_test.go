package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"
	"github.com/aenawi/uhp-go/uhp"
)

// capture returns a logger and the lines it wrote, decoded.
func capture() (*slog.Logger, func(t *testing.T) []map[string]any) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return log, func(t *testing.T) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("log line is not JSON: %q", line)
			}
			out = append(out, rec)
		}
		return out
	}
}

func seedShare(t *testing.T, s service.Store, sessionID, shareID string) {
	t.Helper()
	ctx := context.Background()
	sess := &domain.Session{Session: uhp.Session{
		ID: sessionID, Object: "session", HarnessID: "chrn_echo", CreatedAt: 1,
	}}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := s.CreateShare(ctx, &domain.Share{ID: shareID, SessionID: sessionID, CreatedAt: 1}); err != nil {
		t.Fatalf("create share: %v", err)
	}
}

// The line #68 is about: a server started without UHP_SESSION_SHARING that is
// still holding shares says so, because turning the capability off suspends
// them rather than revoking them.
func TestSuspendedSharesAreReportedAtStartup(t *testing.T) {
	s := store.NewMemoryStore()
	seedShare(t, s, "sess_a", "shr_a")
	seedShare(t, s, "sess_b", "shr_b")

	log, lines := capture()
	warnSuspendedShares(context.Background(), s, log)

	recs := lines(t)
	if len(recs) != 1 {
		t.Fatalf("wrote %d lines, want one: %v", len(recs), recs)
	}
	rec := recs[0]
	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", rec["level"])
	}
	if n, _ := rec["shares"].(float64); n != 2 {
		t.Errorf("shares = %v, want 2", rec["shares"])
	}
	msg, _ := rec["msg"].(string)
	// The distinction the message exists to draw. An operator who reads this
	// and hears "revoked" has been told the opposite of what happened.
	if !strings.Contains(msg, "suspended") || !strings.Contains(msg, "not revoked") {
		t.Errorf("msg does not say the shares are suspended rather than revoked: %q", msg)
	}
	if hint, _ := rec["hint"].(string); !strings.Contains(hint, "UHP_SESSION_SHARING") {
		t.Errorf("hint does not name the way back to revoking them: %q", rec["hint"])
	}
}

// Silence when there is nothing suspended, which is nearly every deployment.
// A line on every start is a line nobody reads on the start that mattered.
func TestNoSuspendedSharesIsSilent(t *testing.T) {
	log, lines := capture()
	warnSuspendedShares(context.Background(), store.NewMemoryStore(), log)
	if recs := lines(t); len(recs) != 0 {
		t.Fatalf("a store with no shares logged %v", recs)
	}
}

// failingShares is a store whose count cannot be read. The embedded interface
// is nil: nothing else is called on this path, and a method that is would
// panic here rather than quietly do something.
type failingShares struct{ service.Store }

func (failingShares) CountShares(context.Context) (int, error) {
	return 0, errors.New("disk went away")
}

// A store that will not answer is a warning, not an exit. This is a courtesy
// about state the server does not serve; refusing to start over it would make
// a read that nothing depends on into a boot dependency.
func TestAnUnreadableShareCountIsNotFatal(t *testing.T) {
	log, lines := capture()
	warnSuspendedShares(context.Background(), failingShares{}, log)

	recs := lines(t)
	if len(recs) != 1 || recs[0]["level"] != "WARN" {
		t.Fatalf("an unreadable count logged %v", recs)
	}
	if msg, _ := recs[0]["error"].(string); !strings.Contains(msg, "disk went away") {
		t.Errorf("the failure is not in the line: %v", recs[0])
	}
}
