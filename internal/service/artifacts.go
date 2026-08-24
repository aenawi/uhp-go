package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"

	// net/http is here for DetectContentType alone — the sniffing table, not
	// the transport. The service still knows nothing about requests.
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
)

// Artifact capture works by diffing the session's own working directory across
// a run. No CLI harness reports the files it wrote — none of the five has a
// flag for it — so the alternative to diffing is advertising `files_out` and
// never producing an artifact, which is what this server used to do.
//
// The directory is per-session and the router creates it, so "what appeared or
// changed while the harness ran" is a well-defined question with a cheap
// answer. Files the router itself materialized as task input are snapshotted
// before the run starts and therefore are not reported back as output.
const (
	// maxCapturedArtifacts bounds how many files one task can report. A task
	// that ran `npm install` should not turn into ten thousand artifacts.
	maxCapturedArtifacts = 200

	// maxWalkedEntries bounds the walk itself, so capture costs a bounded
	// amount of work no matter what the agent left behind.
	maxWalkedEntries = 20000
)

// runState is what a supervised run needs to know about the directory it ran
// in, so that the same goroutine that owns the task's state can also decide
// what the task produced.
type runState struct {
	workDir string
	before  dirSnapshot
}

// fileFingerprint is enough to answer "did this file change during the run?".
// Content hashing would be exact and would also mean reading every byte of
// every file in the workspace twice per task. The case it cannot see is a file
// rewritten to exactly its old length within the filesystem's timestamp
// granularity, which stays listed with its previous size rather than vanishing
// from the listing.
type fileFingerprint struct {
	size    int64
	modTime time.Time
}

type dirSnapshot map[string]fileFingerprint

// snapshotDir records the state of root before a run. A directory that cannot
// be read yields an empty snapshot rather than an error: the consequence is
// that more files look new, which is a reporting inaccuracy, not a reason to
// refuse to run the task.
func snapshotDir(root string) dirSnapshot {
	snap := make(dirSnapshot)
	if root == "" {
		return snap
	}
	walkArtifacts(root, func(rel string, info fs.FileInfo) bool {
		snap[rel] = fileFingerprint{size: info.Size(), modTime: info.ModTime()}
		return true
	})
	return snap
}

// changed returns the paths under root that are new or modified since the
// snapshot, newest first, and reports whether the list was truncated.
func (before dirSnapshot) changed(root string) (paths []string, truncated bool) {
	type entry struct {
		path string
		mod  time.Time
	}
	var found []entry
	walked := walkArtifacts(root, func(rel string, info fs.FileInfo) bool {
		if was, ok := before[rel]; ok && was.size == info.Size() && was.modTime.Equal(info.ModTime()) {
			return true
		}
		found = append(found, entry{path: rel, mod: info.ModTime()})
		return true
	})
	sort.Slice(found, func(i, j int) bool {
		if !found[i].mod.Equal(found[j].mod) {
			return found[i].mod.After(found[j].mod)
		}
		return found[i].path < found[j].path
	})
	if len(found) > maxCapturedArtifacts {
		found = found[:maxCapturedArtifacts]
		truncated = true
	}
	for _, e := range found {
		paths = append(paths, e.path)
	}
	return paths, truncated || walked >= maxWalkedEntries
}

// walkArtifacts visits every regular file under root that is eligible to be an
// artifact, calling fn with its slash-separated path relative to root.
//
// Two exclusions are load-bearing rather than cosmetic. Symlinks are never
// followed and never reported: an agent can be persuaded to write one, and a
// symlink to /etc/passwd that the server then happily serves is precisely the
// vulnerability the Files chapter warns about. Dot-directories are skipped
// because .git alone would bury a report's real output under thousands of
// object files.
func walkArtifacts(root string, fn func(rel string, info fs.FileInfo) bool) int {
	visited := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is skipped, not fatal: the rest of the
			// directory is still a useful answer.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxWalkedEntries {
			return fs.SkipAll
		}
		// Type() reports the symlink itself, not its target, which is the
		// point: this refuses the link without ever resolving it.
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if !fn(filepath.ToSlash(rel), info) {
			return fs.SkipAll
		}
		return nil
	})
	return visited
}

// artifactID derives an opaque, stable id for one file in one container.
//
// Opaque on purpose (Files §5): an id that embodied a path would make every
// download a path-joining exercise against client-supplied input. Stable on
// purpose too — a file rewritten by a later task in the same session keeps its
// id, so a client's saved reference still resolves and the session's listing
// does not grow a second entry for the same file.
func artifactID(containerID, path string) string {
	sum := sha256.Sum256([]byte(containerID + "\x00" + path))
	return "file_" + hex.EncodeToString(sum[:])[:32]
}

