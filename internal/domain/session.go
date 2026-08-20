package domain

import (
	"encoding/json"
	"time"
)

// Session tracks a continued conversation across multiple tasks.
//
// A session preserves, across the tasks in its chain: conversational context,
// the working directory and its files, and the configured harness.
type Session struct {
	ID        string
	HarnessID string
	Title     string
	Status    TaskStatus
	CreatedAt time.Time
	UpdatedAt time.Time

	// Internal bookkeeping, not part of the wire object.
	NativeSessionID string
	LastResponseID  string
}

func (s Session) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID        string     `json:"id"`
		Object    string     `json:"object"`
		HarnessID string     `json:"harness_id"`
		Title     string     `json:"title"`
		Status    TaskStatus `json:"status"`
		CreatedAt int64      `json:"created_at"`
		UpdatedAt int64      `json:"updated_at"`
	}{
		ID:        s.ID,
		Object:    "session",
		HarnessID: s.HarnessID,
		Title:     s.Title,
		Status:    s.Status,
		CreatedAt: s.CreatedAt.Unix(),
		UpdatedAt: s.UpdatedAt.Unix(),
	})
}

// Turn is one task in a session's history. It carries enough to rebuild a
// transcript, and identifies its response id so a client can fetch the whole
// response for any turn it wants in full.
type Turn struct {
	ResponseID string     `json:"response_id"`
	Status     TaskStatus `json:"status"`
	Model      string     `json:"model"`
	Input      string     `json:"input"`
	Output     string     `json:"output"`
	CreatedAt  int64      `json:"created_at"`
}

// SessionFilter selects and pages a session listing.
type SessionFilter struct {
	HarnessID string
	Limit     int
	// Cursor is opaque to the client and is the id of the last session on the
	// previous page.
	Cursor string
}

// SessionPage is one page of a session listing.
//
// NextCursor is empty on the last page. UHP forbids making a client infer the
// end from a short page: that heuristic is wrong whenever a page is exactly
// full, and a client cannot tell the two cases apart.
type SessionPage struct {
	Sessions   []*Session
	NextCursor string
}
