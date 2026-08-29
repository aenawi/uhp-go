package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
)

// The storage shape is not the wire shape, and it gets its own declaration
// here for the same reason the wire shape gets one in domain.
//
// domain.Task, domain.Session and domain.Artifact each carry a MarshalJSON
// that renders UHP's object: it drops the internal bookkeeping a run needs
// (input, artifacts, the harness's own session id), writes timestamps as Unix
// seconds, and folds session_id into metadata. None of the three has an
// UnmarshalJSON to match, so marshalling one and reading it back would lose
// most of a run and round the rest to the second — silently, and only for the
// engine that serialises.
//
// Two declarations also mean a change to the wire format cannot rewrite what
// is on disk without someone deciding that it should.

type taskRecord struct {
	ID                 string             `json:"id"`
	Object             string             `json:"object"`
	Status             uhp.ResponseStatus `json:"status"`
	Model              string             `json:"model"`
	Output             []uhp.OutputItem   `json:"output"`
	Usage              *uhp.Usage         `json:"usage"`
	Error              *uhp.Error         `json:"error"`
	IncompleteDetails  map[string]any     `json:"incomplete_details"`
	PreviousResponseID *string            `json:"previous_response_id"`
	Store              bool               `json:"store"`
	Metadata           map[string]any     `json:"metadata"`
	// CreatedAt is Unix seconds, matching the wire object it came from;
	// UpdatedAt is a time.Time because it is internal and has no wire format to
	// match. The mismatch is the honest one: only one of the two is a
	// protocol field.
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	HarnessID       string            `json:"harness_id"`
	SessionID       string            `json:"session_id"`
	Input           string            `json:"input"`
	InputItems      []json.RawMessage `json:"input_items"`
	RequestedModel  string            `json:"requested_model"`
	Artifacts       []artifactRecord  `json:"artifacts"`
	NativeSessionID string            `json:"native_session_id"`
	TimeoutSeconds  int               `json:"timeout_seconds"`

	// MaxStep is the step ceiling this run was given, or absent for none (#72).
	// `omitempty` is deliberately not set: a pointer to zero is a real ceiling —
	// "call no tools" — and omitting it would read back as unbounded, which is
	// the one direction a budget must never be wrong in.
	MaxStep *int `json:"max_step"`

	IgnoredFields []string `json:"ignored_fields"`
}

// artifactRecord is domain.Artifact's fields rather than its wire object, which
// adds a download URL derived from the ids.
//
// The Go names follow the domain type and the JSON keys do not, which is
// deliberate on both counts. `path` and `size_bytes` are what previous builds
// wrote, and rows written then still have to decode; the field names are what
// TestSQLiteRecordsCoverDomainFields compares, and a record whose names drifted
// from the type it stores would make that check unable to tell a rename from a
// dropped field.
type artifactRecord struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	ContainerID string `json:"container_id"`
	Filename    string `json:"path"`
	MimeType    string `json:"mime_type"`
	Bytes       int64  `json:"size_bytes"`
	CreatedAt   int64  `json:"created_at"`
}

