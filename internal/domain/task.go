// Package domain contains the core UHP entities: Response (a task), Session,
// Harness, and Artifact.
package domain

import (
	"encoding/json"
	"time"
)

// TaskStatus is the response lifecycle (Lifecycle §3): one non-terminal state
// and four terminal ones. "incomplete" means a budget — a step or time limit —
// stopped the work, and MUST NOT be used for errors: the distinction tells a
// client whether continuing is worth trying.
type TaskStatus string

const (
	StatusInProgress TaskStatus = "in_progress"
	StatusCompleted  TaskStatus = "completed"
	StatusFailed     TaskStatus = "failed"
	StatusIncomplete TaskStatus = "incomplete"
	StatusCancelled  TaskStatus = "cancelled"
)

// Artifact is a file produced by a harness run (UHP "Files" chapter).
type Artifact struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MimeType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// Annotation cites an artifact from within assistant text.
type Annotation struct {
	Type     string `json:"type"`
	FileID   string `json:"file_id,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// ContentPart is one part of an output item's content.
type ContentPart struct {
	Type string `json:"type"` // "output_text"
	Text string `json:"text"`
	// Annotations is always present, never omitted: a client must be able to
	// tell "no annotations" from "this server predates the field".
	Annotations []Annotation `json:"annotations"`
}

// OutputItem is one element of a response's `output` array.
//
// UHP's output is an ordered array of typed items — message, reasoning,
// function_call, function_call_output — not a flat string. A client that
// renders only `message` items and ignores the rest is a valid client.
type OutputItem struct {
	ID      string        `json:"id,omitempty"`
	Type    string        `json:"type"`
	Status  string        `json:"status,omitempty"`
	Role    string        `json:"role,omitempty"`
	Content []ContentPart `json:"content,omitempty"`
	Summary []any         `json:"summary,omitempty"`
	CallID  string        `json:"call_id,omitempty"`
	Name    string        `json:"name,omitempty"`
	Args    string        `json:"arguments,omitempty"`
	Output  string        `json:"output,omitempty"`
}

// Usage accounts for tokens consumed.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// TaskError follows UHP's failure taxonomy: a stable machine code plus a human
// message, so clients can branch on Code without matching on prose.
type TaskError struct {
	Type      string `json:"type,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// Task is UHP's response object.
//
// The wire shape is not the internal shape, so marshalling is explicit rather
// than tag-driven: `created_at` is Unix seconds, `session_id` lives inside
// `metadata`, and `error`, `usage`, `incomplete_details` and
// `previous_response_id` are always present as explicit nulls. Omitting them
// would leave a client unable to distinguish "no value" from "this server is
// older than the field", which the specification calls out repeatedly.
type Task struct {
	ID                 string
	Object             string
	Status             TaskStatus
	Model              string
	Output             []OutputItem
	Usage              *Usage
	Error              *TaskError
	IncompleteDetails  map[string]any
	PreviousResponseID string
	Store              bool
	Metadata           map[string]any
	CreatedAt          time.Time
	UpdatedAt          time.Time

	// Internal bookkeeping, not part of the wire object.
	HarnessID       string
	SessionID       string
	Input           string
	RequestedModel  string
	Artifacts       []Artifact
	NativeSessionID string
}

// Text returns the assistant text assembled from the output items, which is
// what most callers and tests actually want to assert on.
func (t *Task) Text() string {
	var b []byte
	for _, it := range t.Output {
		if it.Type != "message" {
			continue
		}
		for _, c := range it.Content {
			b = append(b, c.Text...)
		}
	}
	return string(b)
}

// AppendText appends a text delta to the task's single assistant message item,
// creating it if this is the first delta. It returns the item's index and id.
func (t *Task) AppendText(delta string) (outputIndex int, itemID string) {
	for i := range t.Output {
		if t.Output[i].Type == "message" {
			if len(t.Output[i].Content) == 0 {
				t.Output[i].Content = []ContentPart{{Type: "output_text", Annotations: []Annotation{}}}
			}
			t.Output[i].Content[0].Text += delta
			return i, t.Output[i].ID
		}
	}
	item := OutputItem{
		ID:      "msg_" + t.ID,
		Type:    "message",
		Status:  "in_progress",
		Role:    "assistant",
		Content: []ContentPart{{Type: "output_text", Text: delta, Annotations: []Annotation{}}},
	}
	t.Output = append(t.Output, item)
	return len(t.Output) - 1, item.ID
}

// MessageItem returns the assistant message item, if one exists.
func (t *Task) MessageItem() (int, *OutputItem) {
	for i := range t.Output {
		if t.Output[i].Type == "message" {
			return i, &t.Output[i]
		}
	}
	return -1, nil
}

func (t Task) MarshalJSON() ([]byte, error) {
	meta := make(map[string]any, len(t.Metadata)+4)
	for k, v := range t.Metadata {
		meta[k] = v
	}
	// Lifecycle §4: the session id MUST be reported in metadata.session_id.
	if t.SessionID != "" {
		meta["session_id"] = t.SessionID
	}
	if t.HarnessID != "" {
		meta["harness_id"] = t.HarnessID
	}
	// Tasks §1.3: a client must always be able to answer "did the model I
	// asked for actually run?" by comparing model with requested_model.
	if t.RequestedModel != "" && t.RequestedModel != t.Model {
		meta["requested_model"] = t.RequestedModel
		meta["model_fallback"] = true
	}

	out := t.Output
	if out == nil {
		out = []OutputItem{}
	}

	var prev *string
	if t.PreviousResponseID != "" {
		p := t.PreviousResponseID
		prev = &p
	}

	return json.Marshal(struct {
		ID                 string         `json:"id"`
		Object             string         `json:"object"`
		CreatedAt          int64          `json:"created_at"`
		Status             TaskStatus     `json:"status"`
		Error              *TaskError     `json:"error"`
		IncompleteDetails  map[string]any `json:"incomplete_details"`
		PreviousResponseID *string        `json:"previous_response_id"`
		Model              string         `json:"model"`
		Output             []OutputItem   `json:"output"`
		Store              bool           `json:"store"`
		Usage              *Usage         `json:"usage"`
		Metadata           map[string]any `json:"metadata"`
	}{
		ID:                 t.ID,
		Object:             "response",
		CreatedAt:          t.CreatedAt.Unix(),
		Status:             t.Status,
		Error:              t.Error,
		IncompleteDetails:  t.IncompleteDetails,
		PreviousResponseID: prev,
		Model:              t.Model,
		Output:             out,
		Store:              t.Store,
		Usage:              t.Usage,
		Metadata:           meta,
	})
}
