package http

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func parse(t *testing.T, body string) (parsedInput, error) {
	t.Helper()
	return parseInput(json.RawMessage(body))
}

func TestInputAcceptsTheStringForm(t *testing.T) {
	got, err := parse(t, `"Summarise README.md"`)
	if err != nil {
		t.Fatalf("string input rejected: %v", err)
	}
	if got.Text != "Summarise README.md" || len(got.Attachments) != 0 {
		t.Fatalf("got %+v", got)
	}
}

// The shape the conformance suite sends, and the one the Files chapter
// documents: a user message whose content mixes text with an inline file.
func TestInputAcceptsAMessageWithAnInlineFile(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("The secret token is uhp-1234."))
	body := `[{"role":"user","content":[
		{"type":"input_text","text":"Reply with only the secret token from the attached file."},
		{"type":"input_file","filename":"token.txt","file_data":"data:text/plain;base64,` + data + `"}]}]`

	got, err := parse(t, body)
	if err != nil {
		t.Fatalf("item array rejected: %v", err)
	}
	if got.Text != "Reply with only the secret token from the attached file." {
		t.Errorf("text = %q", got.Text)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %v", got.Attachments)
	}
	a := got.Attachments[0]
	if a.Filename != "token.txt" || string(a.Data) != "The secret token is uhp-1234." {
		t.Errorf("attachment = %+v (%q)", a, a.Data)
	}
}

func TestInputAcceptsBareContentPartsAndStringContent(t *testing.T) {
	got, err := parse(t, `[{"type":"input_text","text":"first"},{"role":"user","content":"second"}]`)
	if err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if got.Text != "first\nsecond" {
		t.Errorf("text = %q", got.Text)
	}
}

func TestInputAcceptsAFileReference(t *testing.T) {
	got, err := parse(t, `[{"role":"user","content":[{"type":"input_file","file_id":"file_abc123"}]}]`)
	if err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].FileID != "file_abc123" {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
}

func TestInlineFileDecoding(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"percent-encoded text", `data:text/plain,hello%20world`, "hello world"},
		{"base64", `data:application/pdf;base64,` + base64.StdEncoding.EncodeToString([]byte("%PDF")), "%PDF"},
		{"no media type", `data:;base64,` + base64.StdEncoding.EncodeToString([]byte("raw")), "raw"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal([]any{map[string]any{
				"type": "input_file", "filename": "f.bin", "file_data": tc.data}})
			got, err := parseInput(body)
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if string(got.Attachments[0].Data) != tc.want {
				t.Errorf("decoded %q, want %q", got.Attachments[0].Data, tc.want)
			}
		})
	}
}

func TestUnnamedInlineFileGetsANameFromItsMediaType(t *testing.T) {
	got, err := parse(t, `[{"type":"input_file","file_data":"data:text/plain,hi"}]`)
	if err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if got.Attachments[0].Filename != "input.txt" {
		t.Errorf("filename = %q", got.Attachments[0].Filename)
	}
}

// Every one of these loses part of the client's prompt if it is accepted
// quietly, which is worse than refusing the request.
func TestInputRejectsWhatItCannotHonour(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"absent", ``},
		{"null", `null`},
		{"empty string", `""`},
		{"a number", `42`},
		{"an empty item array", `[]`},
		{"an unknown part type", `[{"type":"input_audio","text":"x"}]`},
		{"a file with no source", `[{"type":"input_file","filename":"a.txt"}]`},
		{"undecodable base64", `[{"type":"input_file","file_data":"data:text/plain;base64,!!!!"}]`},
		{"a remote image", `[{"type":"input_image","image_url":"https://example.com/cat.png"}]`},
		{"a typeless, textless part", `[{"filename":"a.txt"}]`},
		{"bare base64 with no data URL", `[{"type":"input_file","file_data":"bGVuaWVudA=="}]`},
		{"an assistant role", `[{"role":"assistant","content":"I already answered"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parse(t, tc.body); err == nil {
				t.Fatalf("accepted %s: %+v", tc.name, got)
			}
		})
	}
}
