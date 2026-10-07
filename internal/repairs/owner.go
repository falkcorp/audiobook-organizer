// file: internal/repairs/owner.go
// version: 1.1.0
// guid: 7a3d5f81-2c94-4e0b-b6a7-1f8e3c2d9b54
// last-edited: 2026-10-07

package repairs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// OWNER APPLY (owner decision 2026-10-06). Some rows a fixer lists as
// manual-only are ones the owner may apply himself, by clicking Apply on the
// row: the fragment-consolidation fixer's iTunes-tracked library copies proven
// by content hash, written as database rows only. The fixer marks such a row
// at plan time (Row.OwnerApplicable); the row stays skipped, so no bulk
// selection, scheduled run or plain apply ever writes it.
//
// The only way in is a grant: the Repairs HTTP handler, for a request that
// carries a verified Cloudflare Access JWT for the configured owner email
// (auth.OwnerProofWhyNot; owner decision 2026-10-07 — a password, SSO,
// temp-login or invite session is not enough), mints a one-shot grant in this
// process's memory for one row of one plan, and enqueues repairs.apply naming it. The
// apply op takes (consumes) the grant before any write. Op params are never
// trusted on their own: params written by hand (POST /operations/v2), a
// retry, or a resume after a restart find no grant, and the row is refused
// (OutcomeOwnerRefused).

// OutcomeOwnerRefused: an owner row was requested without a valid owner
// grant (none, used, expired, another plan or row), on a resumed run, or on
// a row the plan did not mark owner-applicable.
const OutcomeOwnerRefused = "owner_apply_refused"

// OwnerAuthMethod is the only auth method an owner grant may carry
// (auth.MethodCFAccess; a string here so this package does not import auth).
const OwnerAuthMethod = "cf_access"

// OwnerGrantTTL is how long a minted grant waits for its apply op to start.
// An apply queues behind at most the one running apply (ConcurrencyKey) and
// its scan stand-down wait, so a grant that outlives this is stale.
var OwnerGrantTTL = 2 * time.Hour

// OwnerGrant is one owner approval: who approved which rows of which plan.
type OwnerGrant struct {
	UserID     string
	AuthMethod string
	// AccessEmail is the verified Cloudflare Access email that proved the
	// owner. Issue refuses a grant without it, or with any AuthMethod but
	// cf_access, so every exception an owner grant unlocks (the owner row
	// itself, and its OwnerITunesDatabaseOnly books) is reachable only from
	// an Access-proven request, whichever caller mints it.
	AccessEmail string
	FixerID     string
	PlanOpID    string
	RowIDs      []string
	IssuedAt    time.Time
	ExpiresAt   time.Time
}

// OwnerGrants holds the grants minted by this process. In memory on
// purpose: a restart drops every grant, so a resumed apply can never apply
// an owner row.
type OwnerGrants struct {
	mu  sync.Mutex
	m   map[string]OwnerGrant
	now func() time.Time
}

// NewOwnerGrants returns an empty grant store.
func NewOwnerGrants() *OwnerGrants {
	return &OwnerGrants{m: map[string]OwnerGrant{}, now: time.Now}
}

// DefaultOwnerGrants is the process's grant store: the Repairs handler mints
// into it and repairs.apply takes from it.
var DefaultOwnerGrants = NewOwnerGrants()

