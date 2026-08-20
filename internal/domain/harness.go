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
	ID             string   `json:"id"`
	Object         string   `json:"object"`
	Name           string   `json:"name"`
	Base           string   `json:"base"`
	BaseLabel      string   `json:"baseLabel,omitempty"`
	DefaultModel   string   `json:"defaultModel,omitempty"`
	SystemPrompt   string   `json:"systemPrompt"`
	McpServers     []any    `json:"mcpServers"`
	Skills         []any    `json:"skills"`
	DisabledTools  []string `json:"disabledTools"`
	MaxStep        *int     `json:"maxStep"`
	TimeoutSeconds *int     `json:"timeoutSeconds"`
	CreatedAt      int64    `json:"createdAt"`

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
