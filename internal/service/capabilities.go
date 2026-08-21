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

// Consequences, phrased for the client rather than for the adapter.
const (
	whyNoSessions = "this conversation cannot be continued on it; " +
		"start a new one, or use a harness that advertises `sessions`"
	whyNoCancellation = "work already running on it cannot be stopped"
)
