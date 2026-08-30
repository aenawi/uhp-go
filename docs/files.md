# Files in and out

A harness that can only return text is a chatbot. This is the other half: files as task input, and the files a run leaves behind.

A harness that can only return text is a chatbot. `uhpd` implements the UHP "Files"
chapter: files in as task input, files out as session artifacts.

**Set `UHP_WORKSPACE`.** Both halves need a per-session working directory: without one
there is nowhere to put a client's file, and nothing to diff for artifacts. Discovery
reports `files_input`/`files_output` as `false` when it is unset, no harness advertises
`files_in` or `files_out`, and a task carrying a file is refused with `501` rather than
having its attachment silently dropped.

Both halves are the router's, not the CLI's: every harness gets the same treatment, and no
harness declares either capability. See
[Capabilities are enforced, not decorative](api.md#capabilities-are-enforced-not-decorative).

### In

`input` accepts a bare string or an array of items. A file is inlined as a data URL, or
uploaded once and referenced by id:

```bash
# Inline
curl -s http://localhost:8080/v1/responses -H "Authorization: Bearer devkey" \
  -H "Content-Type: application/json" -d '{
    "input": [{"role":"user","content":[
      {"type":"input_text","text":"Summarise this."},
      {"type":"input_file","filename":"q3.txt","file_data":"data:text/plain;base64,cTMK"}]}],
    "metadata": {"harness_id":"codex"}}'

# Upload once, reference by id
curl -s -F file=@q3.pdf http://localhost:8080/v1/files -H "Authorization: Bearer devkey"
# → {"id":"file_…"} → {"type":"input_file","file_id":"file_…"}
```

A file must arrive as a data URL or as an uploaded `file_id`: a bare base64 string is
refused, because ordinary text is often valid base64 and decoding it would hand the
harness a different file than the client sent. An item whose `role` is anything but
`user` is refused too — everything in `input` becomes one prompt, so an `assistant` item
would silently become user text; prior conversation belongs in `previous_response_id`.

Attachments are written into the session's working directory under a sanitised name, and
the prompt is appended with a line naming them — no CLI harness has a generic "attach this
file" flag, and a file the model is never told about is a file it will not read. Remote
`image_url`s are refused rather than fetched: this server opens no outbound connections of
its own.

### Out

There is no harness that reports the files it wrote, so artifacts are captured by diffing
the session's working directory across a run: anything new or modified is an artifact of
that session's container. Files the router itself wrote as task input are fingerprinted
first and never come back as output, symlinks are never followed or captured, and
dot-directories (`.git`) are skipped. Capture is bounded at 200 files per task, and a
truncated capture is logged rather than silently trimmed.

Artifacts are reported twice, as the specification requires: as
`container_file_citation` annotations on the assistant message, and by
`GET /v1/sessions/{id}/files` — which lists every artifact of the session, including
earlier tasks'.

### Download safety

Artifact ids are opaque digests, not paths, so resolving one is a lookup in records this
server wrote rather than a path join of client input; the resolved path is then checked to
be inside its container anyway. Downloads are served as raw bytes with
`X-Content-Type-Options: nosniff` and a `Content-Disposition` filename: an artifact is
content an agent can be persuaded to write, and serving it without `nosniff` turns it into
stored XSS against the client's own origin. A path containing a `.` or `..` segment is
refused before routing rather than redirected to a cleaned one.

Artifacts are reachable only through their session's records, so an artifact of a session
this server no longer has is a 404 — which is what the specification asks for when a
session is deleted, and `DELETE /v1/traces/{id}` is the endpoint that does it. Access is scoped to the server's
single principal: every configured `UHP_API_KEYS` value is equivalent and carries no
identity, so a deployment serving several tenants runs one `uhpd` per tenant rather than
one server that filters — see
[`UHP_API_KEYS` is a list of credentials, not a list of tenants](authentication.md#uhp_api_keys-is-a-list-of-credentials-not-a-list-of-tenants).