// Issue mints a grant and returns its token (128 random bits, hex).
func (g *OwnerGrants) Issue(gr OwnerGrant) (string, error) {
	if gr.UserID == "" || gr.FixerID == "" || gr.PlanOpID == "" || len(gr.RowIDs) == 0 {
		return "", errors.New("repairs: owner grant needs a user, fixer, plan and rows")
	}
	if gr.AuthMethod != OwnerAuthMethod || gr.AccessEmail == "" {
		return "", errors.New("repairs: an owner grant is minted only for a verified Cloudflare Access owner sign-in")
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("repairs: owner grant token: %w", err)
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
	gr.RowIDs = normalizeIDs(gr.RowIDs)
	gr.IssuedAt, gr.ExpiresAt = now, now.Add(OwnerGrantTTL)
	g.m[tok] = gr
	return tok, nil
}

// Take consumes the grant named by tok: it is removed whether or not it is
// still valid, so no token is ever usable twice.
func (g *OwnerGrants) Take(tok string) (OwnerGrant, bool) {
	if tok == "" {
		return OwnerGrant{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.m[tok]
	delete(g.m, tok)
	if !ok || g.now().After(gr.ExpiresAt) {
		return OwnerGrant{}, false
	}
	return gr, true
}

// OwnerApproval is a consumed grant as RunApply uses it (ApplyDeps.Owner).
type OwnerApproval struct {
	UserID     string
	AuthMethod string
	RowIDs     []string
	// Refused, when set, is why the requested owner rows may not run in owner
	// mode (no grant, a resume, a mismatch); every one of them is then
	// reported OutcomeOwnerRefused with this reason.
	Refused string
}

func (a *OwnerApproval) has(id string) bool {
	return a != nil && a.Refused == "" && slices.Contains(a.RowIDs, id)
}

// ResolveOwnerApproval turns an apply's owner params into its approval. It
// takes the grant from grants (consuming it) and checks it names exactly
// this fixer, plan and rows. nil when the params request no owner rows. A
// resumed run (p.Resume set) is refused whatever its grant: owner rows are
// only ever applied by the run the owner's click started.
func ResolveOwnerApproval(grants *OwnerGrants, p ApplyParams) *OwnerApproval {
	ids := normalizeIDs(p.OwnerApplyRowIDs)
	if len(ids) == 0 {
		return nil
	}
	refuse := func(why string) *OwnerApproval { return &OwnerApproval{RowIDs: ids, Refused: why} }
	if p.Resume != nil {
		// Consume it anyway so a resumed run leaves nothing behind.
		if grants != nil {
			grants.Take(p.OwnerGrant)
		}
		return refuse("a resumed apply never applies owner rows; apply it again from Repairs")
	}
	if grants == nil {
		return refuse("no owner grant store")
	}
	gr, ok := grants.Take(p.OwnerGrant)
	switch {
	case !ok:
		return refuse("no valid owner grant (owner rows are applied only by the owner's own click in Repairs, signed in through Cloudflare Access; a grant is single-use, expires, and does not survive a restart)")
	case gr.FixerID != p.FixerID || gr.PlanOpID != p.PlanOpID:
		return refuse("the owner grant is for another fixer or plan")
	case !slices.Equal(gr.RowIDs, ids):
		return refuse("the owner grant names other rows")
	case gr.AuthMethod != OwnerAuthMethod || gr.AccessEmail == "":
		return refuse("the owner grant was not minted for a verified Cloudflare Access owner sign-in")
	}
	return &OwnerApproval{UserID: gr.UserID, AuthMethod: gr.AuthMethod, RowIDs: ids}
}

type ownerCtxKey struct{}

// OwnerApplyContext is what a fixer's Replan and Apply see for a row the
// owner approved (OwnerApplyFrom).
type OwnerApplyContext struct {
	RowID      string
	UserID     string
	AuthMethod string
}

// WithOwnerApply marks ctx as the owner's apply of one row.
func WithOwnerApply(ctx context.Context, o OwnerApplyContext) context.Context {
	return context.WithValue(ctx, ownerCtxKey{}, o)
}

// OwnerApplyFrom reports the owner approval of the row being applied, if
// any. A fixer relaxes nothing unless it is set AND names the row it is
// writing.
func OwnerApplyFrom(ctx context.Context) (OwnerApplyContext, bool) {
	if ctx == nil {
		return OwnerApplyContext{}, false
	}
	o, ok := ctx.Value(ownerCtxKey{}).(OwnerApplyContext)
	return o, ok && o.RowID != "" && o.UserID != ""
}
