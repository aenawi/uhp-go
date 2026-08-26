package uhp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Client speaks UHP to a server.
//
// It is the protocol's client, not this repository's: every method maps to an
// endpoint in uhp-2026-08-11.openapi.yaml, nothing here knows anything about
// uhp-go, and the only dependency is the standard library. What it adds over
// hand-rolling net/http is the handful of rules a correct client has to follow
// and that are easy to get wrong or skip:
//
//   - the error envelope is decoded, so a failure comes back as an [*Error]
//     with a code you can switch on rather than a status and a blob;
//   - UHP-Version is sent and the server's answer is checked against it;
//   - Idempotency-Key is required on retries of a task, and [Client.Do] will
//     not retry one without it;
//   - retries follow Errors §4 — which classes are worth retrying, and
//     Retry-After when the server sends one;
//   - a stream is read incrementally and resumed from Last-Event-ID, rather
//     than buffered and restarted.
//
// The zero value is not usable; call [NewClient].
//
// A Client is safe for concurrent use. Its methods do not retain the context
// beyond the call, except [Client.Stream], which reads until its stream ends or
// its context is cancelled.
type Client struct {
	// BaseURL is the server's origin, with or without a trailing slash.
	// Endpoint paths are joined onto it, so a prefix ("https://host/uhp") works.
	BaseURL string

	// APIKey is sent as `Authorization: Bearer`. Empty sends no header, which
	// is correct only for GET /v1/uhp and for a server running without
	// authentication.
	APIKey string

	// HTTPClient is the transport. Nil means a client with no timeout, which
	// is the right default here and an unusual one: agent tasks routinely run
	// for minutes, and Errors §5 tells clients to bound a stream on inactivity
	// rather than on total duration. Set a Timeout only if you know your tasks
	// are short.
	HTTPClient *http.Client

	// Version is the protocol version to ask for, sent as `UHP-Version`. It
	// defaults to [Version] — the one these types describe — because asking for
	// a version whose shapes you do not have is how a client decodes garbage.
	Version string

	// UserAgent is sent unchanged. Empty sends Go's default.
	UserAgent string

	// MaxRetries bounds how many times a retryable failure is retried. Zero
	// means [DefaultMaxRetries]; a negative value disables retrying.
	//
	// Only failures Errors §4 calls retryable are retried, and a task creation
	// is retried only when it carries an Idempotency-Key. See [Client.Do].
	MaxRetries int

	// RetryBase is the first backoff interval, doubled per attempt. Zero means
	// [DefaultRetryBase]. A server's Retry-After header overrides it.
	RetryBase time.Duration
}

// Defaults for [Client.MaxRetries] and [Client.RetryBase].
//
// Three attempts and a 500ms base put the last retry a little over two seconds
// out, which is short enough not to hide a broken server behind a slow client
// and long enough to ride out a restart.
const (
	DefaultMaxRetries = 3
	DefaultRetryBase  = 500 * time.Millisecond
)

// NewClient returns a client for a server, with the defaults described on
// [Client]. The fields are exported, so anything else is set afterwards.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{BaseURL: baseURL, APIKey: apiKey}
}

// VersionMismatchError is a server that answered in a version other than the
// one asked for.
//
// Lifecycle §1 forbids this — "It MUST NOT silently serve a different version"
// — so it is reported rather than tolerated. A client that ignored it would be
// decoding one version's shapes out of another's bytes, which is precisely the
// failure the version header exists to prevent.
type VersionMismatchError struct {
	Requested string
	Served    string
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("uhp: asked for protocol version %s, server answered %s",
		e.Requested, e.Served)
}

// Discover fetches GET /v1/uhp.
//
// It is the one endpoint that needs no credential, and the one to call first: a
// client learns which versions a server speaks and which capabilities it
// implements before it commits to anything. A capability reported false is not
// supported — an absent key means the same thing (Lifecycle §2).
func (c *Client) Discover(ctx context.Context) (*Discovery, error) {
	var out Discovery
	return &out, c.get(ctx, "/v1/uhp", nil, &out)
}