type sessionRecord struct {
	ID string `json:"id"`
	// Object is stored rather than reconstructed on read. It is a constant
	// today, and a record that leaves a field to be re-derived is one the
	// coverage check cannot see is missing.
	Object          string `json:"object"`
	HarnessID       string `json:"harness_id"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	NativeSessionID string `json:"native_session_id"`
	LastResponseID  string `json:"last_response_id"`
}

// Rows written before `object` was stored have none, so a decode falls back to
// the constant the field has always held. Reading a field that may be absent is
// the shape every additive change takes here: an old row decodes with the new
// field zero, and zero has to mean something the reader can use.
const (
	objectSession  = "session"
	objectFileKind = "file"
)

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// No field declared here carries `omitempty`, deliberately. A nil slice and an
// empty one are different values to the transport that renders them, and
// omitting the empty case would collapse the two on the way through storage —
// so the engine a deployment chose would decide what its clients see.
//
// The guarantee stops at the fields these records declare. Below them,
// uhp.OutputItem's own tags apply, and `content` and `summary` are
// `omitempty` there: an item with an empty-but-non-nil Content comes back nil
// from this engine and non-nil from MemoryStore. Nothing mints that value —
// Task.AppendText always writes a part, and an item parsed from a harness
// either has content or has none — so the divergence is unreachable rather
// than handled. Giving OutputItem a record of its own is the fix if it ever
// stops being unreachable.

func encodeTask(t *domain.Task) (string, error) {
	rec := taskRecord{
		ID:                 t.ID,
		Object:             t.Object,
		Status:             t.Status,
		Model:              t.Model,
		Output:             t.Output,
		Usage:              t.Usage,
		Error:              t.Error,
		IncompleteDetails:  t.IncompleteDetails,
		PreviousResponseID: t.PreviousResponseID,
		Store:              t.Store,
		Metadata:           t.Metadata,
		CreatedAt:          t.CreatedAt,
		UpdatedAt:          t.UpdatedAt,
		HarnessID:          t.HarnessID,
		SessionID:          t.SessionID,
		Input:              t.Input,
		InputItems:         t.InputItems,
		RequestedModel:     t.RequestedModel,
		NativeSessionID:    t.NativeSessionID,
		TimeoutSeconds:     t.TimeoutSeconds,
		MaxStep:            t.MaxStep,
		IgnoredFields:      t.IgnoredFields,
	}
	if t.Artifacts != nil {
		rec.Artifacts = make([]artifactRecord, len(t.Artifacts))
		for i, a := range t.Artifacts {
			rec.Artifacts[i] = artifactRecord{
				ID: a.ID, Object: a.Object, ContainerID: a.ContainerID,
				Filename: a.Filename, MimeType: a.MimeType,
				Bytes: a.Bytes, CreatedAt: a.CreatedAt,
			}
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("store: encode task %s: %w", t.ID, err)
	}
	return string(data), nil
}

// decodeTask reads a stored document. id is only used to name the row in an
// error, and may be empty where the caller does not have one to hand.
func decodeTask(id, data string) (*domain.Task, error) {
	var rec taskRecord
	if err := json.Unmarshal([]byte(data), &rec); err != nil {
		return nil, fmt.Errorf("store: decode task %s: %w", rowName(id, rec.ID), err)
	}
	task := &domain.Task{
		Response: uhp.Response{
			ID:                 rec.ID,
			Object:             rec.Object,
			Status:             rec.Status,
			Model:              rec.Model,
			Output:             rec.Output,
			Usage:              rec.Usage,
			Error:              rec.Error,
			IncompleteDetails:  rec.IncompleteDetails,
			PreviousResponseID: rec.PreviousResponseID,
			Store:              rec.Store,
			Metadata:           rec.Metadata,
			CreatedAt:          rec.CreatedAt,
		},
		UpdatedAt:       rec.UpdatedAt,
		HarnessID:       rec.HarnessID,
		SessionID:       rec.SessionID,
		Input:           rec.Input,
		InputItems:      rec.InputItems,
		RequestedModel:  rec.RequestedModel,
		NativeSessionID: rec.NativeSessionID,
		TimeoutSeconds:  rec.TimeoutSeconds,
		MaxStep:         rec.MaxStep,
		IgnoredFields:   rec.IgnoredFields,
	}
	if rec.Artifacts != nil {
		task.Artifacts = make([]domain.Artifact, len(rec.Artifacts))
		for i, a := range rec.Artifacts {
			task.Artifacts[i] = domain.Artifact{
				File: uhp.File{
					ID: a.ID, Object: orDefault(a.Object, objectFileKind),
					ContainerID: a.ContainerID, Filename: a.Filename,
					Bytes: a.Bytes, CreatedAt: a.CreatedAt,
				},
				MimeType: a.MimeType,
			}
		}
	}
	return task, nil
}

func encodeSession(s *domain.Session) (string, error) {
	data, err := json.Marshal(sessionRecord{
		ID:              s.ID,
		Object:          s.Object,
		HarnessID:       s.HarnessID,
		Title:           s.Title,
		Status:          s.Status,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
		NativeSessionID: s.NativeSessionID,
		LastResponseID:  s.LastResponseID,
	})
	if err != nil {
		return "", fmt.Errorf("store: encode session %s: %w", s.ID, err)
	}
	return string(data), nil
}

func decodeSession(id, data string) (*domain.Session, error) {
	var rec sessionRecord
	if err := json.Unmarshal([]byte(data), &rec); err != nil {
		return nil, fmt.Errorf("store: decode session %s: %w", rowName(id, rec.ID), err)
	}
	return &domain.Session{
		Session: uhp.Session{
			ID:        rec.ID,
			Object:    orDefault(rec.Object, objectSession),
			HarnessID: rec.HarnessID,
			Title:     rec.Title,
			Status:    rec.Status,
			CreatedAt: rec.CreatedAt,
			UpdatedAt: rec.UpdatedAt,
		},
		NativeSessionID: rec.NativeSessionID,
		LastResponseID:  rec.LastResponseID,
	}, nil
}

// rowName names the row an error is about. A listing has no id to hand and the
// document it could not parse has none either, so there is a case where the
// honest answer is that we do not know which row it was.
func rowName(id, decoded string) string {
	if id != "" {
		return id
	}
	if decoded != "" {
		return decoded
	}
	return "(unknown id)"
}
