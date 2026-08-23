package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
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
	ID                 string              `json:"id"`
	Object             string              `json:"object"`
	Status             domain.TaskStatus   `json:"status"`
	Model              string              `json:"model"`
	Output             []domain.OutputItem `json:"output"`
	Usage              *domain.Usage       `json:"usage"`
	Error              *domain.TaskError   `json:"error"`
	IncompleteDetails  map[string]any      `json:"incomplete_details"`
	PreviousResponseID string              `json:"previous_response_id"`
	Store              bool                `json:"store"`
	Metadata           map[string]any      `json:"metadata"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
	HarnessID          string              `json:"harness_id"`
	SessionID          string              `json:"session_id"`
	Input              string              `json:"input"`
	RequestedModel     string              `json:"requested_model"`
	Artifacts          []artifactRecord    `json:"artifacts"`
	NativeSessionID    string              `json:"native_session_id"`
}

// artifactRecord is domain.Artifact's fields rather than its wire object,
// which renames Path to `filename`, adds a download URL derived from the ids,
// and truncates CreatedAt to the second.
type artifactRecord struct {
	ID          string    `json:"id"`
	ContainerID string    `json:"container_id"`
	Path        string    `json:"path"`
	MimeType    string    `json:"mime_type"`
	SizeBytes   int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
}

type sessionRecord struct {
	ID              string            `json:"id"`
	HarnessID       string            `json:"harness_id"`
	Title           string            `json:"title"`
	Status          domain.TaskStatus `json:"status"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	NativeSessionID string            `json:"native_session_id"`
	LastResponseID  string            `json:"last_response_id"`
}

// No field declared here carries `omitempty`, deliberately. A nil slice and an
// empty one are different values to the transport that renders them, and
// omitting the empty case would collapse the two on the way through storage —
// so the engine a deployment chose would decide what its clients see.
//
// The guarantee stops at the fields these records declare. Below them,
// domain.OutputItem's own tags apply, and `content` and `summary` are
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
		RequestedModel:     t.RequestedModel,
		NativeSessionID:    t.NativeSessionID,
	}
	if t.Artifacts != nil {
		rec.Artifacts = make([]artifactRecord, len(t.Artifacts))
		for i, a := range t.Artifacts {
			rec.Artifacts[i] = artifactRecord{
				ID: a.ID, ContainerID: a.ContainerID, Path: a.Path,
				MimeType: a.MimeType, SizeBytes: a.SizeBytes, CreatedAt: a.CreatedAt,
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
		UpdatedAt:          rec.UpdatedAt,
		HarnessID:          rec.HarnessID,
		SessionID:          rec.SessionID,
		Input:              rec.Input,
		RequestedModel:     rec.RequestedModel,
		NativeSessionID:    rec.NativeSessionID,
	}
	if rec.Artifacts != nil {
		task.Artifacts = make([]domain.Artifact, len(rec.Artifacts))
		for i, a := range rec.Artifacts {
			task.Artifacts[i] = domain.Artifact{
				ID: a.ID, ContainerID: a.ContainerID, Path: a.Path,
				MimeType: a.MimeType, SizeBytes: a.SizeBytes, CreatedAt: a.CreatedAt,
			}
		}
	}
	return task, nil
}

func encodeSession(s *domain.Session) (string, error) {
	data, err := json.Marshal(sessionRecord{
		ID:              s.ID,
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
		ID:              rec.ID,
		HarnessID:       rec.HarnessID,
		Title:           rec.Title,
		Status:          rec.Status,
		CreatedAt:       rec.CreatedAt,
		UpdatedAt:       rec.UpdatedAt,
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
