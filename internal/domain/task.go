// Package domain contains the core entities a run is made of: Task, Session
// and Artifact.
//
// The wire objects they are reported as live in
// [github.com/aenawi/uhp-go/uhp] and are embedded here rather than restated.
// Where this package keeps a word of its own it is because the internal concept
// is genuinely a different thing — a Task is the unit of work and a Response is
// what goes on the wire; an Artifact is a file a run produced and a File is any
// file. Both halves of each pair are recorded in CONTEXT.md.
package domain

import (
	"encoding/json"
	"time"

	"github.com/aenawi/uhp-go/uhp"
)

// Task is the internal unit of work: an input, the harness that runs it, and
// the run's bookkeeping — carrying the [uhp.Response] it will be reported as.
//
// # Marshalling is the embedded type's, deliberately
//
// There is no MarshalJSON here. [uhp.Response] has one and Go promotes it, so
// a Task marshals as exactly the response object a client would produce from
// the published type. Overriding it would mean [uhp.Response] marshalled by a
// client produced different JSON than this server emits, and a published type
// that does not round-trip to the wire format defeats the reason for publishing
// it.
//
// That promotion is also the hazard. A promoted marshaller renders only the
// fields it can see — the embedded ones — so anything the wire needs that lives
// on the outer struct is dropped silently, and the compiler cannot object
// because the code is correct Go. The three fields below that used to be
// computed at marshal time are exactly that case, which is why they are
// projected by [Task.SyncMetadata] instead. See
// docs/adr/0003-internal-types-embed-the-wire-types.md.
type Task struct {
	uhp.Response

	// SessionID, HarnessID and RequestedModel are bookkeeping that *feeds* the
	// wire rather than bookkeeping that is absent from it.
	//
	// An earlier comment here called all three "not part of the wire object",
	// and that was wrong in a way that mattered: they reach a client inside
	// `metadata`, and metadata.session_id is a MUST (Tasks §3). The schema has
	// nowhere else to put them — Response has twelve properties and none of
	// them is session_id — so they are contents of the metadata object, not
	// fields beside it. [Task.SyncMetadata] is what puts them there.
	SessionID      string
	HarnessID      string
	RequestedModel string

	// TimeoutSeconds is the wall-clock budget this run was given, and reaches
	// a client the same way the three above do — inside `metadata`, because
	// Response has nowhere else to put it.
	//
	// It is reported because the resolved value is not necessarily the one the
	// client asked for: a request may narrow the deployment's bound and not
	// widen it, so a caller that asked for a day and was given half an hour
	// finds that out here rather than by watching a task stop early.
	//
	// Zero means no budget, which nothing in this server produces: every task
	// gets one, and service.budgetSeconds rounds a sub-second budget up to one
	// rather than truncating it away — a bound enforced and not reported is the
	// reporting half of #54 undone. The projection below therefore omits the
	// key only for a Task nothing ever ran.
	TimeoutSeconds int

	// The genuinely internal four: nothing below reaches a client on the
	// response object. InputItems reaches one on its own endpoint.
	Input string

	// InputItems is the `input` the client sent, normalised to an array and
	// otherwise untouched, so GET /v1/responses/{id}/input_items can answer
	// with what was sent rather than with this server's paraphrase of it.
	//
	// Input, beside it, is the flattened prompt the harness was given. The two
	// are deliberately both kept: a client asking what it sent is not asking
	// what the CLI received, and answering the first with the second loses
	// every file item — which is most of what the endpoint exists for.
	//
	// json.RawMessage rather than a decoded shape, because the schema declines
	// to type an input item (`additionalProperties: true`) and a decode-encode
	// round trip would reorder keys and drop the ones this server has no field
	// for. Verbatim is the only faithful answer.
	InputItems []json.RawMessage

	Artifacts       []Artifact
	NativeSessionID string

	// UpdatedAt is internal too — the response object has no such field. It is
	// a time.Time where the embedded CreatedAt is Unix seconds, because only
	// one of the two has a wire format to match.
	UpdatedAt time.Time
}

// SyncMetadata folds this task's bookkeeping into the wire `metadata` object.
//
// It must be called whenever one of its three inputs becomes known or changes.
// There are two such points and they are named in ADR-0003: task creation, and
// mid-run model resolution — where an adapter replaces the harness default with
// the model its own output names, which is the #43 fix. Miss one and a response
// goes out without a substitution it should have declared, or worse, without
// the session id the specification requires.
//
// Computing this at marshal time, which is what it replaced, needed no such
// discipline. That cost is real and is accepted for one reason: the alternative
// was overriding MarshalJSON on Task, which would make [uhp.Response] a lie
// about what this server emits. The schema test in uhp/schema_test.go is what
// makes the cost bearable — a missed call produces a response whose metadata is
// wrong, which is what marshalling each public type and validating it exists to
// catch.
//
// It is idempotent, and it never mutates the map a caller handed in: the copy
// is what lets it run twice without the client's own metadata acquiring fields
// it did not send.
func (t *Task) SyncMetadata() {
	meta := make(map[string]any, len(t.Metadata)+4)
	for k, v := range t.Metadata {
		meta[k] = v
	}

	// Tasks §3: the session id MUST be reported in metadata.session_id.
	if t.SessionID != "" {
		meta["session_id"] = t.SessionID
	}
	if t.HarnessID != "" {
		meta["harness_id"] = t.HarnessID
	}
	// Security §5 makes bounding a task this server's obligation; reporting the
	// bound is what makes it something the client can act on rather than a
	// silent truncation it has to infer.
	if t.TimeoutSeconds > 0 {
		meta["timeout_seconds"] = t.TimeoutSeconds
	}

	// Tasks §1.3: a client must always be able to answer "did the model I
	// asked for actually run?" by comparing model with requested_model.
	//
	// Deleted rather than left alone when the answer is no. This runs more than
	// once and the model it compares against can change between calls, so a
	// projection that only ever added would leave a stale `model_fallback:
	// true` behind — a claim that a substitution happened, on a task where it
	// did not. Making the result a function of the current state is what keeps
	// the second call as correct as the first.
	if t.RequestedModel != "" && t.RequestedModel != t.Model {
		meta["requested_model"] = t.RequestedModel
		meta["model_fallback"] = true
	} else {
		delete(meta, "requested_model")
		delete(meta, "model_fallback")
	}

	t.Metadata = meta
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
				t.Output[i].Content = []uhp.ContentPart{{Type: "output_text", Annotations: []uhp.Annotation{}}}
			}
			t.Output[i].Content[0].Text += delta
			return i, t.Output[i].ID
		}
	}
	item := uhp.OutputItem{
		ID:      "msg_" + t.ID,
		Type:    "message",
		Status:  "in_progress",
		Role:    "assistant",
		Content: []uhp.ContentPart{{Type: "output_text", Text: delta, Annotations: []uhp.Annotation{}}},
	}
	t.Output = append(t.Output, item)
	return len(t.Output) - 1, item.ID
}

// MessageItem returns the assistant message item, if one exists.
func (t *Task) MessageItem() (int, *uhp.OutputItem) {
	for i := range t.Output {
		if t.Output[i].Type == "message" {
			return i, &t.Output[i]
		}
	}
	return -1, nil
}
