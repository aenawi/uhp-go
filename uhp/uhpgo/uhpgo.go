// Package uhpgo is what this server adds to UHP, kept apart from what UHP
// says.
//
// Every object in uhp-2026-08-11.schema.json is additionalProperties: true, so
// each addition below is legal on the wire. Legal is not the same as
// normative. An extension declared beside a protocol field in
// [github.com/aenawi/uhp-go/uhp] would be indistinguishable from protocol in
// godoc — same package, same style of comment, no signal at all — and a client
// author would have no way to learn which fields survive a move to a different
// conformant server. Putting them in a package of their own makes that boundary
// something the compiler shows you: an import of uhpgo is a dependency on this
// implementation, and an import of uhp alone is not.
//
// The types here embed their protocol counterparts rather than restating them,
// so there is one declaration of every wire field and no second copy to drift.
//
// # What you give up by using these
//
// A client that reads [Harness].Capabilities against a different conformant
// server finds nothing there. The harness object in the schema has thirteen
// properties and capabilities is not among them; the protocol's capability list
// is the nine booleans on [github.com/aenawi/uhp-go/uhp.Discovery]. That one
// stings the most, because capability enforcement here is load-bearing — a
// previous_response_id sent to a harness without sessions is refused 422 — and
// it is exactly the kind of useful, load-bearing addition that is easiest to
// mistake for protocol.
package uhpgo

import (
	"encoding/json"

	"github.com/aenawi/uhp-go/uhp"
)

// Capability is something a harness can do, named on the harness object itself.
//
// This is an extension. The protocol's capabilities are the nine booleans of
// [uhp.Capabilities], reported once for the whole server on the discovery
// document; there is no per-harness capability list in UHP. This one exists
// because a router with several harnesses of different bases cannot answer
// "can this one continue a session?" from a server-wide flag.
//
// Advertised capabilities are enforced, not merely reported: a request that
// takes one up against a harness that does not have it is refused rather than
// accepted and quietly served something else.
type Capability string

const (
	CapStreaming    Capability = "streaming"
	CapFilesIn      Capability = "files_in"
	CapFilesOut     Capability = "files_out"
	CapSessions     Capability = "sessions"
	CapCancellation Capability = "cancellation"
	CapTools        Capability = "tools"
)

// Harness status values, reported in [Harness].Status.
const (
	HarnessReady       = "ready"
	HarnessUnavailable = "unavailable"
	HarnessDegraded    = "degraded"
)

// Harness is [uhp.Harness] plus the three fields this server adds to it.
//
// All three answer the same question — where should I send this work? — which
// the protocol object cannot answer on its own: it describes how a harness is
// configured and says nothing about what it can do or whether it is reachable
// right now.
type Harness struct {
	uhp.Harness

	// Models is the model ids this harness can run. Computed from the base on
	// every read rather than stored, because a stored answer goes stale the
	// moment a CLI is upgraded.
	Models []string `json:"models"`
	// Capabilities is what this harness can do. See [Capability].
	Capabilities []Capability `json:"capabilities"`
	// Status is one of the Harness* constants above: whether the underlying
	// CLI is reachable at all.
	Status string `json:"status"`
}

