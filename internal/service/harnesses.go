package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/harness"
)

// Errors the transport maps onto UHP status codes.
var (
	// ErrHarnessManagementUnsupported is a deployment that has no harness
	// store. It is deliberately distinct from a bad request: nothing about the
	// request is wrong, this server is simply not configured to keep what it
	// would create.
	ErrHarnessManagementUnsupported = errors.New(
		"service: harness management requires a configured harness store")

	// ErrHarnessNotManaged is an attempt to change a harness that came from
	// this server's own configuration rather than from the API.
	ErrHarnessNotManaged = errors.New("service: harness is not managed over the API")

	// ErrBaseImmutable is an update that would change a harness's base.
	ErrBaseImmutable = errors.New("service: a harness base cannot be changed")

	ErrInvalidSkill     = errors.New("service: invalid skill bundle")
	ErrInvalidMcpServer = errors.New("service: invalid mcp server")
)

// UnsupportedBaseError is a create naming a base this server cannot run.
//
// It carries the list of bases that would work, because Errors §1 requires the
// dotted path and structured context whenever there is one, and a client told
// only "unsupported" has to guess. Harnesses §5.1 makes this a 422 rather than
// an acceptance: a base accepted here and discovered to be unrunnable at the
// first task fails after the client has already committed to it.
type UnsupportedBaseError struct {
	Base      string
	Supported []string
}

func (e *UnsupportedBaseError) Error() string {
	return fmt.Sprintf("service: harness base %q is not supported by this server", e.Base)
}

// HarnessSpec is the configuration a client supplies on create or update.
//
// There is no "absent" for an optional field, because Harnesses §5.2 defines
// update as a replacement of the mutable configuration: a field the client did
// not send is a field it does not want. The two immutable fields — id and
// createdAt — are not here at all, so no code path can even express changing
// them.
type HarnessSpec struct {
	Name           string
	Base           string
	DefaultModel   string
	SystemPrompt   string
	McpServers     []domain.McpServer
	Skills         []domain.Skill
	DisabledTools  []string
	MaxStep        *int
	TimeoutSeconds *int
}

// HarnessManagementEnabled reports whether this server can create harnesses.
//
// Discovery reports this rather than a hardcoded value, for the same reason it
// computes the file capabilities: it depends on configuration, and a
// capability advertised unconditionally is one a client believes right up
// until the request fails.
func (s *TaskService) HarnessManagementEnabled() bool { return s.harnesses != nil }

// SupportedBases lists the harness runtimes this server can actually run,
// ordered so that successive callers get the same answer.
func (s *TaskService) SupportedBases() []string {
	seen := make(map[string]bool)
	var out []string
	for _, h := range s.registry.List() {
		if h.Base == "" || seen[h.Base] {
			continue
		}
		seen[h.Base] = true
		out = append(out, h.Base)
	}
	sort.Strings(out)
	return out
}

