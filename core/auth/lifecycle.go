// file: core/auth/lifecycle.go
// version: 0.1.0
// guid: 3b8d6f12-5c47-4e90-a1d3-7e2f9c04b856
// last-edited: 2026-11-22

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Lifecycle is the account events a module may observe (F4-08 B10r). It is a
// hook, not an event: the service calls it itself, once, AFTER the write that
// caused the event has committed, with ids and the minimal facts and nothing
// that authenticates (no password, hash, token or key bytes). It cannot veto
// the write, it holds no batch and no store handle (a network call must never
// sit inside a store batch), and a callback that panics is recovered and
// logged and fails nothing. A callback is called on the goroutine of the
// request that made the write, so it must return quickly: one that waits on
// a network answer starts its own goroutine (as OnInvalidated must not
// block). The ctx it gets carries the request's values (the actor) but not
// its cancellation: the write is durable, and a client that hung up must not
// cut a callback's own bookkeeping short.
//
// What fires it:
//
//   - InviteAccepted: AcceptInvite, when the account was created. A refused
//     (spent) invite fires nothing.
//   - StatusChanged: SetStatus, when the stored status really changed (a
//     disable and a re-enable; a call that finds the status already set writes
//     nothing and fires nothing). A user merge disabling its source, and a
//     restore re-applying a journal line, fire nothing, as HEAD's revoke did
//     not run for either.
//   - EmailChanged: UpdateProfile, when the account's address became a
//     different address by SameEmail (a change of spelling only fires
//     nothing: it is the address that was verified).
//
// Any callback may be nil.
type Lifecycle struct {
	InviteAccepted func(ctx context.Context, e InviteAccepted)
	StatusChanged  func(ctx context.Context, e StatusChanged)
	EmailChanged   func(ctx context.Context, e EmailChanged)
}

// InviteAccepted is what an accepted invite did. The payload is a struct so a
// later fact is a new field, not a new signature.
type InviteAccepted struct {
	// InviteID is the invite's public ID (InviteView.ID), the handle an
	// admin's list and the audit record name; never the token.
	InviteID string
	// UserID is the account the accept created.
	UserID UserID
	// CreatedBy is the invite's creator.
	CreatedBy UserID
	// Email is the address the creator named, normalised ("" when it named
	// none). The account holds it as written to its record: core does not
	// verify it on accept.
	Email string
	// CreatorHeldAdmin is true when, at the moment of the accept, the creator
	// was active and held core:admin: HEAD's InviteCreatorMayGrant, judged on
	// the creator row the accept guards in its own batch (a demotion after the
	// invite was made counts).
	CreatorHeldAdmin bool
	// DisplacedUserIDs are the accounts that held Email unverified and lost it
	// in the accept's own batch (displaceUnverifiedHolders). Empty while an
	// accept binds no address.
	DisplacedUserIDs []UserID
}

// StatusChanged is a committed change of a user's status.
type StatusChanged struct {
	UserID UserID
	// Email is the address the account held when it changed ("" when none),
	// so a callback that must act on it does not race a later e-mail change
	// by re-reading the user.
	Email string
	// From and To are the stored status before and after.
	From, To UserStatus
}

// EmailChanged is a committed change of a user's address to another address.
type EmailChanged struct {
	UserID UserID
	// From is the address the account held ("" when it had none); To is the
	// new one. Both are as stored.
	From, To string
}

// ErrLifecycleRegistered is RegisterLifecycle's refusal of a second call.
var ErrLifecycleRegistered = errors.New("auth: a lifecycle hook is already registered")

// RegisterLifecycle registers the one Lifecycle of the app. It is called once
// at boot, before requests are served (the kernel hands it to the module that
// declares Manifest.AuthLifecycle); a second call is refused
// (ErrLifecycleRegistered) and changes nothing, whether or not the first set
// any callback. The Lifecycle is copied: later edits of l reach nothing.
func (s *Service) RegisterLifecycle(l Lifecycle) error {
	c := l
	if !s.lifecycle.CompareAndSwap(nil, &c) {
		return ErrLifecycleRegistered
	}
	return nil
}

// after records fn to run once, after the attempt's batch has committed
// (commitInvalidatingN). A retried attempt starts from an empty list, so only
// the attempt that commits runs its callbacks.
func (v *invalidations) after(fn func(ctx context.Context)) {
	v.post = append(v.post, fn)
}

// queueInviteAccepted queues e for the registered hook, when it has one.
func (s *Service) queueInviteAccepted(v *invalidations, e InviteAccepted) {
	if l := s.lifecycle.Load(); l != nil && l.InviteAccepted != nil {
		v.after(func(ctx context.Context) {
			s.callHook(ctx, "invite_accepted", func(ctx context.Context) { l.InviteAccepted(ctx, e) })
		})
	}
}

// queueStatusChanged and queueEmailChanged are queueInviteAccepted for the
// other two callbacks.
func (s *Service) queueStatusChanged(v *invalidations, e StatusChanged) {
	if l := s.lifecycle.Load(); l != nil && l.StatusChanged != nil {
		v.after(func(ctx context.Context) {
			s.callHook(ctx, "status_changed", func(ctx context.Context) { l.StatusChanged(ctx, e) })
		})
	}
}

func (s *Service) queueEmailChanged(v *invalidations, e EmailChanged) {
	if l := s.lifecycle.Load(); l != nil && l.EmailChanged != nil {
		v.after(func(ctx context.Context) {
			s.callHook(ctx, "email_changed", func(ctx context.Context) { l.EmailChanged(ctx, e) })
		})
	}
}

// callHook runs one lifecycle callback. A panic is recovered and logged with
// its type and stack, never its value (a payload value must not reach the
// log): the write is durable and its caller must not see a failure for what
// an observer did. core/events' subscriber recovery is the model.
func (s *Service) callHook(ctx context.Context, name string, fn func(ctx context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			s.log.LogAttrs(ctx, slog.LevelError, "auth: lifecycle hook panicked (recovered; the write stands)",
				slog.String("hook", name), slog.String("panic_type", fmt.Sprintf("%T", r)), slog.String("stack", string(debug.Stack())))
		}
	}()
	fn(context.WithoutCancel(ctx))
}