// MarshalJSON renders the protocol harness and this server's three additions
// as one object.
//
// It exists because [uhp.Harness] carries a MarshalJSON of its own and Go
// promotes it. Without this method, marshalling a uhpgo.Harness would call that
// one — which can only see the fields it declares — and Models, Capabilities
// and Status would disappear from every harness this server serves. The code
// would compile, every test that did not read those three keys would pass, and
// nothing would point at the cause. That is the hazard
// docs/adr/0003-internal-types-embed-the-wire-types.md is about, and this is
// the one place in this repository where it is real.
//
// The protocol half is delegated rather than restated, so the empty-array
// normalisation [uhp.Harness.MarshalJSON] performs happens exactly once.
func (h Harness) MarshalJSON() ([]byte, error) {
	wire, err := json.Marshal(h.Harness)
	if err != nil {
		return nil, err
	}
	ext, err := json.Marshal(struct {
		Models       []string     `json:"models"`
		Capabilities []Capability `json:"capabilities"`
		Status       string       `json:"status"`
	}{h.Models, h.Capabilities, h.Status})
	if err != nil {
		return nil, err
	}
	// Both halves are structs, so both marshal to objects: the first ends in
	// `}` and the second begins with `{`. Splicing them beats re-declaring the
	// thirteen protocol fields here, which is the copy this package exists to
	// avoid. The guard covers the one shape the splice cannot join — an empty
	// object — which [uhp.Harness] cannot currently produce and might if every
	// one of its fields gained an omitempty.
	if len(wire) <= 2 {
		return ext, nil
	}
	out := make([]byte, 0, len(wire)+len(ext))
	out = append(out, wire[:len(wire)-1]...)
	out = append(out, ',')
	return append(out, ext[1:]...), nil
}

// HasCapability reports whether the harness advertises a given capability.
//
// The list is a promise — it is on every harness object and in the discovery
// document, so a client reads it before it sends anything — which is why the
// router consults it before accepting a request that relies on it.
func (h Harness) HasCapability(c Capability) bool { return HasCapability(h.Capabilities, c) }

// HasCapability reports whether a capability list advertises c.
//
// The free function exists alongside the method because the two callers hold
// different things: the router refuses a request against a whole [Harness],
// while a harness declaration checks its own list while it is still being built
// and has no Harness yet.
func HasCapability(caps []Capability, c Capability) bool {
	for _, have := range caps {
		if have == c {
			return true
		}
	}
	return false
}

