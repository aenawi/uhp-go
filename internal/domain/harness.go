package domain

import "github.com/aenawi/uhp-go/uhp"

// The harness object itself is not declared here. It is
// [github.com/aenawi/uhp-go/uhp/uhpgo.Harness] — the protocol's thirteen-field
// object plus this server's three additions — because it is a wire shape, and
// no wire shape lives outside uhp. What stays is the one harness type that is
// genuinely internal.

// HarnessConfig is the mutable configuration of a harness created over the API
// (Harnesses §5), and is what this server persists.
//
// It is a distinct type from the harness object deliberately, and not a wire
// shape despite the family resemblance. Half of a harness is computed — which
// models the base advertises, what it can do, whether its CLI is reachable
// right now — and persisting a computed field means serving a stale answer once
// the world moves underneath it. Only what a client actually set is stored; the
// rest is recomputed from the base on every read.
type HarnessConfig struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Base           string          `json:"base"`
	DefaultModel   string          `json:"defaultModel,omitempty"`
	SystemPrompt   string          `json:"systemPrompt,omitempty"`
	McpServers     []uhp.McpServer `json:"mcpServers,omitempty"`
	Skills         []uhp.Skill     `json:"skills,omitempty"`
	DisabledTools  []string        `json:"disabledTools,omitempty"`
	MaxStep        *int            `json:"maxStep,omitempty"`
	TimeoutSeconds *int            `json:"timeoutSeconds,omitempty"`
	CreatedAt      int64           `json:"createdAt"`
}