// ListHarnesses answers GET /v1/harnesses with both the harnesses compiled
// into this server and the ones a client created.
func (s *TaskService) ListHarnesses(ctx context.Context) ([]domain.Harness, error) {
	out := s.registry.List()
	if s.harnesses == nil {
		return out, nil
	}
	configs, err := s.harnesses.ListHarnesses(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: list harnesses: %v", ErrStorage, err)
	}
	for _, cfg := range configs {
		out = append(out, s.harnessView(cfg))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetHarness answers GET /v1/harnesses/{id}, accepting an id or an alias.
func (s *TaskService) GetHarness(ctx context.Context, id string) (domain.Harness, bool, error) {
	if a, ok := s.registry.Get(id); ok {
		return a.Info(), true, nil
	}
	cfg, ok, err := s.managedConfig(ctx, id)
	if err != nil || !ok {
		return domain.Harness{}, false, err
	}
	return s.harnessView(cfg), true, nil
}

// ModelAvailable reports whether a harness can serve a model right now.
func (s *TaskService) ModelAvailable(ctx context.Context, harnessID, model string) bool {
	a, ok, err := s.adapterFor(ctx, harnessID)
	if err != nil || !ok {
		return false
	}
	if av, ok := a.(interface{ Available(string) bool }); ok {
		return av.Available(model)
	}
	return true
}

// CreateHarness implements POST /v1/harnesses (Harnesses §5.1).
func (s *TaskService) CreateHarness(ctx context.Context, spec HarnessSpec) (domain.Harness, error) {
	if s.harnesses == nil {
		return domain.Harness{}, ErrHarnessManagementUnsupported
	}
	if spec.Base == "" {
		return domain.Harness{}, fmt.Errorf("%w: `base` is required", ErrInvalidInput)
	}
	base, ok := s.baseAdapter(spec.Base)
	if !ok {
		return domain.Harness{}, &UnsupportedBaseError{Base: spec.Base, Supported: s.SupportedBases()}
	}
	cfg, err := s.applySpec(spec, domain.HarnessConfig{
		ID:        newHarnessID(),
		Base:      spec.Base,
		CreatedAt: time.Now().UnixMilli(),
	}, base)
	if err != nil {
		return domain.Harness{}, err
	}
	if err := s.harnesses.PutHarness(ctx, cfg); err != nil {
		return domain.Harness{}, fmt.Errorf("%w: persist harness: %v", ErrStorage, err)
	}
	return s.harnessView(cfg), nil
}

// UpdateHarness implements PUT /v1/harnesses/{id} (Harnesses §5.2): it
// replaces the mutable configuration and leaves id, base and createdAt alone.
func (s *TaskService) UpdateHarness(ctx context.Context, id string, spec HarnessSpec) (domain.Harness, error) {
	existing, err := s.managedForWrite(ctx, id)
	if err != nil {
		return domain.Harness{}, err
	}
	// Refused rather than silently ignored. Changing a base would change the
	// behaviour of every session already attached to the harness, and a client
	// that asked for it and was answered 200 would believe it happened.
	if spec.Base != "" && spec.Base != existing.Base {
		return domain.Harness{}, fmt.Errorf("%w: %q is on base %q; create a separate harness for %q",
			ErrBaseImmutable, id, existing.Base, spec.Base)
	}
	// The base is looked up but its absence is not fatal here. §5.2 forbids
	// changing a base, not editing a harness whose base has since been removed
	// from the deployment: refusing would strand every such harness as
	// readable, unrunnable and uneditable, and would report `unsupported_base`
	// for a base the client never sent.
	base, _ := s.baseAdapter(existing.Base)
	cfg, err := s.applySpec(spec, domain.HarnessConfig{
		ID:        existing.ID,
		Base:      existing.Base,
		CreatedAt: existing.CreatedAt,
	}, base)
	if err != nil {
		return domain.Harness{}, err
	}
	cfg.McpServers = carryForwardCredentials(cfg.McpServers, existing.McpServers)
	if err := s.harnesses.PutHarness(ctx, cfg); err != nil {
		return domain.Harness{}, fmt.Errorf("%w: persist harness: %v", ErrStorage, err)
	}
	return s.harnessView(cfg), nil
}

// DeleteHarness implements DELETE /v1/harnesses/{id} (Harnesses §5.3).
//
// The sessions and responses that used the harness are deliberately left
// alone: history that disappears when configuration changes cannot be audited.
// A later turn in such a session is refused with harness_not_found, which is
// the truthful answer.
func (s *TaskService) DeleteHarness(ctx context.Context, id string) error {
	if _, err := s.managedForWrite(ctx, id); err != nil {
		return err
	}
	if err := s.harnesses.DeleteHarness(ctx, id); err != nil {
		return fmt.Errorf("%w: delete harness: %v", ErrStorage, err)
	}
	return nil
}

// managedForWrite resolves a harness that a write may touch, distinguishing
// the three ways it can be absent: management is off, there is no such
// harness, or there is one but this server owns it.
func (s *TaskService) managedForWrite(ctx context.Context, id string) (domain.HarnessConfig, error) {
	if s.harnesses == nil {
		return domain.HarnessConfig{}, ErrHarnessManagementUnsupported
	}
	cfg, ok, err := s.managedConfig(ctx, id)
	if err != nil {
		return domain.HarnessConfig{}, err
	}
	if ok {
		return cfg, nil
	}
	if _, isBuiltIn := s.registry.Get(id); isBuiltIn {
		return domain.HarnessConfig{}, fmt.Errorf(
			"%w: %q comes from this server's own configuration", ErrHarnessNotManaged, id)
	}
	return domain.HarnessConfig{}, fmt.Errorf("%w: %q", ErrHarnessNotFound, id)
}

func (s *TaskService) managedConfig(ctx context.Context, id string) (domain.HarnessConfig, bool, error) {
	if s.harnesses == nil {
		return domain.HarnessConfig{}, false, nil
	}
	cfg, ok, err := s.harnesses.GetHarness(ctx, id)
	if err != nil {
		return domain.HarnessConfig{}, false, fmt.Errorf("%w: read harness: %v", ErrStorage, err)
	}
	return cfg, ok, nil
}

// applySpec validates a spec and folds it onto `keep` — the id, base and
// createdAt of the harness being written, which no spec can reach.
//
// A nil base means this server no longer has the runtime this harness was
// built on. The structural checks still run; only the model check is skipped,
// because there is no catalogue left to check against and inventing a refusal
// would be worse than storing a value for a harness that cannot run anyway.
func (s *TaskService) applySpec(spec HarnessSpec, keep domain.HarnessConfig, base harness.Adapter) (domain.HarnessConfig, error) {
	if base != nil {
		if err := validateDefaultModel(base, spec.DefaultModel); err != nil {
			return domain.HarnessConfig{}, err
		}
	}
	servers, err := normalizeMcpServers(spec.McpServers)
	if err != nil {
		return domain.HarnessConfig{}, err
	}
	skills, err := normalizeSkills(spec.Skills)
	if err != nil {
		return domain.HarnessConfig{}, err
	}

	keep.Name = spec.Name
	if keep.Name == "" && base != nil {
		keep.Name = base.Info().Name
	}
	keep.DefaultModel = spec.DefaultModel
	keep.SystemPrompt = spec.SystemPrompt
	keep.McpServers = servers
	keep.Skills = skills
	keep.DisabledTools = append([]string(nil), spec.DisabledTools...)
	keep.MaxStep = spec.MaxStep
	keep.TimeoutSeconds = spec.TimeoutSeconds
	return keep, nil
}

// baseAdapter resolves a base name to the adapter that runs it.
//
// The Info().Base check is what stops a `chrn_` id being accepted as a base:
// the registry answers to both, but a harness id is not a runtime name, and
// storing one in `base` would hand a client an opaque string where the
// specification promises the name of a runtime.
func (s *TaskService) baseAdapter(base string) (harness.Adapter, bool) {
	if base == "" {
		return nil, false
	}
	a, ok := s.registry.Get(base)
	if !ok || a.Info().Base != base {
		return nil, false
	}
	return a, true
}

// harnessView renders a stored configuration as the harness object a client
// sees. Managed.Info recomputes the base-derived fields and strips the MCP
// credentials.
func (s *TaskService) harnessView(cfg domain.HarnessConfig) domain.Harness {
	base, _ := s.baseAdapter(cfg.Base)
	return harness.NewManaged(cfg, base).Info()
}

// adapterFor resolves a harness id — compiled-in or managed, canonical or
// alias — to something that can run a task.
func (s *TaskService) adapterFor(ctx context.Context, id string) (harness.Adapter, bool, error) {
	if a, ok := s.registry.Get(id); ok {
		return a, true, nil
	}
	cfg, ok, err := s.managedConfig(ctx, id)
	if err != nil || !ok {
		return nil, false, err
	}
	base, _ := s.baseAdapter(cfg.Base)
	// A nil base is passed through deliberately: the harness exists and must
	// resolve, and Managed reports the missing base rather than pretending the
	// harness is gone.
	return harness.NewManaged(cfg, base), true, nil
}

// newHarnessID mints an opaque `chrn_` id.
//
// Random, unlike the compiled-in harnesses whose ids are derived from their
// base: two harnesses can share a base, so a derived id would collide, and a
// client that deleted one and created another with the same name and base
// would find the old id still resolving.
func newHarnessID() string {
	return "chrn_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func validateDefaultModel(base harness.Adapter, model string) error {
	if model == "" {
		return nil
	}
	info := base.Info()
	for _, m := range info.Models {
		if m == model {
			return nil
		}
	}
	// Same reasoning as an unsupported base: a default the runtime cannot
	// serve is a configuration that fails at the first task, once the client
	// has committed to it.
	return fmt.Errorf("%w %q for base %q (supported: %v)",
		harness.ErrUnsupportedModel, model, info.Base, info.Models)
}

// normalizeMcpServers validates the MCP entries and makes their defaults
// explicit, so a client never has to infer whether an entry is enabled.
func normalizeMcpServers(servers []domain.McpServer) ([]domain.McpServer, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	out := make([]domain.McpServer, 0, len(servers))
	for i, m := range servers {
		if strings.TrimSpace(m.Name) == "" {
			return nil, fmt.Errorf("%w: mcp_servers[%d] has no name", ErrInvalidMcpServer, i)
		}
		if strings.TrimSpace(m.URL) == "" {
			return nil, fmt.Errorf("%w: mcp_servers[%d] has no url", ErrInvalidMcpServer, i)
		}
		if m.Transport == "" {
			m.Transport = "http"
		}
		if m.Transport != "http" && m.Transport != "sse" {
			return nil, fmt.Errorf("%w: mcp_servers[%d] names transport %q, which is neither http nor sse",
				ErrInvalidMcpServer, i, m.Transport)
		}
		if m.Enabled == nil {
			enabled := true
			m.Enabled = &enabled
		}
		out = append(out, m)
	}
	return out, nil
}

// normalizeSkills validates a skill bundle at configuration time.
//
// Harnesses §4.2: "A bundle MUST contain a SKILL.md; a server MUST reject one
// that does not, at configuration time rather than at run time." The timing is
// the whole point. A bundle refused here is a mistake the client can still
// fix; one accepted and ignored later looks like the agent behaving oddly,
// weeks afterwards, to someone who has no reason to suspect the skill.
func normalizeSkills(skills []domain.Skill) ([]domain.Skill, error) {
	if len(skills) == 0 {
		return nil, nil
	}
	out := make([]domain.Skill, 0, len(skills))
	for i, sk := range skills {
		if strings.TrimSpace(sk.Name) == "" {
			return nil, fmt.Errorf("%w: skills[%d] has no name", ErrInvalidSkill, i)
		}
		if err := validateSkillFiles(i, sk); err != nil {
			return nil, err
		}
		if sk.Enabled == nil {
			enabled := true
			sk.Enabled = &enabled
		}
		sk.Files = append([]domain.SkillFile(nil), sk.Files...)
		out = append(out, sk)
	}
	return out, nil
}

func validateSkillFiles(idx int, sk domain.Skill) error {
	// `content` is the shorthand for a bundle whose only member is SKILL.md,
	// so it satisfies the manifest requirement by itself.
	if len(sk.Files) == 0 {
		if sk.Content != "" {
			return nil
		}
		return fmt.Errorf("%w: skills[%d] (%q) carries no files and no content", ErrInvalidSkill, idx, sk.Name)
	}

	var manifest bool
	for j, f := range sk.Files {
		if err := validSkillPath(f.Path); err != nil {
			return fmt.Errorf("%w: skills[%d].files[%d] (%q): %v", ErrInvalidSkill, idx, j, sk.Name, err)
		}
		if f.Path == "SKILL.md" {
			manifest = true
		}
		if f.ContentB64 != "" {
			if _, err := base64.StdEncoding.DecodeString(f.ContentB64); err != nil {
				// Stored as-is it would be materialised as garbage, and the
				// only symptom would be the agent misreading its own skill.
				return fmt.Errorf("%w: skills[%d].files[%d] (%q) is not valid base64",
					ErrInvalidSkill, idx, j, sk.Name)
			}
		}
	}
	if !manifest {
		return fmt.Errorf("%w: skills[%d] (%q) has no SKILL.md, so it would be stored and then ignored",
			ErrInvalidSkill, idx, sk.Name)
	}
	return nil
}

// validSkillPath refuses anything that is not a plain relative path inside the
// skill's own folder. The folder is materialised on disk where the agent can
// read it, so a member that escapes it writes wherever it likes.
func validSkillPath(p string) error {
	if p == "" {
		return errors.New("the path is empty")
	}
	if strings.ContainsAny(p, "\\\x00") {
		return errors.New("the path contains a backslash or a NUL byte")
	}
	if strings.HasPrefix(p, "/") {
		return errors.New("the path is absolute")
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("the path escapes the skill folder")
	}
	return nil
}

// carryForwardCredentials keeps an MCP credential the client cannot resend.
//
// Two of the specification's rules meet here and cannot both be taken
// literally. §5.2 makes an update a replacement of the mutable configuration,
// while §4.1 forbids ever returning a resolved credential — so a client that
// reads a harness and PUTs it back has no `auth` to send, because it was never
// given one. Treating that absence as "remove it" would disconnect a working
// MCP server on an unrelated rename, which is the round-trip loss §4.2 calls
// out for skills, landing on the one field a client is structurally unable to
// echo. So an absent `auth` keeps the stored one; an entry that does send one
// replaces it, and deleting the entry removes it with the entry.
func carryForwardCredentials(next, previous []domain.McpServer) []domain.McpServer {
	if len(next) == 0 || len(previous) == 0 {
		return next
	}
	stored := make(map[string]string, len(previous))
	for _, m := range previous {
		if m.Auth != "" {
			stored[m.Name] = m.Auth
		}
	}
	for i := range next {
		if next[i].Auth == "" {
			next[i].Auth = stored[next[i].Name]
		}
	}
	return next
}