// Event is [uhp.Event] plus the two fields that say which task an event came
// from. They are set only on a harness feed.
//
// A task's own stream carries one task, so naming it on every event would be
// noise the client already has. A feed multiplexes every task on a harness, and
// without these two an event cannot be attributed to anything: a
// response.output_text.delta names an item, not a response, so two tasks
// writing at once are one interleaved text with no way to separate them again.
type Event struct {
	uhp.Event

	ResponseID string `json:"response_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
}

// Error is [uhp.Error] plus a retry hint.
//
// # This server no longer emits Retryable, and the type is here anyway
//
// The field used to be set on a failed task's error and on nothing else — not
// on the HTTP error envelope, which models the same schema object — so a client
// reading it found it present in one place and absent from the other for no
// reason the specification explains. Collapsing both renderings onto
// [uhp.Error] resolved that contradiction, and this field is what came off in
// the process.
//
// Dropping it cost nothing a client was entitled to. Errors §4 already makes
// the error *type* the retry signal: a server_error may be retried and an
// invalid_request_error may not, and the failure this field was set on carries
// type harness_error. Nothing in this repository ever read it back, and the
// conformance suite does not mention it.
//
// The type remains published so that a client holding responses stored by an
// older build still has something to unmarshal them into, and so that a server
// choosing to send the field has a declared shape for it rather than an
// invented one.
type Error struct {
	uhp.Error

	Retryable bool `json:"retryable,omitempty"`
}

// Upload is a file a client sent ahead of a task (Files §1.2), held until a
// task references it by id.
//
// It has no counterpart in the schema at all, which is the reason it is here
// rather than beside [uhp.File]: the Files chapter specifies the *endpoint*
// that accepts an upload and the file object it answers with, and this is the
// server-side record behind that answer. It embeds [uhp.File] so that what
// reaches a client is the protocol object, with MimeType as the one addition.
type Upload struct {
	uhp.File

	MimeType string `json:"mime_type,omitempty"`

	// Data is the bytes, and is never serialised: this type is a record the
	// server holds, and the endpoint answers with metadata, not content.
	Data []byte `json:"-"`
}

// SharedSession is what a share id resolves to: the session, and the harness
// that ran it, with nothing a viewer is not entitled to.
//
// It is here rather than beside [uhp.Share] because it has no counterpart in
// the specification at all. Sessions §5 requires that a shared view exist and
// be read-only; it does not say what one contains, or where it is served. This
// is one server's answer, at GET /v1/shares/{share_id}.
//
// Session is [uhp.Session] unchanged — a conversation's id, title, status and
// timestamps are the same facts whoever is reading them. Harness is not, and
// that is the interesting half: see [SharedHarness].
type SharedSession struct {
	// Object is always "session.shared".
	Object string `json:"object,omitempty"`
	// Session is the conversation, exactly as GET /v1/sessions/{id} renders it.
	Session uhp.Session `json:"session"`
	// Harness is what ran it, or nil where the harness has since been deleted:
	// a shared conversation outlives the configuration that produced it.
	Harness *SharedHarness `json:"harness,omitempty"`
}

// SharedHarness is what a link holder is told about the harness that ran a
// shared session: identity and capability, and nothing about how the operator
// configured it.
//
// # Why this is not a [Harness] with fields removed
//
// Because several of that object's fields make an affirmative claim when they
// are empty, so removing them would publish statements that are false rather
// than absent. `mcpServers: []` is "this harness has no MCP servers" —
// [uhp.Harness.MarshalJSON] says so outright — and a null `maxStep` is
// "unbounded", not "not shown". A harness fenced to twenty steps and one MCP
// server would render, through a stripped Harness, as an unfenced one with
// none. A viewer would have been told two untrue things about a system they
// cannot see.
//
// A separate type says only what it says. What is here answers a viewer's one
// real question — what ran this, on what, and could it do the things the
// transcript shows — and the fields that are missing are missing because this
// type never had them.
//
// What is deliberately absent, beyond the two above: the system prompt (the
// operator's standing instructions), skills (each carries its bundle's file
// contents inline), and the disabled-tool list (a description of how this
// deployment is fenced, useful to someone probing it and to nobody else). The
// MCP list is the sharpest of them: Harnesses §4.1 forbids returning a resolved
// credential to a client, and a shared view is the one place where "a client"
// includes someone who presented nothing.
type SharedHarness struct {
	// Object is always "session.shared.harness".
	Object string `json:"object,omitempty"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	// Base and BaseLabel name the runtime. See [uhp.Harness].Base for why the
	// protocol declines to enumerate it.
	Base      string `json:"base"`
	BaseLabel string `json:"baseLabel,omitempty"`
	// DefaultModel and Models describe what it can run. They are here because a
	// transcript shows a model id, and a viewer with no other context cannot
	// tell an unusual choice from the ordinary one.
	DefaultModel string   `json:"defaultModel,omitempty"`
	Models       []string `json:"models"`
	// Capabilities is what the harness can do — the same extension [Harness]
	// carries, and the field that makes the transcript legible: a session with
	// no artifacts on a harness without `files_out` is a different story from
	// one on a harness that had it and produced nothing.
	Capabilities []Capability `json:"capabilities"`
	// Status is whether the runtime is reachable right now. It describes the
	// harness rather than the conversation, so a viewer reading an old
	// transcript should not take it as a statement about the run.
	Status string `json:"status"`
	// CreatedAt is Unix milliseconds, following [uhp.Harness].CreatedAt rather
	// than the seconds a response carries. The two chapters disagree and this
	// package does not smooth it over.
	CreatedAt int64 `json:"createdAt"`
}

// MarshalJSON defaults Object and renders the two lists as arrays rather than
// null, for the reasons [uhp.Harness.MarshalJSON] gives — and here both claims
// are ones this type is entitled to make: a shared harness with no models is
// one this server could not enumerate models for, not one whose models were
// withheld.
func (h SharedHarness) MarshalJSON() ([]byte, error) {
	type sharedHarness SharedHarness
	out := sharedHarness(h)
	if out.Object == "" {
		out.Object = "session.shared.harness"
	}
	if out.Models == nil {
		out.Models = []string{}
	}
	if out.Capabilities == nil {
		out.Capabilities = []Capability{}
	}
	return json.Marshal(out)
}

