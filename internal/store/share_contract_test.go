package store

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/uhp"
)

// The share half of the store contract, run against both engines for the reason
// the rest of the suite is: a rule asserted against one implementation is a
// description of that implementation, not a contract.
//
// Every test here is about a security property. A share id is an anonymous
// bearer capability, so "the store forgot to delete it" and "the store handed
// the wrong session back" are not storage bugs — they are a stranger reading a
// conversation.

func shareSession(id string) *domain.Session {
	return &domain.Session{Session: uhp.Session{
		ID: id, Object: "session", HarnessID: "chrn_echo", CreatedAt: storeEpoch.Unix(),
	}}
}

func TestStoreResolvesAShareToItsSession(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		seed(t, s, shareSession("sess_a"))
		seed(t, s, shareSession("sess_b"))

		a := &domain.Share{ID: "shr_a", SessionID: "sess_a", CreatedAt: storeEpoch.Unix()}
		b := &domain.Share{ID: "shr_b", SessionID: "sess_b", CreatedAt: storeEpoch.Unix() + 1}
		if _, _, err := s.CreateShare(ctx, a); err != nil {
			t.Fatalf("create share a: %v", err)
		}
		if _, _, err := s.CreateShare(ctx, b); err != nil {
			t.Fatalf("create share b: %v", err)
		}

		got, found, err := s.GetShare(ctx, "shr_a")
		if err != nil || !found {
			t.Fatalf("get share: found=%v err=%v", found, err)
		}
		if got.SessionID != "sess_a" || got.CreatedAt != storeEpoch.Unix() {
			t.Fatalf("share = %+v", got)
		}

		// The other direction, which is what an idempotent POST reads.
		bySession, found, err := s.GetSessionShare(ctx, "sess_b")
		if err != nil || !found {
			t.Fatalf("get session share: found=%v err=%v", found, err)
		}
		if bySession.ID != "shr_b" {
			t.Fatalf("session share = %+v", bySession)
		}
	})
}

// An unknown id is found=false and not an error, for the reason GetSession
// gives: the transport turns absence into 404 and failure into 500, and folding
// the two together picks one of them for both.
func TestStoreReportsAnUnknownShareAsAbsent(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		if _, found, err := s.GetShare(ctx, "shr_nope"); found || err != nil {
			t.Fatalf("get unknown share: found=%v err=%v", found, err)
		}
		if _, found, err := s.GetSessionShare(ctx, "sess_nope"); found || err != nil {
			t.Fatalf("get share of unknown session: found=%v err=%v", found, err)
		}
	})
}

// A session has at most one share, and CreateShare is get-or-create: the second
// call reports the first one rather than replacing it.
//
// Replacing would be the other plausible reading and it is the wrong one. The
// client that minted the first id was told about that id and revokes that id;
// a second POST silently displacing it would leave whoever holds the first link
// looking at a 404 that nobody asked for.
func TestStoreKeepsOneSharePerSession(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		seed(t, s, shareSession("sess_a"))

		first, found, err := s.CreateShare(ctx, &domain.Share{ID: "shr_first", SessionID: "sess_a", CreatedAt: 1})
		if err != nil || !found {
			t.Fatalf("create first: found=%v err=%v", found, err)
		}
		if first.ID != "shr_first" {
			t.Fatalf("first create reported %+v", first)
		}

		second, found, err := s.CreateShare(ctx, &domain.Share{ID: "shr_second", SessionID: "sess_a", CreatedAt: 2})
		if err != nil || !found {
			t.Fatalf("create second: found=%v err=%v", found, err)
		}
		if second.ID != "shr_first" || second.CreatedAt != 1 {
			t.Fatalf("a second create did not report the existing share: %+v", second)
		}

		if _, found, err := s.GetShare(ctx, "shr_second"); found || err != nil {
			t.Fatalf("the losing id resolves: found=%v err=%v", found, err)
		}
		if _, found, err := s.GetShare(ctx, "shr_first"); !found || err != nil {
			t.Fatalf("the first id stopped resolving: found=%v err=%v", found, err)
		}
	})
}

