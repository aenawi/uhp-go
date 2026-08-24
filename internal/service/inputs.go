package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
	"github.com/google/uuid"
)

var (
	// ErrInvalidInput is a request the client can fix: a malformed item, an
	// undecodable data URL, an unknown file id.
	ErrInvalidInput = errors.New("service: invalid input")

	// ErrFilesUnsupported is a request this server is not configured to serve.
	// It is deliberately distinct from ErrInvalidInput: the request was
	// well-formed and no rewording of it will help.
	ErrFilesUnsupported = errors.New("service: file input requires a configured workspace")
)

// Attachment is one file a task carries as input (Files §1). Exactly one of
// Data and FileID is set: the first is an inline data URL the transport has
// already decoded, the second is a reference to an earlier upload.
type Attachment struct {
	Filename string
	Data     []byte
	FileID   string
}

// maxAttachmentNameRunes bounds a client-supplied filename. It is a name, not a
// path, and a 4 KB one is an attack rather than a file.
const maxAttachmentNameRunes = 128

// FilesEnabled reports whether this server can accept file input and produce
// artifacts. Both need a per-session working directory: without one, harnesses
// run in the router's own directory, where writing a client's file would be
// wrong and diffing for artifacts would be meaningless.
//
// Discovery reports this rather than a hardcoded true, because a capability
// that depends on configuration and is advertised unconditionally is exactly
// the kind of claim the conformance suite exists to falsify.
func (s *TaskService) FilesEnabled() bool { return s.workspace != "" }

// materializeAttachments writes a task's input files into the session's working
// directory and returns their absolute paths, in the order given.
//
// The harness meets its input as ordinary files in its own working directory,
// which is the one mechanism every CLI harness supports: none of the five has a
// generic "attach this file" flag, and inventing per-harness plumbing for
// something the filesystem already does would be five ways to get it wrong.
func (s *TaskService) materializeAttachments(ctx context.Context, workDir string, atts []Attachment) ([]string, error) {
	if len(atts) == 0 {
		return nil, nil
	}
	if workDir == "" {
		return nil, ErrFilesUnsupported
	}
	paths := make([]string, 0, len(atts))
	used := make(map[string]bool, len(atts))
	for i, att := range atts {
		data := att.Data
		name := att.Filename
		if att.FileID != "" {
			up, err := s.upload(ctx, att.FileID)
			if err != nil {
				return nil, err
			}
			data = up.Data
			if name == "" {
				name = up.Filename
			}
		}
		name = uniqueName(safeAttachmentName(name, i), used)
		full := filepath.Join(workDir, name)
		// Defence in depth: safeAttachmentName already reduced the name to a
		// single path element, so this can only fire if that changes.
		if !withinDir(workDir, full) {
			return nil, fmt.Errorf("%w: attachment %d has an unusable filename", ErrInvalidInput, i)
		}
		if err := os.WriteFile(full, data, 0o600); err != nil {
			return nil, fmt.Errorf("service: write input file: %w", err)
		}
		paths = append(paths, full)
	}
	return paths, nil
}

// safeAttachmentName reduces a client-supplied filename to one safe path
// element. The name reaches a filesystem, so nothing about it is trusted:
// directory components are dropped, dot names are refused, and anything outside
// a conservative set becomes an underscore.
func safeAttachmentName(name string, index int) string {
	name = filepath.Base(filepath.FromSlash(strings.ReplaceAll(name, "\\", "/")))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
		if b.Len() >= maxAttachmentNameRunes {
			break
		}
	}
	clean := strings.Trim(b.String(), ".")
	if clean == "" {
		return fmt.Sprintf("input-%d", index+1)
	}
	return clean
}

// uniqueName keeps two attachments called report.pdf from becoming one file.
func uniqueName(name string, used map[string]bool) string {
	candidate := name
	for n := 2; used[candidate]; n++ {
		ext := filepath.Ext(name)
		candidate = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), n, ext)
	}
	used[candidate] = true
	return candidate
}

// attachmentNote tells the harness where its input files are.
//
// Without it the files are present but unmentioned, and a model asked to "read
// the attached file" has no way to know a file was attached or what it is
// called. The note names the files relative to the working directory the
// harness is already running in.
func attachmentNote(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	return "\n\n[Attached files, present in the current working directory: " +
		strings.Join(names, ", ") + "]"
}

// StoreUpload answers POST /v1/files.
func (s *TaskService) StoreUpload(ctx context.Context, filename, mimeType string, data []byte) (uhpgo.Upload, error) {
	if s.uploads == nil {
		return uhpgo.Upload{}, ErrFilesUnsupported
	}
	up := uhpgo.Upload{
		File: uhp.File{
			ID:        "file_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			Object:    "file",
			Filename:  safeAttachmentName(filename, 0),
			CreatedAt: time.Now().UTC().Unix(),
		},
		MimeType: mimeType,
		Data:     data,
	}
	if err := s.uploads.Put(ctx, up); err != nil {
		return uhpgo.Upload{}, err
	}
	return up, nil
}

// upload resolves a file id a task referenced.
func (s *TaskService) upload(ctx context.Context, fileID string) (uhpgo.Upload, error) {
	if s.uploads == nil {
		return uhpgo.Upload{}, ErrFilesUnsupported
	}
	up, err := s.uploads.Get(ctx, fileID)
	if err != nil {
		return uhpgo.Upload{}, fmt.Errorf("%w: no uploaded file with id %q", ErrInvalidInput, fileID)
	}
	return up, nil
}