// ListHarnesses fetches GET /v1/harnesses. The list may legitimately be empty.
//
// It decodes the protocol's harness object and nothing else. A server that
// extends it — and every object in the schema is `additionalProperties: true`,
// so any server may — sends fields this type has no home for, and a decoder
// drops what it cannot land. [Client.ListHarnessesInto] is how a client that
// knows a particular server keeps them.
func (c *Client) ListHarnesses(ctx context.Context) ([]Harness, error) {
	var out []Harness
	if err := c.ListHarnessesInto(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetHarness fetches GET /v1/harnesses/{id}. It decodes the protocol's object;
// see [Client.ListHarnesses] for what that leaves out and
// [Client.GetHarnessInto] for the way to keep it.
func (c *Client) GetHarness(ctx context.Context, id string) (*Harness, error) {
	var out Harness
	return &out, c.GetHarnessInto(ctx, id, &out)
}

// GetHarnessInto fetches GET /v1/harnesses/{id} and decodes the harness into
// out, whatever out is — in particular a type that embeds [Harness] and adds
// the fields a specific server extends it with.
//
// # Why this is in the protocol's client
//
// Because the extensibility is the protocol's. Every object in
// uhp-2026-08-11.schema.json is `additionalProperties: true`, so a server MAY
// send fields UHP does not define, and a client that wants them needs somewhere
// to put them. This method knows nothing about which fields those are or which
// server sends them: it names the endpoint, and the caller names the shape.
//
//	var h uhpgo.Harness // uhp.Harness plus this server's three additions
//	err := c.GetHarnessInto(ctx, id, &h)
//
// What the caller gives up is portability of the extra fields, not of the call:
// against a server that sends none of them, the additions decode as their zero
// values and the protocol half is exactly what [Client.GetHarness] returns.
func (c *Client) GetHarnessInto(ctx context.Context, id string, out any) error {
	return c.get(ctx, "/v1/harnesses/"+url.PathEscape(id), nil, out)
}

// ListHarnessesInto fetches GET /v1/harnesses and decodes the `harnesses` array
// into out, which must be a pointer to a slice. See [Client.GetHarnessInto] for
// why this exists.
//
// The array is decoded in a second pass rather than by declaring out as the
// envelope's field, so that the type a caller hands in is the element type
// rather than something wrapping it — the same shape they pass to
// [Client.GetHarnessInto], for what is the same object.
func (c *Client) ListHarnessesInto(ctx context.Context, out any) error {
	var envelope struct {
		Harnesses json.RawMessage `json:"harnesses"`
	}
	if err := c.get(ctx, "/v1/harnesses", nil, &envelope); err != nil {
		return err
	}
	// An absent key and a null array are both "this server listed nothing",
	// which leaves out as the caller passed it rather than being an error.
	if len(envelope.Harnesses) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Harnesses, out); err != nil {
		return fmt.Errorf("uhp: decode response: %w", err)
	}
	return nil
}

// CreateHarness posts POST /v1/harnesses (conformance class full).
//
// Check `harness_management` in the discovery document first; a server that
// does not implement it answers an error rather than creating anything.
func (c *Client) CreateHarness(ctx context.Context, spec HarnessCreate) (*Harness, error) {
	var out Harness
	return &out, c.send(ctx, http.MethodPost, "/v1/harnesses", spec, &out)
}

// UpdateHarness puts PUT /v1/harnesses/{id}, replacing the mutable
// configuration. `id`, `base` and `createdAt` are immutable.
//
// The whole configuration is replaced, so a partial body clears what it omits —
// in particular, sending a harness back without its skills empties the folder.
// Read it with [Client.GetHarness] and send back what you were given, changed.
func (c *Client) UpdateHarness(ctx context.Context, id string, spec HarnessCreate) (*Harness, error) {
	var out Harness
	return &out, c.send(ctx, http.MethodPut, "/v1/harnesses/"+url.PathEscape(id), spec, &out)
}

// DeleteHarness deletes DELETE /v1/harnesses/{id}. The sessions and responses
// that ran on it are kept.
func (c *Client) DeleteHarness(ctx context.Context, id string) error {
	return c.send(ctx, http.MethodDelete, "/v1/harnesses/"+url.PathEscape(id), nil, nil)
}

// Models fetches GET /v1/models: the catalogue by backend.
func (c *Client) Models(ctx context.Context) (*ModelCatalog, error) {
	var out ModelCatalog
	return &out, c.get(ctx, "/v1/models", nil, &out)
}

// HarnessModels fetches GET /v1/harnesses/{id}/models.
//
// `available` is computed by the server for right now, so it is worth reading
// rather than caching: a model listed as available and then failing the task is
// the outcome the field exists to prevent.
func (c *Client) HarnessModels(ctx context.Context, id string) (*HarnessModels, error) {
	var out HarnessModels
	return &out, c.get(ctx, "/v1/harnesses/"+url.PathEscape(id)+"/models", nil, &out)
}

// Create posts POST /v1/responses and waits for the finished Response.
//
// The request's `stream` field is ignored — this is the non-streaming path, and
// [Client.Stream] is the other one. Both produce the same output array, so
// which you use is a question of whether you want to render progress.
//
// idempotencyKey may be empty, and should not be. Errors §4: "Retries of
// POST /v1/responses MUST carry an Idempotency-Key" — and this client will not
// retry a task creation that has no key, because a retry without one can run
// expensive, side-effecting work twice.
func (c *Client) Create(
	ctx context.Context, req CreateResponseRequest, idempotencyKey string,
) (*Response, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("uhp: encode request: %w", err)
	}
	r, err := c.request(ctx, http.MethodPost, "/v1/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		r.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.do(r, body, idempotencyKey != "")
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)

	var out Response
	return &out, decodeJSON(resp.Body, &out)
}