// MarshalJSON defaults Object, for the reason [Upload.MarshalJSON] does: a
// constant every construction site has to remember is one a construction site
// will forget.
func (s SharedSession) MarshalJSON() ([]byte, error) {
	type sharedSession SharedSession
	out := sharedSession(s)
	if out.Object == "" {
		out.Object = "session.shared"
	}
	return json.Marshal(out)
}

// The vendor-prefixed error codes this server defines.
//
// Errors §3 has no entry for any of these conditions, and requires an
// additional code to be namespaced with a vendor prefix so that a future
// version of the specification cannot collide with it. `uhpgo_` is that prefix.
//
// The near miss for most of them is `invalid_input`, and it does not fit:
// §3.1 defines it as "the body could not be parsed, or a field has the wrong
// type". A skill bundle with no SKILL.md parses fine and has every field's type
// right — it is semantically wrong — and answering `invalid_input` would send a
// client looking for a type error it will never find.
//
// The two routing codes miss for their own reason: `harness_not_found` is about
// a harness this server does not have, where those two are about a path no
// route claims and a method no route accepts, on any route at all.
//
// CodePrefix is the namespace every code this server adds to the protocol's
// list carries.
//
// Errors §3 permits a server to define codes for conditions the specification
// does not cover and requires it to namespace them, so that a future version of
// UHP can add a code of its own without colliding with one of these. The prefix
// is therefore part of the code and not decoration: an unprefixed addition is a
// name this repository does not own.
//
// It is exported so that a check for "is this one of ours" reads the same
// string the constants below are built from, rather than a second copy of it.
const CodePrefix = "uhpgo_"

// A client that does not recognise one of these follows UHP's fourth client
// rule and falls back to the error's type, which is why every one of them is
// always sent with a type set.
const (
	CodeCapabilityUnsupported        = "uhpgo_capability_unsupported"
	CodeHarnessManagementUnsupported = "uhpgo_harness_management_unsupported"
	CodeHarnessNotManaged            = "uhpgo_harness_not_managed"
	CodeImmutableField               = "uhpgo_immutable_field"
	CodeInvalidSkill                 = "uhpgo_invalid_skill"
	CodeInvalidMcpServer             = "uhpgo_invalid_mcp_server"
	CodeMcpUndeliverable             = "uhpgo_mcp_undeliverable"
	CodeMethodNotAllowed             = "uhpgo_method_not_allowed"
	CodeRouteNotFound                = "uhpgo_route_not_found"
	CodeSkillNotFound                = "uhpgo_skill_not_found"
	CodeStorageFailure               = "uhpgo_storage_failure"
	CodeEventGap                     = "uhpgo_event_gap"
	CodeAdapterStartFailed           = "uhpgo_adapter_start_failed"
	CodeSessionSharingUnsupported    = "uhpgo_session_sharing_unsupported"
	CodeShareNotFound                = "uhpgo_share_not_found"
)

// MarshalJSON keeps the bytes out of the answer and reports their count
// instead, which is what the file object's `bytes` field is for.
//
// It is defined rather than left to the struct tags because Bytes is derived
// from Data: a caller that set Data and forgot Bytes would otherwise publish a
// file of size zero, and the two cannot disagree if only one of them is
// authoritative.
//
// [Harness], [Event] and [Error] need no equivalent. Each embeds a protocol
// type that carries no marshaller of its own, so struct-tag marshalling already
// flattens the protocol fields alongside the extensions — but the general shape
// is worth knowing about, because a marshaller on an embedded type *is*
// promoted, and a promoted one renders only the fields it can see. That is the
// mechanism behind the silent-drop hazard recorded in
// docs/adr/0003-internal-types-embed-the-wire-types.md.
func (u Upload) MarshalJSON() ([]byte, error) {
	type upload Upload
	out := upload(u)
	out.Bytes = int64(len(u.Data))
	if out.Object == "" {
		out.Object = "file"
	}
	return json.Marshal(out)
}