// The reason CreateShare is get-or-create rather than read-then-write in the
// service: concurrent shares of one session must agree on one id.
//
// A read followed by a write passes every sequential test above and fails this
// one — both callers read "not shared", both mint, and one client walks away
// with a 200 carrying an id that is already dead.
func TestStoreMintsOneShareUnderConcurrentCreates(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		seed(t, s, shareSession("sess_a"))

		const callers = 8
		ids := make([]string, callers)
		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sh, found, err := s.CreateShare(ctx, &domain.Share{
					ID:        fmt.Sprintf("shr_%d", i),
					SessionID: "sess_a",
					CreatedAt: int64(i),
				})
				if err != nil || !found {
					t.Errorf("create %d: found=%v err=%v", i, found, err)
					return
				}
				ids[i] = sh.ID
			}()
		}
		wg.Wait()

		winner := ids[0]
		for i, id := range ids {
			if id != winner {
				t.Fatalf("caller %d was handed %q while caller 0 was handed %q", i, id, winner)
			}
		}
		// And exactly that one resolves.
		if _, found, err := s.GetShare(ctx, winner); !found || err != nil {
			t.Fatalf("the id every caller was handed does not resolve: found=%v err=%v", found, err)
		}
		for i := range callers {
			id := fmt.Sprintf("shr_%d", i)
			if id == winner {
				continue
			}
			if _, found, _ := s.GetShare(ctx, id); found {
				t.Fatalf("%s resolves as well as the winner", id)
			}
		}
	})
}

func TestStoreRevokesAShare(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		seed(t, s, shareSession("sess_a"))
		if _, _, err := s.CreateShare(ctx, &domain.Share{ID: "shr_a", SessionID: "sess_a"}); err != nil {
			t.Fatalf("create: %v", err)
		}

		revoked, found, err := s.DeleteSessionShare(ctx, "sess_a")
		if err != nil || !found {
			t.Fatalf("revoke: found=%v err=%v", found, err)
		}
		// The id has to be the one this call removed, so a caller can report
		// which link it withdrew without reading first — and so a revoke racing
		// a re-share cannot name the wrong one.
		if revoked != "shr_a" {
			t.Errorf("revoke reported %q, want shr_a", revoked)
		}
		if _, found, err := s.GetShare(ctx, "shr_a"); found || err != nil {
			t.Fatalf("the revoked id still resolves: found=%v err=%v", found, err)
		}

		// A second revoke deleted nothing and says so, for the reason a second
		// DELETE of a session is a 404 rather than a second success.
		if id, found, err := s.DeleteSessionShare(ctx, "sess_a"); found || err != nil || id != "" {
			t.Fatalf("second revoke: id=%q found=%v err=%v", id, found, err)
		}
	})
}

// Deleting the trace revokes the share, and this is the one in the suite that
// would be a live vulnerability if it regressed: a share whose session is gone
// is an id that outlived the thing it was a capability for. Sessions §6 makes
// a deleted session's files unreachable, and a share that survived would be the
// route back to them.
func TestStoreRevokesTheShareWhenTheSessionIsDeleted(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		seed(t, s, shareSession("sess_a"))
		seed(t, s, shareSession("sess_b"))
		if _, _, err := s.CreateShare(ctx, &domain.Share{ID: "shr_a", SessionID: "sess_a"}); err != nil {
			t.Fatalf("create a: %v", err)
		}
		if _, _, err := s.CreateShare(ctx, &domain.Share{ID: "shr_b", SessionID: "sess_b"}); err != nil {
			t.Fatalf("create b: %v", err)
		}

		if _, err := s.DeleteSession(ctx, "sess_a"); err != nil {
			t.Fatalf("delete session: %v", err)
		}

		if _, found, err := s.GetShare(ctx, "shr_a"); found || err != nil {
			t.Fatalf("the deleted session's share still resolves: found=%v err=%v", found, err)
		}
		// And only that session's.
		if _, found, err := s.GetShare(ctx, "shr_b"); !found || err != nil {
			t.Fatalf("an unrelated share was revoked: found=%v err=%v", found, err)
		}
	})
}