// captureArtifacts records what the run produced. It is called by the
// supervisor, on the goroutine that owns the task, before the terminal event is
// built — so the response that reports the task complete already cites its
// files.
func (s *TaskService) captureArtifacts(ctx context.Context, task *domain.Task, rs *runState) {
	if rs == nil || rs.workDir == "" || task.SessionID == "" {
		return
	}
	containerID := domain.ContainerIDFor(task.SessionID)
	if containerID == "" {
		return
	}
	paths, truncated := rs.before.changed(rs.workDir)
	if truncated {
		// A silently capped list reads as "these are all the files", which is
		// worse than a shorter list the operator knows is short.
		s.log.Warn("artifact capture truncated", "task_id", task.ID,
			"captured", len(paths), "limit", maxCapturedArtifacts)
	}
	now := time.Now().UTC()
	for _, rel := range paths {
		info, err := os.Stat(filepath.Join(rs.workDir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		a := domain.Artifact{
			File: uhp.File{
				ID:          artifactID(containerID, rel),
				Object:      "file",
				ContainerID: containerID,
				// The path within the container, not the base name: two files
				// called report.md in different directories are different
				// files.
				Filename:  rel,
				Bytes:     info.Size(),
				CreatedAt: now.Unix(),
			},
			MimeType: detectMimeType(rs.workDir, rel),
		}
		if err := s.store.AppendArtifact(ctx, task.ID, a); err != nil {
			s.log.Error("persist artifact", "error", err, "task_id", task.ID)
			continue
		}
		task.Artifacts = append(task.Artifacts, a)
	}
}

// detectMimeType prefers the extension and falls back to content sniffing.
//
// The type is advisory metadata for a client's UI. It is never a licence for a
// browser to decide what an artifact is: downloads are served with `nosniff`
// exactly because this value is derived from attacker-influenceable content.
func detectMimeType(root, rel string) string {
	if t := mime.TypeByExtension(filepath.Ext(rel)); t != "" {
		return t
	}
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "application/octet-stream"
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 512)
	n, err := f.Read(head)
	if err != nil && err != io.EOF {
		return "application/octet-stream"
	}
	return http.DetectContentType(head[:n])
}

// citeArtifacts attaches container_file_citation annotations for everything the
// task produced to its assistant message (Files §2.1).
//
// Artifacts are additive to the message, never a replacement for it: a client
// that renders only text still shows a correct answer, and one that reads
// annotations can offer the file.
func (s *TaskService) citeArtifacts(task *domain.Task) {
	if len(task.Artifacts) == 0 {
		return
	}
	_, item := task.MessageItem()
	if item == nil || len(item.Content) == 0 {
		return
	}
	seen := make(map[string]bool, len(item.Content[0].Annotations))
	for _, an := range item.Content[0].Annotations {
		seen[an.FileID] = true
	}
	for _, a := range task.Artifacts {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		item.Content[0].Annotations = append(item.Content[0].Annotations, a.Cite(s.publicBaseURL))
	}
}

// SessionFiles answers GET /v1/sessions/{id}/files: every artifact of the
// session, including those from earlier tasks.
//
// Files §2.2 is explicit that restricting the list to the most recent task
// would make a multi-step session's earlier outputs unreachable. The union is
// deduplicated by id, so a file a later task rewrote appears once, with its
// most recent size.
func (s *TaskService) SessionFiles(ctx context.Context, sessionID string) ([]domain.Artifact, error) {
	tasks, err := s.sessionTasks(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]domain.Artifact)
	order := make([]string, 0, 8)
	for _, t := range tasks {
		for _, a := range t.Artifacts {
			if _, ok := byID[a.ID]; !ok {
				order = append(order, a.ID)
			}
			byID[a.ID] = a
		}
	}
	out := make([]domain.Artifact, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

// ErrArtifactNotFound is returned for an unknown container, an unknown file id,
// or a recorded artifact whose bytes have since gone.
var ErrArtifactNotFound = fmt.Errorf("service: artifact not found")

// OpenArtifact resolves a container/file pair to its metadata and its bytes.
//
// Files §5 requires file access to be scoped to the owning principal. This
// server has exactly one: every configured API key is equivalent and no
// credential carries an identity, so requiring a key is the whole of that
// scoping. Multi-tenancy would mean a principal on the credential first, and
// then a filter here — not a change that can be faked at this layer.
//
// Resolution is a lookup, never a path join of client input: the file id must
// match an artifact this server recorded, and only then is the server's own
// stored path used to open a file. The final containment check is belt and
// braces — it costs nothing, and it is the difference between "we believe our
// ids are opaque" and "a traversal cannot escape even if they are not".
func (s *TaskService) OpenArtifact(ctx context.Context, containerID, fileID string) (domain.Artifact, *os.File, error) {
	sessionID, ok := domain.SessionIDFromContainer(containerID)
	if !ok {
		return domain.Artifact{}, nil, ErrArtifactNotFound
	}
	files, err := s.SessionFiles(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrStorage) {
			// A store that could not be read is not a container that is not
			// there, and this is the one arm where saying so matters: every
			// other return below is a genuine "no such file". Answering
			// file_not_found for a failed read tells a client its id was wrong
			// and never to ask again, for the one condition where asking again
			// is exactly what works.
			return domain.Artifact{}, nil, err
		}
		// A container whose session is gone is indistinguishable from one that
		// never existed, which is what Files §5 asks for: deleting a session
		// MUST make its artifacts unreachable.
		return domain.Artifact{}, nil, ErrArtifactNotFound
	}
	var found *domain.Artifact
	for i := range files {
		if files[i].ID == fileID {
			found = &files[i]
			break
		}
	}
	if found == nil {
		return domain.Artifact{}, nil, ErrArtifactNotFound
	}

	root, err := s.workspace.sessionDir(sessionID)
	if err != nil || root == "" {
		return domain.Artifact{}, nil, ErrArtifactNotFound
	}
	full := filepath.Join(root, filepath.FromSlash(found.Filename))
	if !withinDir(root, full) {
		s.log.Error("refusing to serve an artifact from outside its container", "container_id", containerID)
		return domain.Artifact{}, nil, ErrArtifactNotFound
	}
	f, err := os.Open(full)
	if err != nil {
		return domain.Artifact{}, nil, ErrArtifactNotFound
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return domain.Artifact{}, nil, ErrArtifactNotFound
	}
	// Serve the size the bytes actually have now, not the size they had when
	// the artifact was recorded.
	a := *found
	a.Bytes = info.Size()
	return a, f, nil
}

// withinDir reports whether path is inside root after both are resolved.
func withinDir(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