// Get fetches GET /v1/responses/{id}.
//
// This is the authoritative record of a task. Streams are an optimisation: a
// client that lost one, or never opened one, reads the answer here.
func (c *Client) Get(ctx context.Context, id string) (*Response, error) {
	var out Response
	return &out, c.get(ctx, "/v1/responses/"+url.PathEscape(id), nil, &out)
}

// InputItems fetches GET /v1/responses/{id}/input_items: the input the task was
// created with, for rebuilding a transcript you did not store.
//
// The items are returned as raw JSON because the schema declines to type them
// (`additionalProperties: true`), and decoding them into a shape this package
// invented would quietly drop whatever it had no field for.
func (c *Client) InputItems(ctx context.Context, id string) ([]json.RawMessage, error) {
	var out struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := c.get(ctx, "/v1/responses/"+url.PathEscape(id)+"/input_items", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Cancel posts POST /v1/responses/{id}/cancel.
//
// Cancellation is asynchronous and idempotent: this returns when the server has
// accepted the request, which is not the same as the harness having stopped,
// and cancelling an already-terminal task succeeds and changes nothing. A
// cancelled task reports `cancelled`, never `failed`.
func (c *Client) Cancel(ctx context.Context, id string) (*Response, error) {
	var out Response
	return &out, c.send(ctx, http.MethodPost,
		"/v1/responses/"+url.PathEscape(id)+"/cancel", nil, &out)
}

// Delete deletes DELETE /v1/responses/{id}: the stored response, not the work.
//
// A running task keeps running. Tasks §4 requires that — deleting history and
// stopping work are different intentions — so a client that wants both must
// call [Client.Cancel] as well, and in that order if it wants the output kept.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.send(ctx, http.MethodDelete, "/v1/responses/"+url.PathEscape(id), nil, nil)
}

// SessionFilter narrows [Client.ListSessions]. The zero value lists the first
// page of everything.
type SessionFilter struct {
	Limit   int
	Cursor  string
	Harness string
}

// ListSessions fetches GET /v1/sessions (conformance class extended).
//
// Page with `next_cursor` and stop when it is null. Do not stop on a short
// page: that heuristic is wrong whenever a page is exactly full, which is why
// the field is an explicit null rather than an omission.
func (c *Client) ListSessions(ctx context.Context, f SessionFilter) (*SessionList, error) {
	q := url.Values{}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Cursor != "" {
		q.Set("cursor", f.Cursor)
	}
	if f.Harness != "" {
		q.Set("harness", f.Harness)
	}
	var out SessionList
	return &out, c.get(ctx, "/v1/sessions", q, &out)
}

// GetSession fetches GET /v1/sessions/{id}.
func (c *Client) GetSession(ctx context.Context, id string) (*Session, error) {
	var out Session
	return &out, c.get(ctx, "/v1/sessions/"+url.PathEscape(id), nil, &out)
}

// SessionTurns fetches GET /v1/sessions/{id}/turns, oldest first.
//
// The items come back raw for the reason [Client.InputItems] gives: the OpenAPI
// types a turn as an untyped object, so the six fields [Turn] names are one
// implementation's reading and not a guarantee. Decode into [Turn] if you have
// checked that your server fills it, and tolerate absences either way.
func (c *Client) SessionTurns(ctx context.Context, id string) ([]json.RawMessage, error) {
	var out struct {
		Turns []json.RawMessage `json:"turns"`
	}
	if err := c.get(ctx, "/v1/sessions/"+url.PathEscape(id)+"/turns", nil, &out); err != nil {
		return nil, err
	}
	return out.Turns, nil
}

// CancelSession posts POST /v1/sessions/{id}/cancel: stop whatever is running,
// without deleting the session. The conversation stays continuable.
func (c *Client) CancelSession(ctx context.Context, id string) error {
	return c.send(ctx, http.MethodPost,
		"/v1/sessions/"+url.PathEscape(id)+"/cancel", nil, nil)
}

// DeleteSession deletes DELETE /v1/traces/{id} (conformance class full): the
// whole conversation, its turns and the files it produced.
//
// The path is the specification's, and it is the same id every other session
// method here takes — `traces` and `sessions` name one resource in UHP, not two.
//
// Unlike [Client.Delete], this one *does* stop the work: Sessions §6 couples
// cancellation to it, because deleting the trace disposes of the conversation
// the run belongs to. Cancellation is asynchronous, so a server may answer this
// before the harness has actually wound down.
func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.send(ctx, http.MethodDelete, "/v1/traces/"+url.PathEscape(id), nil, nil)
}

