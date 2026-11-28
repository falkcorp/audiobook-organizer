// file: internal/usermerge/grant.go
// version: 1.0.0
// guid: 08d4f2b6-a173-4e59-8c30-b9e5a7d1f642
// last-edited: 2026-10-23

package usermerge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"
)

// GrantTTL is how long a minted grant stays redeemable. It spans enqueue to the
// operation's first line, not the merge itself.
const GrantTTL = 10 * time.Minute

// Grant is the server's record that one admin, through the merge route's own
// checks (the route guard, the owner gate, the active-admin check, the
// actor-is-not-the-absorbed-account check), asked for exactly one merge action.
//
// Why it exists. The merge runs as a background operation, and the generic
// POST /api/v1/operations/v2 enqueues any registered definition with any params,
// bypassing every check the dedicated route makes. The operation therefore
// refuses to write anything unless it redeems a grant the dedicated route
// minted, and the grant is bound to the actor, the two accounts, the preview's
// digest and the action. It is in memory on purpose (a restart drops every grant,
// so a resumed operation can never merge), single-use, and expires.
type Grant struct {
	ActorID    string
	Action     Action
	SurvivorID string
	AbsorbedID string
	// Digest is the preview digest (apply only).
	Digest string
	// JournalID names the journal (resume and undo only).
	JournalID                string
	AcknowledgeOwnerIdentity bool
	// Principal is the signed-in admin's own principal, as the request that
	// minted this grant held it (core's PrincipalFrom on the request ctx), and
	// only for an apply under identity. It is what lets the background
	// operation act as that admin: Apply re-validates it through core and core
	// re-checks every rule against it. It lives in this process's memory for at
	// most GrantTTL and never leaves it: the grant has no JSON form, and every
	// formatting path below leaves the principal out (it names a credential).
	Principal *coreauth.Principal

	IssuedAt  time.Time
	ExpiresAt time.Time
}

// String is the grant without its principal, for %v and %+v.
func (g Grant) String() string {
	return fmt.Sprintf("usermerge.Grant{actor=%s action=%s survivor=%s absorbed=%s journal=%s principal=%t expires=%s}",
		g.ActorID, g.Action, g.SurvivorID, g.AbsorbedID, g.JournalID, g.Principal != nil, g.ExpiresAt.Format(time.RFC3339))
}

// GoString is String, for %#v.
func (g Grant) GoString() string { return g.String() }

// LogValue keeps the principal out of a structured log record.
func (g Grant) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("actor", g.ActorID), slog.String("action", string(g.Action)),
		slog.String("survivor", g.SurvivorID), slog.String("absorbed", g.AbsorbedID),
		slog.String("journal", g.JournalID), slog.Bool("principal", g.Principal != nil),
	)
}

// Action is what a grant permits.
type Action string

// Merge actions.
const (
	ActionApply  Action = "apply"
	ActionResume Action = "resume"
	ActionUndo   Action = "undo"
)

// Valid reports whether a is a known action.
func (a Action) Valid() bool { return a == ActionApply || a == ActionResume || a == ActionUndo }

// Grants holds the grants minted by this process.
type Grants struct {
	mu  sync.Mutex
	m   map[string]Grant
	now func() time.Time
}

// NewGrants returns an empty grant store.
func NewGrants() *Grants { return &Grants{m: map[string]Grant{}, now: time.Now} }

// DefaultGrants is the process's grant store: the merge handler mints into it
// and the maintenance.user-merge operation takes from it.
var DefaultGrants = NewGrants()

// Issue mints a grant and returns its token (128 random bits, hex).
func (g *Grants) Issue(gr Grant) (string, error) {
	if gr.ActorID == "" || !gr.Action.Valid() {
		return "", errors.New("usermerge: a grant needs an actor and an action")
	}
	switch gr.Action {
	case ActionApply:
		if gr.SurvivorID == "" || gr.AbsorbedID == "" || gr.Digest == "" {
			return "", errors.New("usermerge: an apply grant needs both accounts and the preview digest")
		}
	default:
		if gr.JournalID == "" {
			return "", errors.New("usermerge: a resume or undo grant needs the journal id")
		}
	}
	if gr.Principal != nil && (gr.Principal.User == nil || string(gr.Principal.User.ID) != gr.ActorID) {
		return "", errors.New("usermerge: a grant's principal must be the actor's own")
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("usermerge: grant token: %w", err)
	}
	tok := hex.EncodeToString(b[:])
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for k, v := range g.m {
		if now.After(v.ExpiresAt) {
			delete(g.m, k)
		}
	}
	gr.IssuedAt, gr.ExpiresAt = now, now.Add(GrantTTL)
	g.m[tok] = gr
	return tok, nil
}

// Take consumes the grant named by tok. It is removed whether or not it is still
// valid, so no token is ever usable twice.
func (g *Grants) Take(tok string) (Grant, bool) {
	if tok == "" {
		return Grant{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.m[tok]
	delete(g.m, tok)
	if !ok || g.now().After(gr.ExpiresAt) {
		return Grant{}, false
	}
	return gr, true
}

// Redeem takes tok and checks it was minted for this action, for the user the
// redeeming operation is enqueued for (actorID, the op row's actor), and for
// want's fields. It returns the grant, or the reason it is refused.
func (g *Grants) Redeem(tok string, actorID string, want Grant) (Grant, error) {
	gr, ok := g.Take(tok)
	switch {
	case !ok:
		return Grant{}, errors.New("no valid merge grant: a merge runs only from the Users page's own request (a grant is single-use, expires, and does not survive a restart)")
	case gr.Action != want.Action:
		return Grant{}, errors.New("the merge grant is for a different action")
	case actorID == "" || actorID != gr.ActorID:
		return Grant{}, errors.New("the merge grant was minted for another user than the one this operation runs for")
	case gr.SurvivorID != want.SurvivorID || gr.AbsorbedID != want.AbsorbedID || gr.Digest != want.Digest || gr.JournalID != want.JournalID:
		return Grant{}, errors.New("the merge grant names a different merge than the operation's parameters")
	case gr.AcknowledgeOwnerIdentity != want.AcknowledgeOwnerIdentity:
		return Grant{}, errors.New("the merge grant's owner-identity acknowledgement differs from the operation's")
	}
	return gr, nil
}
