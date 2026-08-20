package http

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"strings"

	"github.com/aenawi/uhp-go/internal/service"
)

// UHP's `input` is either a bare string or an array of items (Files §1), and a
// server MUST accept both. The string form is shorthand for one user message;
// the array form is how a client attaches a file, and rejecting it — which this
// server did, with a 400, because the field was typed as a string — makes
// `files_in` an advertised capability that no client can use.
//
// Parsing lives here, in the transport, because it is a wire-format concern:
// the service is handed a prompt and a list of attachments and never sees an
// item array.

// parsedInput is what the wire form reduces to.
type parsedInput struct {
	Text        string
	Attachments []service.Attachment
}

// badInputError is returned for anything a client can fix by sending a
// different body. The transport maps it to 400 `invalid_input`, and it carries
// the dotted path to the offending field because Errors §1 requires `param`
// whenever there is one.
type badInputError struct {
	msg   string
	param string
}

func (e badInputError) Error() string { return e.msg }

func badInput(param, format string, args ...any) error {
	return badInputError{msg: fmt.Sprintf(format, args...), param: param}
}

// parseInput accepts the string form and the item-array form.
func parseInput(raw json.RawMessage) (parsedInput, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return parsedInput{}, badInput("input", "field 'input' is required")
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return parsedInput{}, badInput("input", "field 'input' is not a valid string")
		}
		if strings.TrimSpace(s) == "" {
			return parsedInput{}, badInput("input", "field 'input' is required")
		}
		return parsedInput{Text: s}, nil
	}

	if trimmed[0] != '[' {
		return parsedInput{}, badInput("input", "field 'input' must be a string or an array of input items")
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return parsedInput{}, badInput("input", "field 'input' is not a valid array of input items")
	}

	var out parsedInput
	for i, rawItem := range items {
		if err := readItem(rawItem, fmt.Sprintf("input[%d]", i), &out); err != nil {
			return parsedInput{}, err
		}
	}
	if strings.TrimSpace(out.Text) == "" && len(out.Attachments) == 0 {
		return parsedInput{}, badInput("input", "field 'input' carries neither text nor a file")
	}
	return out, nil
}

// inputItem is a message item, or — for clients that skip the message wrapper —
// a bare content part.
type inputItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Text    string          `json:"text"`

	// input_file
	Filename string `json:"filename"`
	FileData string `json:"file_data"`
	FileID   string `json:"file_id"`

	// input_image
	ImageURL string `json:"image_url"`
}

func readItem(raw json.RawMessage, param string, out *parsedInput) error {
	var item inputItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return badInput(param, "an input item is not an object")
	}

	// A role this server cannot honour is refused rather than flattened.
	// Everything in `input` becomes one prompt for a CLI that takes one, so an
	// item labelled `assistant` or `system` would silently become user text —
	// the same "part of the prompt quietly means something else" failure this
	// parser refuses everywhere else. Prior conversation belongs in
	// previous_response_id, which is how UHP continues a session.
	switch item.Role {
	case "", "user":
	default:
		return badInput(param+".role",
			"input role %q is not supported; continue a conversation with previous_response_id", item.Role)
	}

	// A message item carries its parts in `content`, which is itself either a
	// string or an array.
	if len(item.Content) > 0 {
		return readContent(item.Content, param+".content", out)
	}
	return readPart(item, param, out)
}

func readContent(raw json.RawMessage, param string, out *parsedInput) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return badInput(param, "content is not a valid string")
		}
		out.appendText(s)
		return nil
	}
	var parts []inputItem
	if err := json.Unmarshal(raw, &parts); err != nil {
		return badInput(param, "content must be a string or an array of content parts")
	}
	for i, p := range parts {
		if err := readPart(p, fmt.Sprintf("%s[%d]", param, i), out); err != nil {
			return err
		}
	}
	return nil
}

// readPart folds one content part into the parsed input.
//
// An unrecognised part is an error rather than something to skip: silently
// dropping part of a prompt produces a confident answer to a question the
// client did not ask, which is the same failure mode the specification rejects
// for truncated uploads.
func readPart(p inputItem, param string, out *parsedInput) error {
	switch p.Type {
	case "input_text", "text", "output_text":
		out.appendText(p.Text)
		return nil

	case "input_file":
		att, err := fileAttachment(p, param)
		if err != nil {
			return err
		}
		out.Attachments = append(out.Attachments, att)
		return nil

	case "input_image":
		att, err := imageAttachment(p, param)
		if err != nil {
			return err
		}
		out.Attachments = append(out.Attachments, att)
		return nil

	case "":
		// A part with no type but with text is a client being loose with the
		// shorthand, and its meaning is unambiguous.
		if p.Text != "" {
			out.appendText(p.Text)
			return nil
		}
		return badInput(param, "an input item has no 'type'")

	default:
		return badInput(param, "input item type %q is not supported", p.Type)
	}
}