// SessionFiles fetches GET /v1/sessions/{id}/files: every artifact of the
// session, including ones produced by earlier tasks.
func (c *Client) SessionFiles(ctx context.Context, id string) ([]File, error) {
	var out struct {
		Files []File `json:"files"`
	}
	if err := c.get(ctx, "/v1/sessions/"+url.PathEscape(id)+"/files", nil, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// SessionArchive fetches GET /v1/sessions/{id}/files/archive as a zip.
//
// The caller closes the reader. A session with forty artifacts is forty
// requests otherwise, which is the whole reason the endpoint exists.
func (c *Client) SessionArchive(ctx context.Context, id string) (io.ReadCloser, error) {
	return c.openStream(ctx, http.MethodGet,
		"/v1/sessions/"+url.PathEscape(id)+"/files/archive", nil, nil)
}

// ShareSession posts POST /v1/sessions/{id}/share (conformance class full): a
// read-only view of the conversation, published under an unguessable id.
//
// Check `session_sharing` in the discovery document first. The returned
// [Share].ID is a bearer capability — anyone holding it reads the conversation,
// its turns and its files with no credential at all — so treat it the way you
// treat an API key: out of logs, out of bug reports, and revoked with
// [Client.RevokeShare] the moment it should stop working.
//
// It is idempotent per session. A second call returns the share that already
// exists rather than a second one, so this is not the way to rotate a link:
// revoke first, then share again.
func (c *Client) ShareSession(ctx context.Context, id string) (*Share, error) {
	var out Share
	return &out, c.send(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(id)+"/share", nil, &out)
}

// SessionShare fetches GET /v1/sessions/{id}/share: the share this session has.
//
// A session that has never been shared answers 404. That is not the same
// failure as a session that does not exist, and both arrive as an [*Error] —
// read `Code` to tell them apart.
func (c *Client) SessionShare(ctx context.Context, id string) (*Share, error) {
	var out Share
	return &out, c.get(ctx, "/v1/sessions/"+url.PathEscape(id)+"/share", nil, &out)
}

// RevokeShare deletes DELETE /v1/sessions/{id}/share: the link stops working,
// and the id of the link that was withdrawn comes back.
//
// Read the returned id rather than assuming it is the one you last minted. A
// revoke that crosses a re-share withdraws whichever share the session had when
// the delete ran, and the server reports that one.
//
// Sessions §5 requires revocation and names no endpoint for it, so this path is
// one server's reading rather than protocol. Revoking a session that has no
// share answers 404 rather than succeeding, so a client cannot use this to
// assert "make sure this is not shared" without handling that.
func (c *Client) RevokeShare(ctx context.Context, id string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.send(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(id)+"/share", nil, &out)
	return out.ID, err
}

// SharedSession fetches a shared view by its share id, with no credential.
//
// # None of this is normative
//
// Sessions §5 requires that a shared view exist and be read-only, and says
// nothing about where it is served or what it contains. This method — and the
// three below it — speak the paths *this project's server* uses, and a
// different conformant server may not have them at all. Everything above this
// point in the file is the specification's; this block is not.
//
// The response envelope carries the harness that ran the session as well, which
// this returns nothing of: the harness object a server sends is the protocol's
// thirteen fields plus whatever it adds, and surfacing it here would mean this
// package declaring a shape it does not own. Read the endpoint directly if you
// want it.
//
// Set [Client.APIKey] to the empty string for these. A share is the credential.
func (c *Client) SharedSession(ctx context.Context, shareID string) (*Session, error) {
	var out struct {
		Session Session `json:"session"`
	}
	if err := c.get(ctx, "/v1/shares/"+url.PathEscape(shareID), nil, &out); err != nil {
		return nil, err
	}
	return &out.Session, nil
}

// SharedTurns fetches a shared session's turns, oldest first. The items come
// back raw for the reason [Client.SessionTurns] gives.
func (c *Client) SharedTurns(ctx context.Context, shareID string) ([]json.RawMessage, error) {
	var out struct {
		Turns []json.RawMessage `json:"turns"`
	}
	if err := c.get(ctx, "/v1/shares/"+url.PathEscape(shareID)+"/turns", nil, &out); err != nil {
		return nil, err
	}
	return out.Turns, nil
}

// SharedFiles fetches a shared session's artifacts.
//
// Each entry's `download_url` points at the authenticated container path, which
// a viewer holding only a share cannot use. Fetch the bytes with
// [Client.DownloadShared] instead.
func (c *Client) SharedFiles(ctx context.Context, shareID string) ([]File, error) {
	var out struct {
		Files []File `json:"files"`
	}
	if err := c.get(ctx, "/v1/shares/"+url.PathEscape(shareID)+"/files", nil, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// DownloadShared fetches one artifact's bytes through a share. The caller
// closes the reader.
//
// There is no container id, and that is the scoping: the server derives it from
// the share, so a file id belonging to another session does not resolve here.
// The bytes carry the same warning [Client.Download] gives — they are whatever
// an agent wrote — and rather more of it, since this path is reachable by
// anyone holding the link.
func (c *Client) DownloadShared(ctx context.Context, shareID, fileID string) (io.ReadCloser, error) {
	return c.openStream(ctx, http.MethodGet, "/v1/shares/"+
		url.PathEscape(shareID)+"/files/"+url.PathEscape(fileID)+"/content", nil, nil)
}

// Upload posts POST /v1/files: a file to reference later by `file_id`.
//
// The alternative is a data: URL inline in the request, which a server must
// also accept — but inline means re-sending the bytes on every retry, so
// anything large belongs here. A file over the server's limit is refused with
// `file_too_large` and never silently truncated.
func (c *Client) Upload(ctx context.Context, filename string, content io.Reader) (*File, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("uhp: build upload: %w", err)
	}
	if _, err := io.Copy(part, content); err != nil {
		return nil, fmt.Errorf("uhp: read upload: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("uhp: build upload: %w", err)
	}

	r, err := c.request(ctx, http.MethodPost, "/v1/files", bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", mw.FormDataContentType())

	// Not retried: the body is a fresh upload with no idempotency key, and a
	// retry could leave two copies behind for the caller to choose between.
	resp, err := c.do(r, nil, false)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)

	var out File
	return &out, decodeJSON(resp.Body, &out)
}

// Download fetches an artifact's bytes from
// GET /v1/containers/{cid}/files/{fid}/content. The caller closes the reader.
//
// The bytes are whatever an agent wrote, which is to say attacker-influenceable
// content. A conformant server sends `X-Content-Type-Options: nosniff` with
// them; if you are putting them in a browser, keep them off your own origin.
func (c *Client) Download(ctx context.Context, containerID, fileID string) (io.ReadCloser, error) {
	return c.openStream(ctx, http.MethodGet, "/v1/containers/"+
		url.PathEscape(containerID)+"/files/"+url.PathEscape(fileID)+"/content", nil, nil)
}

// get issues a GET and decodes a JSON body into out.
func (c *Client) get(ctx context.Context, endpoint string, query url.Values, out any) error {
	r, err := c.request(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if len(query) > 0 {
		r.URL.RawQuery = query.Encode()
	}
	resp, err := c.do(r, nil, true)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if out == nil {
		return nil
	}
	return decodeJSON(resp.Body, out)
}

// send issues a request with an optional JSON body and an optional JSON reply.
//
// Nothing here is retried. Every caller is a write — create, update, delete,
// cancel — and none of them carries an idempotency key, so a retry is a second
// attempt at something that may already have happened. Cancel is genuinely
// idempotent and could be retried; it is not, so that this rule has no
// exceptions to remember.
func (c *Client) send(ctx context.Context, method, endpoint string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("uhp: encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	r, err := c.request(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.do(r, nil, false)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if out == nil {
		return nil
	}
	// A 204 is a legitimate answer to a delete on some servers even though the
	// OpenAPI specifies a body, and decoding one produces "unexpected EOF" for
	// a request that succeeded.
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return decodeJSON(resp.Body, out)
}

// openStream issues a request and hands back the undecoded body.
//
// The caller closes it. Nothing is retried and nothing is buffered: these are
// artifact downloads and event streams, and buffering either defeats the point.
func (c *Client) openStream(
	ctx context.Context, method, endpoint string, header http.Header, body io.Reader,
) (io.ReadCloser, error) {
	r, err := c.request(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	resp, err := c.do(r, nil, false)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// request builds a request with this client's headers set.
func (c *Client) request(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	full, err := c.resolve(endpoint)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, fmt.Errorf("uhp: build request: %w", err)
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("UHP-Version", c.version())
	if c.APIKey != "" {
		r.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.UserAgent != "" {
		r.Header.Set("User-Agent", c.UserAgent)
	}
	return r, nil
}

// resolve joins an endpoint path onto BaseURL, preserving any path prefix the
// base carries — a server behind "https://host/uhp" is a normal deployment and
// dropping its prefix would send every request to the wrong place.
func (c *Client) resolve(endpoint string) (string, error) {
	base, err := url.Parse(strings.TrimSuffix(c.BaseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("uhp: invalid base url %q: %w", c.BaseURL, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("uhp: base url %q needs a scheme and a host", c.BaseURL)
	}
	joined := *base
	joined.Path = path.Join(base.Path, endpoint)
	return joined.String(), nil
}

func (c *Client) version() string {
	if c.Version != "" {
		return c.Version
	}
	return Version
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// do sends a request, retries what Errors §4 says is worth retrying, and turns
// a non-2xx into an [*Error].
//
// `body` is the request body to replay on a retry, or nil for a request that
// carries none. `retryable` is the caller's own judgement about whether this
// request may be sent twice at all — a task creation says yes only when it
// carries an Idempotency-Key, because without one a retry after a timeout runs
// expensive, side-effecting work a second time while the first may still be
// going.
func (c *Client) do(r *http.Request, body []byte, retryable bool) (*http.Response, error) {
	attempts := c.MaxRetries
	if attempts == 0 {
		attempts = DefaultMaxRetries
	}
	if attempts < 0 || !retryable {
		attempts = 0
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			// Rewound rather than reused: a *http.Request's Body is consumed by
			// the first send, and replaying one without resetting it sends an
			// empty body that the server reports as invalid input.
			if body != nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
			}
		}

		resp, err := c.httpClient().Do(r)
		if err != nil {
			// A cancelled context is the caller's decision, not a failure to
			// ride out. Retrying it would spin until the deadline.
			if r.Context().Err() != nil {
				return nil, err
			}
			lastErr = fmt.Errorf("uhp: %s %s: %w", r.Method, r.URL.Path, err)
			if attempt >= attempts {
				return nil, lastErr
			}
			if waitErr := sleep(r.Context(), c.backoff(attempt, 0)); waitErr != nil {
				return nil, waitErr
			}
			continue
		}

		if resp.StatusCode < 300 {
			if err := c.checkVersion(resp); err != nil {
				drainClose(resp.Body)
				return nil, err
			}
			return resp, nil
		}

		apiErr := decodeError(resp)
		drainClose(resp.Body)
		lastErr = apiErr
		if attempt >= attempts || !retryableStatus(resp.StatusCode, apiErr) {
			return nil, lastErr
		}
		if waitErr := sleep(r.Context(), c.backoff(attempt, retryAfter(resp))); waitErr != nil {
			return nil, waitErr
		}
	}
}

// checkVersion enforces Lifecycle §1's "MUST NOT silently serve a different
// version".
//
// A server that sends no header at all is left alone rather than refused. The
// header is a MUST on the server, but a proxy that strips unknown headers is a
// real deployment and failing every request over one would be worse than
// proceeding — the shapes are checked by decoding either way.
func (c *Client) checkVersion(resp *http.Response) error {
	served := resp.Header.Get("UHP-Version")
	if served == "" || served == c.version() {
		return nil
	}
	return &VersionMismatchError{Requested: c.version(), Served: served}
}

// backoff is exponential from RetryBase, or the server's own Retry-After when
// it sent one — a server that says when to come back knows better than a
// client's guess.
func (c *Client) backoff(attempt int, serverSaid time.Duration) time.Duration {
	if serverSaid > 0 {
		return serverSaid
	}
	base := c.RetryBase
	if base <= 0 {
		base = DefaultRetryBase
	}
	return base << attempt
}

// retryableStatus reports whether Errors §4 calls this failure worth retrying.
//
// The class is the signal, not the code, which is the rule the chapter states
// and the reason an unrecognised code is safe: a 503 is retryable whether or
// not this package has heard of what the server called it.
//
// The one code-level exception is `quota_exhausted`, which arrives as a 429 —
// a status this would otherwise retry — and means the opposite of "come back
// shortly": the budget is spent, and retrying unchanged will fail identically.
func retryableStatus(status int, err *Error) bool {
	if err != nil && err.Code == CodeQuotaExhausted {
		return false
	}
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		// Everything else is the request being wrong — a 4xx that will fail
		// identically forever, including 409 session_busy, which is retryable
		// in principle but only once the in-flight task is done and so is the
		// caller's decision rather than this loop's.
		return false
	}
}

// retryAfter reads the Retry-After header. Only the delta-seconds form is
// honoured; the HTTP-date form is rare here and a misparse would be worse than
// falling back to the client's own backoff.
func retryAfter(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// sleep waits, or returns early if the context ends first.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// decodeError turns a non-2xx into an [*Error].
//
// A body that is not the envelope still produces one, built from the status.
// Errors §1 requires every non-2xx to carry the envelope, but a client meets
// proxies and load balancers that have never read it, and a caller switching on
// `err.Type` should not have to handle "and sometimes it is a different error
// entirely" as a separate case.
func decodeError(resp *http.Response) *Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var envelope ErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != "" {
		out := envelope.Error
		if out.Type == "" {
			out.Type = typeForStatus(resp.StatusCode)
		}
		return &out
	}

	message := strings.TrimSpace(string(body))
	if message == "" || len(message) > 200 {
		message = http.StatusText(resp.StatusCode)
	}
	return &Error{
		Type:    typeForStatus(resp.StatusCode),
		Code:    strconv.Itoa(resp.StatusCode),
		Message: message,
	}
}

// typeForStatus maps a status onto the error type Errors §2 pairs it with, for
// a response that did not name one itself.
func typeForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return ErrorTypeAuthentication
	case status == http.StatusForbidden:
		return ErrorTypePermission
	case status == http.StatusTooManyRequests:
		return ErrorTypeRateLimit
	case status >= 500:
		return ErrorTypeServerError
	default:
		return ErrorTypeInvalidRequest
	}
}

func decodeJSON(r io.Reader, out any) error {
	if err := json.NewDecoder(r).Decode(out); err != nil {
		return fmt.Errorf("uhp: decode response: %w", err)
	}
	return nil
}

// drainClose reads the rest of a body before closing it, so the connection can
// go back to the pool instead of being torn down.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<16))
	_ = body.Close()
}

// AsError reports whether err is a UHP [*Error] and returns it.
//
//	if e, ok := uhp.AsError(err); ok && e.Code == uhp.CodeSessionBusy { … }
//
// A client should switch on Code where it recognises one and fall back to Type
// where it does not: servers may define their own vendor-prefixed codes, and
// treating an unrecognised code as its type is UHP's fourth client rule.
func AsError(err error) (*Error, bool) {
	var out *Error
	if errors.As(err, &out) {
		return out, true
	}
	return nil, false
}
