package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
	"github.com/aenawi/uhp-go/uhp"
)

// skillsSubdir is where a session's skill folders are written, under its own
// working directory.
//
// Dotted and namespaced so that a skill folder can never collide with the
// agent's own files, and so that a task listing its artifacts does not find the
// router's scaffolding among them.
const skillsSubdir = ".uhp/skills"

// mcpConfigName is the generated per-run MCP configuration file.
const mcpConfigName = ".uhp/mcp.json"

// runtimeConfig is everything a harness's own configuration contributes to one
// run: files written to disk, argv the runner will add, and the standing
// instructions that carry whatever the runtime cannot enforce itself.
type runtimeConfig struct {
	SkillDirs     []string
	McpConfigPath string
	DisabledTools []string

	// StandingInstructions is the harness's own block, prepended to the task
	// input. It holds the system prompt, plus anything the runtime cannot
	// deliver natively — Harnesses §4.3 is explicit that a restriction the
	// runtime cannot enforce MUST still reach the agent, and MUST NOT be
	// dropped.
	//
	// "Standing" is the qualifier that keeps this apart from a task's own
	// `instructions`, which is the protocol's word for a different thing: this
	// one is the operator's and applies to every task on the harness, that one
	// is the caller's and applies to one. composePrompt joins them, in that
	// order, and CONTEXT.md carries both terms.
	//
	// Every runtime receives it as prompt text today, because no adapter is
	// asked whether it takes a system prompt natively — see #79.
	StandingInstructions string
}

// prepareRuntime materializes a managed harness's configuration into the
// session's working directory and returns what the run needs to carry.
//
// Everything here happens before the harness starts, and everything it writes
// is fingerprinted with the rest of the directory beforehand, so the router's
// own scaffolding never comes back as one of the task's artifacts.
func (s *TaskService) prepareRuntime(
	cfg domain.HarnessConfig, adapter harness.Adapter, workDir string,
) (runtimeConfig, error) {
	var out runtimeConfig
	var standing []string

	if cfg.SystemPrompt != "" {
		standing = append(standing, cfg.SystemPrompt)
	}

	delivery := deliveryOf(adapter)

	enabled := enabledSkills(cfg.Skills)
	if len(enabled) > 0 {
		dirs, err := writeSkillFolders(workDir, enabled)
		if err != nil {
			return runtimeConfig{}, err
		}
		out.SkillDirs = dirs
		// A runtime that loads the folders itself does not also need to be
		// told about them in the prompt; one that cannot does, or the folders
		// sit on disk unread.
		if !delivery.Skills {
			standing = append(standing, skillInstruction(enabled, dirs))
		}
	}

	if servers := enabledServers(cfg.McpServers); len(servers) > 0 {
		path, err := writeMcpConfig(workDir, servers)
		if err != nil {
			return runtimeConfig{}, err
		}
		out.McpConfigPath = path
	}

	if len(cfg.DisabledTools) > 0 {
		if delivery.ToolBlock {
			out.DisabledTools = cfg.DisabledTools
		} else {
			// Weaker than a block, and described as exactly that. Dropping it
			// instead would leave the operator believing a tool is off.
			standing = append(standing, "You must not use the following tools: "+
				strings.Join(cfg.DisabledTools, ", ")+
				". This restriction is not enforced by the runtime; honour it.")
		}
	}

	out.StandingInstructions = strings.Join(standing, "\n\n")
	return out, nil
}

// deliveryOf asks an adapter what it enforces. An adapter that cannot say is
// taken to enforce nothing, so the router conveys more and claims less.
func deliveryOf(a harness.Adapter) harness.Delivery {
	if d, ok := a.(harness.Deliverer); ok {
		return d.Delivery()
	}
	return harness.Delivery{}
}

// enabledSkills drops the suppressed ones. `enabled: false` suppresses a
// skill, and a suppressed skill must not be materialized at all — a folder on
// disk is readable whether or not anyone pointed the agent at it.
func enabledSkills(skills []uhp.Skill) []uhp.Skill {
	out := make([]uhp.Skill, 0, len(skills))
	for _, sk := range skills {
		if sk.Enabled == nil || *sk.Enabled {
			out = append(out, sk)
		}
	}
	return out
}

// enabledServers drops the disabled MCP entries before they reach the
// generated configuration.
//
// Harnesses §4.1: "A disabled entry MUST NOT be contacted at all — not
// connected and then hidden, which would still leak the turn's existence to
// whoever operates that endpoint." Filtering here is what makes that true:
// the runtime never learns the entry exists.
func enabledServers(servers []uhp.McpServer) []uhp.McpServer {
	out := make([]uhp.McpServer, 0, len(servers))
	for _, m := range servers {
		if m.Enabled == nil || *m.Enabled {
			out = append(out, m)
		}
	}
	return out
}

