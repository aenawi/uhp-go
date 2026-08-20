package domain

// Capability flags describe what a harness backend can do.
type Capability string

const (
	CapStreaming    Capability = "streaming"
	CapFilesIn      Capability = "files_in"
	CapFilesOut     Capability = "files_out"
	CapSessions     Capability = "sessions"
	CapCancellation Capability = "cancellation"
	CapTools        Capability = "tools"
)

// HarnessStatus values.
const (
	HarnessReady       = "ready"
	HarnessUnavailable = "unavailable"
	HarnessDegraded    = "degraded"
)

// Harness is one configured runtime backend.
//
// The id is `chrn_`-prefixed and opaque, and `base` names the runtime
// ("claude-code", "codex", …). The specification is deliberate that `base` is
// not enumerated and a client must treat it as a string, because anything else
// would mean revising the protocol every time a harness is released.
type Harness struct {
	ID             string      `json:"id"`
	Object         string      `json:"object"`
	Name           string      `json:"name"`
	Base           string      `json:"base"`
	BaseLabel      string      `json:"baseLabel,omitempty"`
	DefaultModel   string      `json:"defaultModel,omitempty"`
	SystemPrompt   string      `json:"systemPrompt"`
	McpServers     []McpServer `json:"mcpServers"`
	Skills         []Skill     `json:"skills"`
	DisabledTools  []string    `json:"disabledTools"`
	MaxStep        *int        `json:"maxStep"`
	TimeoutSeconds *int        `json:"timeoutSeconds"`
	CreatedAt      int64       `json:"createdAt"`

	// Extensions. The harness schema permits additional properties, and these
	// are genuinely useful to a client choosing where to send work.
	Models       []string     `json:"models"`
	Capabilities []Capability `json:"capabilities"`
	Status       string       `json:"status"`
}

// HasCapability reports whether the harness advertises a given capability.
func (h Harness) HasCapability(c Capability) bool {
	for _, have := range h.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// McpServer is a remote MCP server attached to a harness (Harnesses §4.1).
//
// `Auth` is stored but never serialized back to a client: it holds a bearer
// token or a server-side reference, and the specification is explicit that a
// server must never return a resolved credential. See redactedMcpServers.
type McpServer struct {
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Transport string            `json:"transport"`
	Enabled   *bool             `json:"enabled"`
	Headers   map[string]string `json:"headers,omitempty"`
	Auth      string            `json:"auth,omitempty"`
}

// Skill is a folder of files the agent can read, not a single file
// (Harnesses §4.2).
type Skill struct {
	Name    string      `json:"name"`
	Enabled *bool       `json:"enabled"`
	Files   []SkillFile `json:"files,omitempty"`

	// Content is the shorthand for a bundle whose only member is SKILL.md.
	Content string `json:"content,omitempty"`
}

// SkillFile is one member of a skill folder. Text arrives in Content, binary
// in ContentB64, and a server must preserve both byte-for-byte.
type SkillFile struct {
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	ContentB64 string `json:"content_b64,omitempty"`
}

// HarnessConfig is the mutable configuration of a harness created over the
// API (Harnesses §5), and is what this server persists.
//
// It is a distinct type from Harness deliberately. Half of a Harness is
// computed — which models the base advertises, what it can do, whether its CLI
// is reachable right now — and persisting a computed field means serving a
// stale answer once the world moves underneath it. Only what a client actually
// set is stored; the rest is recomputed from the base on every read.
type HarnessConfig struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Base           string      `json:"base"`
	DefaultModel   string      `json:"defaultModel,omitempty"`
	SystemPrompt   string      `json:"systemPrompt,omitempty"`
	McpServers     []McpServer `json:"mcpServers,omitempty"`
	Skills         []Skill     `json:"skills,omitempty"`
	DisabledTools  []string    `json:"disabledTools,omitempty"`
	MaxStep        *int        `json:"maxStep,omitempty"`
	TimeoutSeconds *int        `json:"timeoutSeconds,omitempty"`
	CreatedAt      int64       `json:"createdAt"`
}
