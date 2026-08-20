package domain

import "time"

// Session tracks a continued conversation across multiple tasks — UHP's
// "Sessions" chapter: continuing a conversation, and cancelling one.
type Session struct {
	ID              string    `json:"id"`
	HarnessID       string    `json:"harness_id"`
	NativeSessionID string    `json:"native_session_id,omitempty"` // harness's own session/thread id
	LastResponseID  string    `json:"last_response_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
