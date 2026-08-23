package service

import (
	"fmt"

	"github.com/aenawi/uhp-go/internal/domain"
)

// CapabilityError is a request for something the harness does not advertise.
//
// A harness object carries a `capabilities` list, and discovery hands that list
// to every client before it sends anything. That makes it a promise, and the
// only two honest ways to treat a promise are to keep it or to refuse the
// request that takes it up. The third option — accept the request and deliver
// something else — is the one this type exists to remove: a continuation that
// quietly starts a new conversation, or a cancel that quietly stops nothing,
// both answer 200 and leave the client believing the opposite of what happened.
//
// It carries the capability rather than only a message because Errors §1 wants
// structured context, and because a client told "unsupported" without being
// told what is unsupported has nothing to check against the discovery document
// it already holds.
type CapabilityError struct {
	HarnessID  string
	Capability domain.Capability

	// Consequence names what the client asked for, in the client's own terms,
	// so the message says what will not happen rather than only which flag is
	// missing.
	Consequence string
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf("service: harness %q does not support %q, so %s",
		e.HarnessID, e.Capability, e.Consequence)
}

// requireCapability refuses a request the harness has not advertised support
// for.
//
// The check reads the same list a client can read, so a client that consults
// discovery first never sees this error at all, and one that does not is told
// exactly which capability it assumed.
func requireCapability(h domain.Harness, c domain.Capability, consequence string) error {
	if h.HasCapability(c) {
		return nil
	}
	return &CapabilityError{HarnessID: h.ID, Capability: c, Consequence: consequence}
}

// withRouterCapabilities completes a harness object with the capabilities this
// router delivers rather than the harness underneath it.
//
// `files_in` and `files_out` are both in that position, and both are computed
// here rather than declared anywhere. materializeAttachments writes a task's
// attachments into the session working directory before the run, and
// captureArtifacts diffs that directory afterwards; neither asks an adapter
// anything, so every harness on this server does file input and file output
// identically. The declarations used to say otherwise — `pi` claimed neither
// and `grok-cli` claimed no output, while both did both — which is the same
// class of bug as a capability nobody enforced.
//
// They are also the deployment's answer and not only the router's: both need a
// per-session working directory, so a server started without `UHP_WORKSPACE`
// delivers neither. Recomputing them from that one fact is what keeps the
// per-harness list and the discovery document's `files_input`/`files_output`
// from contradicting each other — a harness cannot advertise `files_in` on a
// server that answers `501` to every attachment.
//
// Recomputed, not merged: an adapter's claim is dropped rather than honoured,
// because the claim was never the thing that decided the behaviour.
//
// Every path that hands a client a harness object goes through here —
// ListHarnesses, GetHarness, and harnessView for the managed ones — so that
// two endpoints cannot describe the same harness differently.
func (s *TaskService) withRouterCapabilities(h domain.Harness) domain.Harness {
	// A fresh slice: Managed.Info hands back the base adapter's own capability
	// list, and appending to it could write into the array the base is still
	// serving to everyone else.
	caps := make([]domain.Capability, 0, len(h.Capabilities)+2)
	for _, c := range h.Capabilities {
		if c != domain.CapFilesIn && c != domain.CapFilesOut {
			caps = append(caps, c)
		}
	}
	if s.FilesEnabled() {
		caps = append(caps, domain.CapFilesIn, domain.CapFilesOut)
	}
	h.Capabilities = caps
	return h
}

// Consequences, phrased for the client rather than for the adapter.
const (
	whyNoSessions = "this conversation cannot be continued on it; " +
		"start a new one, or use a harness that advertises `sessions`"
	whyNoCancellation = "work already running on it cannot be stopped"
)
