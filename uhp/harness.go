package uhp

import "encoding/json"

// Harness is one configured runtime backend: the thing that turns a model into
// a working agent by planning, calling tools and iterating.
//
// The field names on the wire are camelCase here and snake_case on the task
// surface. That is not a transcription slip — the specification records it as a
// known compromise of this version, because both spellings are load-bearing in
// shipped clients and unifying them would break one set of them for a cosmetic
// gain. [HarnessCreate], which writes the same concept, is snake_case.
type Harness struct {
	// ID is "chrn_"-prefixed and opaque.
	ID string `json:"id"`
	// Object is always "harness".
	Object string `json:"object"`
	Name   string `json:"name"`

	// Base names the runtime — "codex", "claude-code", "hermes". It is
	// deliberately not enumerated by the specification and a client MUST treat
	// it as a string, because an enum would mean revising the protocol every
	// time a harness is released. It is a plain string here for that reason and
	// will stay one.
	Base string `json:"base"`

	BaseLabel    string `json:"baseLabel,omitempty"`
	DefaultModel string `json:"defaultModel,omitempty"`
	SystemPrompt string `json:"systemPrompt"`

	// The three lists are always present and never null, even when empty. See
	// [Harness.MarshalJSON]: an absent `skills` and an empty one are different
	// claims, and only one of them is "this harness has no skills".
	McpServers    []McpServer `json:"mcpServers"`
	Skills        []Skill     `json:"skills"`
	DisabledTools []string    `json:"disabledTools"`

	// MaxStep and TimeoutSeconds are the harness's own budgets, and are
	// nullable: null means unbounded, which is a different statement from zero.
	MaxStep        *int `json:"maxStep"`
	TimeoutSeconds *int `json:"timeoutSeconds"`

	// CreatedAt is Unix *milliseconds* here, where [Response].CreatedAt is Unix
	// seconds. The two chapters disagree and the schema records the
	// disagreement rather than smoothing it over; so does this package.
	CreatedAt int64 `json:"createdAt"`
}

// MarshalJSON renders the harness with `object` defaulted and its three lists
// always present as arrays.
//
// A nil Go slice marshals as null, and the schema declares all three as
// type: array — so a zero-valued Harness would otherwise produce a document
// that fails validation against the very schema this type mirrors. Emitting an
// empty array also keeps a real distinction a client can use: no skills is not
// the same statement as a server that does not report skills.
//
// A type that embeds Harness and adds fields of its own MUST define its own
// MarshalJSON. Go promotes this method, and a promoted marshaller renders only
// the fields it can see, so the additions would be dropped silently — see
// [github.com/aenawi/uhp-go/uhp/uhpgo.Harness], which is exactly that case.
func (h Harness) MarshalJSON() ([]byte, error) {
	type harness Harness
	out := harness(h)
	// `object` is pinned to a constant, so an empty one fails validation where
	// an absent one would merely be optional. This type has a marshaller
	// already, so it defaults the constant rather than leaning on omitempty —
	// which is the rule the types without a marshaller follow instead.
	if out.Object == "" {
		out.Object = "harness"
	}
	if out.McpServers == nil {
		out.McpServers = []McpServer{}
	}
	if out.Skills == nil {
		out.Skills = []Skill{}
	}
	if out.DisabledTools == nil {
		out.DisabledTools = []string{}
	}
	return json.Marshal(out)
}

// McpServer is a remote MCP server attached to a harness (Harnesses §4.1).
//
// Only enabled entries are connected for a turn, and a disabled entry MUST NOT
// be contacted at all. An unreachable server MUST NOT fail the task — a tool
// the agent could not reach is a smaller problem than a task that refused to
// run.
type McpServer struct {
	// Name is sanitised to a CLI-safe identifier by the server.
	Name string `json:"name"`
	URL  string `json:"url"`
	// Transport is "http" or "sse"; "http" is the default.
	Transport string `json:"transport,omitempty"`
	// Enabled defaults to true. A pointer because the default is true, so a
	// plain bool could not express "disabled" — the same reason
	// [CreateResponseRequest].Store is one.
	Enabled *bool             `json:"enabled,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Auth is a bearer token, or a server-side reference the server resolves.
	//
	// A server MUST NEVER return a resolved credential to a client. A server
	// populating this field on the way out is the bug that rule exists to
	// prevent, and the field being writable is what makes reading it back
	// tempting.
	Auth string `json:"auth,omitempty"`
}

// Skill is a folder of files an agent can read — a folder, not a file.
//
// A server must materialise the whole folder where the agent can read it.
// Materialising only SKILL.md breaks every skill that carries references,
// scripts or data, and round-tripping a harness through GET and PUT must not
// lose skill contents.
type Skill struct {
	Name string `json:"name"`
	// Enabled defaults to true; false suppresses the skill, including one
	// inherited from the base.
	Enabled *bool `json:"enabled,omitempty"`
	// Files is the bundle and must contain a SKILL.md.
	Files []SkillFile `json:"files,omitempty"`
	// Content is shorthand for a single-file bundle whose only member is
	// SKILL.md.
	Content string `json:"content,omitempty"`
	// Blob is a server-assigned handle for a bundle stored out of line. A
	// client receives it, passes it back unchanged, and reads the files from
	// the skill files endpoint.
	Blob string `json:"blob,omitempty"`
}

// SkillFile is one member of a skill folder.
type SkillFile struct {
	// Path is relative to the skill's own folder and may name nested
	// directories: "SKILL.md", "references/codes.md", "assets/logo.png". A
	// server MUST reject a path that escapes the folder.
	Path string `json:"path"`
	// Content is text content.
	Content string `json:"content,omitempty"`
	// ContentB64 is base64 for binary content, preserved byte-for-byte.
	ContentB64 string `json:"content_b64,omitempty"`
}

// HarnessCreate is the body of a harness create or update (Harnesses §5).
//
// It is snake_case where [Harness] is camelCase — the same known compromise
// noted there. Only Base is required: everything else either has a default or
// is genuinely optional, and the fields a client did not set are the ones the
// server computes from the base rather than stores.
type HarnessCreate struct {
	Name string `json:"name,omitempty"`
	Base string `json:"base"`

	DefaultModel string `json:"default_model,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`

	McpServers    []McpServer `json:"mcp_servers,omitempty"`
	Skills        []Skill     `json:"skills,omitempty"`
	DisabledTools []string    `json:"disabled_tools,omitempty"`

	MaxStep        *int `json:"max_step,omitempty"`
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
}
