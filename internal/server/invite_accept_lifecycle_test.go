// file: internal/server/invite_accept_lifecycle_test.go
// version: 0.1.0
// guid: f1bdaf03-5edb-423f-be57-a81c7dc949ea
// last-edited: 2026-11-22

package server

import (
	"context"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccesspolicy"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stuckUsersView is core's identity view as the guest-policy concern reaches it
// synchronously (coreAccountStands, after the grant is stored): every read counts
// itself, then panics or blocks until released. It is the seam that makes the guest
// policy concern misbehave in the middle of the InviteAccepted callback.
type stuckUsersView struct {
	coreauth.Identities
	reads   *atomic.Int32
	panicky bool
	release chan struct{}
}

func (v stuckUsersView) Users(ctx context.Context, _ coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	v.reads.Add(1)
	if v.panicky {
		panic("the guest policy concern blew up")
	}
	select {
	case <-v.release:
	case <-ctx.Done():
	}
	return func(yield func(*coreauth.User, error) bool) {}
}

func acceptEvent(t *testing.T) (coreauth.InviteAccepted, time.Time) {
	t.Helper()
	at := time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC)
	return coreauth.InviteAccepted{
		InviteID: "inv-sel", UserID: gUserID, Username: "selina.kyle", AcceptedAt: at, CreatedBy: "u-bruce",
		Email: gSelina, BindEmailOnAccept: true, CreatorHeldAdmin: true,
	}, at
}

func listedAccepts(t *testing.T, h guestHook) []database.InviteAccept {
	t.Helper()
	rows, err := h.store.ListInviteAcceptsSince(time.Time{})
	require.NoError(t, err)
	return rows
}

// The accept row is the accept's record, so it is written FIRST and on its own
// recover (AccountLifecycle): a guest-policy concern that panics in the middle of the
// callback does not cost the accept its row, and the callback does not panic.
func TestAccountLifecycle_AcceptRowSurvivesAPanicInTheGuestPolicyConcern(t *testing.T) {
	h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
	reads := new(atomic.Int32)
	h.srv.identity.Users = stuckUsersView{reads: reads, panicky: true}
	e, at := acceptEvent(t)

	require.NotPanics(t, func() { h.accept(t, e) })

	require.EqualValues(t, 1, reads.Load(), "the guest policy concern never reached its read, so this cell proves nothing about it")
	rows := listedAccepts(t, h)
	require.Len(t, rows, 1)
	assert.Equal(t, "inv-sel", rows[0].InviteID)
	assert.Equal(t, "selina.kyle", rows[0].Username)
	assert.True(t, rows[0].AcceptedAt.Equal(at), "accept time %v, the event's %v", rows[0].AcceptedAt, at)
}

// The row does not wait on the guest policy either: while that concern is still
// stuck inside its read, the row is already there. A record run behind the policy
// concern (or inside it) shows nothing until the policy returns.
func TestAccountLifecycle_AcceptRowIsWrittenBeforeTheGuestPolicyConcernReturns(t *testing.T) {
	h := newGuestHook(t, &fakeGuestPolicy{res: cfaccesspolicy.Added}, nil)
	reads := new(atomic.Int32)
	release := make(chan struct{})
	h.srv.identity.Users = stuckUsersView{reads: reads, release: release}
	e, _ := acceptEvent(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.accept(t, e)
	}()
	released := false
	unblock := func() {
		if !released {
			released = true
			close(release)
		}
	}
	t.Cleanup(func() { unblock(); <-done })

	require.Eventually(t, func() bool { return reads.Load() == 1 }, 5*time.Second, 5*time.Millisecond, "the guest policy concern never reached its read")
	require.Eventually(t, func() bool { return len(listedAccepts(t, h)) == 1 }, 2*time.Second, 5*time.Millisecond,
		"the accept row was not written while the guest policy concern was still running")
	select {
	case <-done:
		t.Fatal("the callback returned while the guest policy concern was supposed to be stuck")
	default:
	}
	unblock()
	<-done
}