// The engines hand back records they own, so a caller that mutates what it was
// given must not be editing stored state — the rule the task and session reads
// already follow.
func TestStoreDoesNotShareShareRecordsWithItsCaller(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		seed(t, s, shareSession("sess_a"))
		if _, _, err := s.CreateShare(ctx, &domain.Share{ID: "shr_a", SessionID: "sess_a"}); err != nil {
			t.Fatalf("create: %v", err)
		}

		got, _, err := s.GetShare(ctx, "shr_a")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		got.SessionID = "sess_somebody_else"

		again, _, err := s.GetShare(ctx, "shr_a")
		if err != nil {
			t.Fatalf("get again: %v", err)
		}
		if again.SessionID != "sess_a" {
			t.Fatalf("stored share was mutated through the returned pointer: %+v", again)
		}
	})
}

// A share cannot be minted for a session that is not there.
//
// The two tables have no foreign key, so this is the only thing standing
// between a delete arriving mid-share and a row naming a conversation nobody
// can read — one no endpoint lists and no revoke reaches.
func TestStoreRefusesAShareForASessionItDoesNotHold(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		sh, found, err := s.CreateShare(ctx, &domain.Share{ID: "shr_x", SessionID: "sess_gone"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if found || sh != nil {
			t.Fatalf("a share was minted for a session that does not exist: %+v", sh)
		}
		if _, found, _ := s.GetShare(ctx, "shr_x"); found {
			t.Fatal("the refused share is in the store anyway")
		}
	})
}

// How many links this store is holding, which is the one question an operator
// asks about shares without holding one (#68).
//
// Turning UHP_SESSION_SHARING off suspends every link rather than revoking it,
// so a server that is not serving sharing may still be sitting on capabilities
// that resolve the moment the flag comes back. Saying so at startup needs a
// number, and it is a count rather than a listing because the id is the
// credential: an operator needs to know there are three, not what they are.
func TestStoreCountsTheSharesItHolds(t *testing.T) {
	eachStore(t, func(t *testing.T, s service.Store) {
		ctx := context.Background()
		if n, err := s.CountShares(ctx); n != 0 || err != nil {
			t.Fatalf("empty store counts %d shares (err=%v)", n, err)
		}

		seed(t, s, shareSession("sess_a"))
		seed(t, s, shareSession("sess_b"))
		if _, _, err := s.CreateShare(ctx, &domain.Share{ID: "shr_a", SessionID: "sess_a"}); err != nil {
			t.Fatalf("create share a: %v", err)
		}
		if _, _, err := s.CreateShare(ctx, &domain.Share{ID: "shr_b", SessionID: "sess_b"}); err != nil {
			t.Fatalf("create share b: %v", err)
		}
		if n, err := s.CountShares(ctx); n != 2 || err != nil {
			t.Fatalf("two shares counted %d (err=%v)", n, err)
		}

		// A second share of the same session is the existing one, so the count
		// does not move — the same thing that makes the endpoint idempotent.
		if _, _, err := s.CreateShare(ctx, &domain.Share{ID: "shr_again", SessionID: "sess_a"}); err != nil {
			t.Fatalf("create share again: %v", err)
		}
		if n, err := s.CountShares(ctx); n != 2 || err != nil {
			t.Fatalf("re-sharing a session counted %d (err=%v)", n, err)
		}

		// And a revoked share is gone from the count, as it is from every
		// other read: the number is what would resolve, not what was ever
		// minted.
		if _, _, err := s.DeleteSessionShare(ctx, "sess_a"); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if n, err := s.CountShares(ctx); n != 1 || err != nil {
			t.Fatalf("after a revoke the count is %d (err=%v)", n, err)
		}
	})
}