// writeSkillFolders writes each bundle as a real folder and returns the
// directories, in configuration order.
//
// The whole folder, not just the manifest: Harnesses §4.2 calls out that
// materializing only SKILL.md "breaks every skill that carries references,
// scripts or data — which is most non-trivial skills."
func writeSkillFolders(workDir string, skills []uhp.Skill) ([]string, error) {
	if workDir == "" {
		// Refused rather than skipped. A task that runs without the skills it
		// was configured with produces a plausible answer from an agent that
		// was never given what it needed, and nothing in the response says so.
		return nil, fmt.Errorf("%w: skills need a session working directory; set UHP_WORKSPACE",
			ErrFilesUnsupported)
	}
	root := filepath.Join(workDir, filepath.FromSlash(skillsSubdir))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("service: create skills directory: %w", err)
	}

	dirs := make([]string, 0, len(skills))
	for _, sk := range skills {
		name := sanitizeSkillName(sk.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: skill %q has no usable folder name", ErrInvalidSkill, sk.Name)
		}
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("service: create skill folder: %w", err)
		}
		for _, f := range filesOf(sk) {
			if err := writeSkillFile(dir, f); err != nil {
				return nil, err
			}
		}
		dirs = append(dirs, dir)
	}
	return dirs, nil
}

// filesOf returns a bundle's members, expanding the single-file `content`
// shorthand into the SKILL.md it stands for.
func filesOf(sk uhp.Skill) []uhp.SkillFile {
	if len(sk.Files) > 0 {
		return sk.Files
	}
	if sk.Content != "" {
		return []uhp.SkillFile{{Path: "SKILL.md", Content: sk.Content}}
	}
	return nil
}

func writeSkillFile(dir string, f uhp.SkillFile) error {
	// Validated at configuration time, and re-checked here because this is the
	// line that turns a path into a write. A check that lives only at the far
	// end of a store is one a later code path can walk around.
	if err := validSkillPath(f.Path); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidSkill, f.Path, err)
	}
	full := filepath.Join(dir, filepath.FromSlash(f.Path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return fmt.Errorf("service: create skill subdirectory: %w", err)
	}

	data := []byte(f.Content)
	if f.ContentB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(f.ContentB64)
		if err != nil {
			return fmt.Errorf("%w: %q is not valid base64", ErrInvalidSkill, f.Path)
		}
		// Byte-for-byte, which §4.2 requires of both forms.
		data = decoded
	}
	if err := os.WriteFile(full, data, 0o600); err != nil {
		return fmt.Errorf("service: write skill file: %w", err)
	}
	return nil
}

// sanitizeSkillName reduces a name to something safe to use as a directory.
// Harnesses §4.1 says as much of an MCP server's name; a skill's name lands on
// a filesystem, so it gets the same treatment.
func sanitizeSkillName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	// A name of dots would be a traversal wearing a disguise.
	return strings.Trim(b.String(), ".")
}

// mcpDocument is the generated configuration, in the shape Claude Code's
// `--mcp-config` expects.
type mcpDocument struct {
	McpServers map[string]mcpEntry `json:"mcpServers"`
}

type mcpEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// writeMcpConfig writes the enabled servers to a file the runtime can read,
// and returns its path.
//
// The credential is materialized as an Authorization header rather than a
// field of its own, which is what an HTTP MCP transport actually needs. The
// file lands inside the session's working directory at 0600 — the agent runs
// there, so this is not a secrecy boundary against the agent itself, but it is
// one against everything else on the host.
func writeMcpConfig(workDir string, servers []uhp.McpServer) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("%w: mcp servers need a session working directory; set UHP_WORKSPACE",
			ErrFilesUnsupported)
	}
	doc := mcpDocument{McpServers: make(map[string]mcpEntry, len(servers))}
	for _, m := range servers {
		entry := mcpEntry{Type: transportOf(m), URL: m.URL, Headers: map[string]string{}}
		for k, v := range m.Headers {
			entry.Headers[k] = v
		}
		if m.Auth != "" {
			entry.Headers["Authorization"] = "Bearer " + m.Auth
		}
		if len(entry.Headers) == 0 {
			entry.Headers = nil
		}
		doc.McpServers[sanitizeSkillName(m.Name)] = entry
	}

	path := filepath.Join(workDir, filepath.FromSlash(mcpConfigName))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("service: create mcp config directory: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("service: encode mcp config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("service: write mcp config: %w", err)
	}
	return path, nil
}

func transportOf(m uhp.McpServer) string {
	if m.Transport == "sse" {
		return "sse"
	}
	return "http"
}

// skillInstruction names the folders for a runtime that cannot load them
// itself. It lists the skills by name and says where they are, which is the
// least a model needs to go and read one.
func skillInstruction(skills []uhp.Skill, dirs []string) string {
	names := make([]string, 0, len(skills))
	for i, sk := range skills {
		if i < len(dirs) {
			names = append(names, fmt.Sprintf("%s (%s)", sk.Name, dirs[i]))
		}
	}
	sort.Strings(names)
	return "The following skills are available to you as folders on disk: " +
		strings.Join(names, ", ") +
		". Read a skill's SKILL.md before using it; each folder may also contain " +
		"references, scripts and data the skill needs."
}