func fileAttachment(p inputItem, param string) (service.Attachment, error) {
	if p.FileID != "" {
		return service.Attachment{Filename: p.Filename, FileID: p.FileID}, nil
	}
	if p.FileData == "" {
		return service.Attachment{}, badInput(param, "an input_file needs either 'file_data' or 'file_id'")
	}
	mediaType, data, err := decodeInlineFile(p.FileData, param)
	if err != nil {
		return service.Attachment{}, err
	}
	name := p.Filename
	if name == "" {
		name = "input" + extensionFor(mediaType)
	}
	return service.Attachment{Filename: name, Data: data}, nil
}

func imageAttachment(p inputItem, param string) (service.Attachment, error) {
	if p.FileID != "" {
		return service.Attachment{Filename: p.Filename, FileID: p.FileID}, nil
	}
	src := p.ImageURL
	if src == "" {
		src = p.FileData
	}
	if src == "" {
		return service.Attachment{}, badInput(param, "an input_image needs 'image_url' or 'file_id'")
	}
	if !strings.HasPrefix(src, "data:") {
		// This server opens no outbound connections of its own, so it cannot
		// fetch a remote image — and quietly ignoring the part would leave the
		// model answering about an image it never saw.
		return service.Attachment{}, badInput(param,
			"this server does not fetch remote images; inline the bytes as a data URL or upload the file first")
	}
	mediaType, data, err := decodeInlineFile(src, param)
	if err != nil {
		return service.Attachment{}, err
	}
	name := p.Filename
	if name == "" {
		name = "image" + extensionFor(mediaType)
	}
	return service.Attachment{Filename: name, Data: data}, nil
}

// decodeInlineFile reads a data URL (Files §1.1).
//
// A bare base64 payload is refused rather than guessed at: plain text is
// frequently valid base64, so accepting one would sometimes hand the harness a
// different file than the client sent, with nothing to indicate it.
func decodeInlineFile(s, param string) (mediaType string, data []byte, err error) {
	if !strings.HasPrefix(s, "data:") {
		return "", nil, badInput(param, "inline file data must be a data URL")
	}
	rest := s[len("data:"):]
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", nil, badInput(param, "a data URL must contain a comma before its payload")
	}
	isBase64 := false
	if strings.HasSuffix(meta, ";base64") {
		isBase64 = true
		meta = strings.TrimSuffix(meta, ";base64")
	}
	mediaType = strings.TrimSpace(strings.Split(meta, ";")[0])

	if isBase64 {
		// Accept both alphabets: a client that base64-encoded a URL-safe way is
		// not sending a different file.
		b, decErr := base64.StdEncoding.DecodeString(payload)
		if decErr != nil {
			if b, decErr = base64.URLEncoding.DecodeString(payload); decErr != nil {
				return "", nil, badInput(param, "the data URL payload is not valid base64")
			}
		}
		return mediaType, b, nil
	}
	unescaped, escErr := url.PathUnescape(payload)
	if escErr != nil {
		return "", nil, badInput(param, "the data URL payload is not valid percent-encoded text")
	}
	return mediaType, []byte(unescaped), nil
}

// extensionFor names a file the client did not name, so the harness sees
// something it can recognise rather than an extensionless blob.
//
// The common types are tabulated because mime.ExtensionsByType answers
// text/plain with ".asc" as readily as ".txt", and a harness told to read
// "input.asc" is being handed a small puzzle for no reason.
func extensionFor(mediaType string) string {
	switch mediaType {
	case "":
		return ".bin"
	case "text/plain":
		return ".txt"
	case "text/markdown":
		return ".md"
	case "text/csv":
		return ".csv"
	case "text/html":
		return ".html"
	case "application/json":
		return ".json"
	case "application/pdf":
		return ".pdf"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	}
	exts, err := mime.ExtensionsByType(mediaType)
	if err != nil || len(exts) == 0 {
		return ".bin"
	}
	best := exts[0]
	for _, e := range exts {
		if len(e) < len(best) {
			best = e
		}
	}
	return best
}

func (p *parsedInput) appendText(s string) {
	if s == "" {
		return
	}
	if p.Text != "" {
		p.Text += "\n"
	}
	p.Text += s
}
