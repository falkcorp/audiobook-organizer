// file: internal/usermerge/plan.go
// version: 1.2.2
// guid: c4a82f17-5d93-4e60-9b2a-71f3d0e86a45
// last-edited: 2026-10-23

package usermerge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Family names used in Plan.Counts, Conflict.Family and UserMergeOp.Family.
const (
	FamBookState        = "book_state" // ubs: + idx:ubs:status: + upos:, planned per book
	FamBookmarks        = "bookmarks"
	FamSyncAliasUse     = "sync_alias_use"
	FamPreferences      = "preferences"
	FamPlaybackProgress = "playback_progress"
	FamPlaybackEvents   = "playback_events"
	FamListenStats      = "listen_stats"
	FamOAuthIdentities  = "oauth_identities"
	FamAPIKeys          = "api_keys"
	FamPlaylists        = "playlists"
	FamCollections      = "collections"
	FamInvites          = "invites"
	FamInviteGrants     = "invite_grants"
)

// maxReportedConflicts caps Plan.Conflicts so a preview of a large library
// stays a page, not a megabyte. Plan.ConflictTotal is the real count.
const maxReportedConflicts = 100

// FamilyCount is what a merge does to one family of rows.
type FamilyCount struct {
	// Rows is how many rows (books, for book_state) the absorbed account has.
	Rows int `json:"rows"`
	// Conflicts is how many of them collide with a row the survivor has, and
	// were resolved by the family's rule (see Conflict.Outcome).
	Conflicts int `json:"conflicts"`
}

// Conflict is one collision and how the plan resolves it.
type Conflict struct {
	Family  string `json:"family"`
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
}

// AccountSummary is the part of a user row the preview shows.
type AccountSummary struct {
	ID               string   `json:"id"`
	Username         string   `json:"username"`
	Status           string   `json:"status"`
	Roles            []string `json:"roles"`
	Email            string   `json:"email,omitempty"`
	EmailSource      string   `json:"email_source,omitempty"`
	HasPassword      bool     `json:"has_password"`
	MergedIntoUserID string   `json:"merged_into_user_id,omitempty"`
}

// Identity is one linked sign-in (an OAuth / Cloudflare Access identity).
type Identity struct {
	ID       string    `json:"id"`
	Provider string    `json:"provider"`
	Subject  string    `json:"subject"` // masked; the full value never leaves the server
	Email    string    `json:"email,omitempty"`
	LinkedAt time.Time `json:"linked_at"`
	// OwnerEmail is true when Email is the configured owner_email.
	OwnerEmail bool `json:"owner_email"`
}

// Holder says which side of a merge holds the owner's sign-in.
type Holder string

// Owner-identity holders reported by the preview.
const (
	HolderNone      Holder = "none"
	HolderSurvivor  Holder = "survivor"
	HolderAbsorbed  Holder = "absorbed"
	HolderBoth      Holder = "both"
	HolderNoOwner   Holder = "no_owner_configured"
	HolderUnchecked Holder = ""
)

// Plan is the preview of a merge and, with its unexported ops, what Apply writes.
type Plan struct {
	SurvivorID string         `json:"survivor_id"`
	AbsorbedID string         `json:"absorbed_id"`
	Survivor   AccountSummary `json:"survivor"`
	Absorbed   AccountSummary `json:"absorbed"`

	Counts        map[string]FamilyCount `json:"counts"`
	Conflicts     []Conflict             `json:"conflicts"`
	ConflictTotal int                    `json:"conflict_total"`
	// Ops is how many raw key writes the merge makes.
	Ops int `json:"ops"`

	SurvivorIdentities []Identity `json:"survivor_identities"`
	AbsorbedIdentities []Identity `json:"absorbed_identities"`
	// OwnerIdentityHolder is the STOP-condition answer: which account holds the
	// owner's OAuth / Cloudflare Access identity. When it is absorbed or both,
	// Apply needs an explicit acknowledgement.
	OwnerIdentityHolder Holder `json:"owner_identity_holder"`
	// OwnerIdentityOnAbsorbed is true when the account being merged away holds an
	// owner identity.
	OwnerIdentityOnAbsorbed bool `json:"owner_identity_on_absorbed"`
	// OwnerIdentityAckRequired is what Apply gates on: the absorbed account holds
	// an owner identity, OR the holder cannot be told (no owner_email configured,
	// or a linked sign-in with no address to compare) and the absorbed account has
	// a linked sign-in at all. It fails closed: when the plan cannot say the
	// owner's sign-in is not on the account being merged away, the admin says so.
	OwnerIdentityAckRequired bool `json:"owner_identity_ack_required"`

	RolesGained  []string `json:"roles_gained"`
	EmailOutcome string   `json:"email_outcome"`

	Warnings []string `json:"warnings"`
	Refusals []string `json:"refusals"`
	// Digest names exactly what Apply will write. It is bound into the grant, so
	// an apply carries the digest of the preview the admin looked at.
	Digest string `json:"digest"`

	ops    []database.UserMergeOp
	retire database.UserRetirement
	// rowRefusals are the refusals in Refusals that quote row keys and decode
	// errors. Summary leaves them out (they are for the admin-only preview).
	rowRefusals []string
}

// Applicable is true when the plan has no refusal.
func (p *Plan) Applicable() bool { return len(p.Refusals) == 0 }

// PlanRequest names a merge.
type PlanRequest struct {
	// ActorID is the admin asking. Empty skips the actor checks (the sweep and
	// resume paths, which ran them at the start).
	ActorID    string
	SurvivorID string
	AbsorbedID string
}

// builder accumulates the ops of one plan: one op per key, with the before-image
// read once, the first time a key is touched.
type builder struct {
	s       *Service
	ops     map[string]*database.UserMergeOp
	counts  map[string]*FamilyCount
	plan    *Plan
	refused []string
	// unit is the group the ops touched next belong to ("" for none): see
	// database.UserMergeOp.Unit. A unit's ops are written in one batch and undone
	// together or not at all.
	unit string
}

func newBuilder(s *Service, p *Plan) *builder {
	return &builder{s: s, ops: map[string]*database.UserMergeOp{}, counts: map[string]*FamilyCount{}, plan: p}
}

func (b *builder) count(family string) *FamilyCount {
	c := b.counts[family]
	if c == nil {
		c = &FamilyCount{}
		b.counts[family] = c
	}
	return c
}

func (b *builder) refuse(format string, args ...any) {
	b.refused = append(b.refused, fmt.Sprintf(format, args...))
}

func (b *builder) conflict(family, key, outcome string) {
	b.count(family).Conflicts++
	b.plan.ConflictTotal++
	if len(b.plan.Conflicts) < maxReportedConflicts {
		b.plan.Conflicts = append(b.plan.Conflicts, Conflict{Family: family, Key: key, Outcome: outcome})
	}
}

// touch returns the op for key, creating it with the key's current value as the
// before-image.
func (b *builder) touch(family, key string) (*database.UserMergeOp, error) {
	if op, ok := b.ops[key]; ok {
		return op, nil
	}
	cur, err := b.s.store.GetRaw(key)
	if err != nil {
		// The key stays out of the error: it can carry an email address or a
		// token, and this text reaches operation rows.
		return nil, fmt.Errorf("read a %s row: %w", family, err)
	}
	op := &database.UserMergeOp{Key: key, Family: family, BeforeAbsent: cur == nil, Before: cur,
		Unit: b.unit, Derived: strings.HasPrefix(key, "idx:")}
	b.ops[key] = op
	return op, nil
}

func (b *builder) set(family, key, reason string, value []byte) error {
	op, err := b.touch(family, key)
	if err != nil {
		return err
	}
	op.After, op.Delete, op.Reason = bytes.Clone(value), false, reason
	return nil
}

// setOwner re-owns a row in place: only field changes, from one account to the
// other, in the row as it is when the op is applied. The row's other fields are
// neither read into the digest nor overwritten (see database.UserMergeOp).
func (b *builder) setOwner(family, key, field, from, to, reason string) error {
	op, err := b.touch(family, key)
	if err != nil {
		return err
	}
	if !op.IsOwnerOp() {
		// Refuse in the preview what the apply could only stop at: a row whose
		// owner field cannot be rewritten (repeated, or not a string).
		if _, _, err := database.RewriteJSONField(op.Before, field, to); err != nil {
			b.refuse("a %s row cannot be re-owned: %v", family, err)
		}
	}
	op.OwnerField, op.OwnerFrom, op.OwnerTo, op.Reason = field, from, to, reason
	op.Before, op.After, op.BeforeAbsent, op.Delete = nil, nil, false, false
	return nil
}

func (b *builder) del(family, key, reason string) error {
	op, err := b.touch(family, key)
	if err != nil {
		return err
	}
	op.After, op.Delete = nil, true
	if op.Reason == "" {
		op.Reason = reason
	}
	return nil
}

// finalize returns the ops that change something, in an order that is safe to
// stop in. Unit-less writes go first and unit-less deletes last (a crash between
// them leaves duplicates, never a loss); every unit sits between them whole and
// in unit order, because a unit is written in one batch and a chunk never splits
// one. Within each group, key order.
func (b *builder) finalize() []database.UserMergeOp {
	var sets, units, deletes []database.UserMergeOp
	for _, op := range b.ops {
		switch {
		case op.IsOwnerOp():
		case op.Delete && op.BeforeAbsent:
			continue // deleting a key that is not there
		case !op.Delete && !op.BeforeAbsent && bytes.Equal(op.Before, op.After):
			continue // writing what is already there
		}
		switch {
		case op.Unit != "":
			units = append(units, *op)
		case op.Delete:
			deletes = append(deletes, *op)
		default:
			sets = append(sets, *op)
		}
	}
	byKey := func(s []database.UserMergeOp) {
		sort.Slice(s, func(i, j int) bool { return s[i].Key < s[j].Key })
	}
	byKey(sets)
	byKey(deletes)
	sort.Slice(units, func(i, j int) bool {
		if units[i].Unit != units[j].Unit {
			return units[i].Unit < units[j].Unit
		}
		return units[i].Key < units[j].Key
	})
	return append(append(sets, units...), deletes...)
}

// BuildPlan builds the preview: which rows move or merge, the conflicts and how
// they resolve, every refusal, the identities each side holds, and the digest.
// It writes nothing. An error is an I/O failure; a merge that must not run is a
// Plan with Refusals.
func (s *Service) BuildPlan(ctx context.Context, req PlanRequest) (*Plan, error) {
	p := &Plan{SurvivorID: req.SurvivorID, AbsorbedID: req.AbsorbedID,
		Counts: map[string]FamilyCount{}, Conflicts: []Conflict{}, Warnings: []string{}, Refusals: []string{},
		SurvivorIdentities: []Identity{}, AbsorbedIdentities: []Identity{}, RolesGained: []string{}}
	if req.SurvivorID == "" || req.AbsorbedID == "" {
		p.Refusals = append(p.Refusals, "both a surviving and an absorbed account are required")
		return p, nil
	}
	if req.SurvivorID == req.AbsorbedID {
		p.Refusals = append(p.Refusals, "an account cannot be merged into itself")
		return p, nil
	}
	surv, err := s.store.GetUserByID(req.SurvivorID)
	if err != nil {
		return nil, fmt.Errorf("read survivor: %w", err)
	}
	abs, err := s.store.GetUserByID(req.AbsorbedID)
	if err != nil {
		return nil, fmt.Errorf("read absorbed account: %w", err)
	}
	if surv == nil {
		p.Refusals = append(p.Refusals, "the surviving account does not exist")
	}
	if abs == nil {
		p.Refusals = append(p.Refusals, "the account to merge away does not exist")
	}
	if surv == nil || abs == nil {
		return p, nil
	}
	p.Survivor, p.Absorbed = summarize(surv), summarize(abs)

	if req.ActorID != "" {
		isAdmin, err := s.ActorIsAdmin(req.ActorID)
		if err != nil {
			return nil, err
		}
		switch {
		case !isAdmin:
			p.Refusals = append(p.Refusals, "merging accounts needs an active admin")
		case req.ActorID == abs.ID:
			p.Refusals = append(p.Refusals, "you cannot merge away the account you are signed in as")
		}
	}
	if surv.Status != "active" {
		p.Refusals = append(p.Refusals, "the surviving account is not active")
	}
	if !slices.Contains(surv.Roles, "admin") {
		p.Refusals = append(p.Refusals, "the surviving account is not an admin")
	}
	if surv.MergedIntoUserID != "" {
		p.Refusals = append(p.Refusals, "the surviving account was itself merged into another account")
	}
	if abs.MergedIntoUserID != "" {
		p.Refusals = append(p.Refusals, "the account to merge away was already merged into another account")
	}
	if id := s.backfillUserID(); id != "" && id == abs.ID {
		p.Refusals = append(p.Refusals, "config abs_itunes_position_backfill_user_id pins the account to merge away; change it to the surviving account first")
	}
	if err := s.refuseForActiveOps(p, abs.ID); err != nil {
		return nil, err
	}

	survData, err := s.merger.ScanUserData(surv.ID)
	if err != nil {
		return nil, fmt.Errorf("scan survivor: %w", err)
	}
	absData, err := s.merger.ScanUserData(abs.ID)
	if err != nil {
		return nil, fmt.Errorf("scan absorbed account: %w", err)
	}
	b := newBuilder(s, p)
	if err := b.planData(ctx, surv, abs, absData); err != nil {
		return nil, err
	}
	if err := s.reportIdentities(p, survData, absData); err != nil {
		return nil, err
	}
	if len(survData.OAuthIdentities)+len(absData.OAuthIdentities) == 0 && surv.PasswordHash == "" {
		p.Refusals = append(p.Refusals, "the surviving account would have no way to sign in (no password and no linked sign-in)")
	}
	s.planAccounts(p, surv, abs)
	p.Refusals = append(p.Refusals, b.refused...)
	p.rowRefusals = slices.Clone(b.refused)
	s.warnAboutHistory(p, abs.ID)

	p.ops = b.finalize()
	p.Ops = len(p.ops)
	for fam, c := range b.counts {
		p.Counts[fam] = *c
	}
	p.Digest = digest(p)
	return p, nil
}

// ActorIsAdmin reports whether id is an existing, active account with the admin
// role: the only kind of account that may preview or run a merge.
func (s *Service) ActorIsAdmin(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	u, err := s.store.GetUserByID(id)
	if err != nil {
		return false, fmt.Errorf("read actor: %w", err)
	}
	return u != nil && u.Status == "active" && slices.Contains(u.Roles, "admin"), nil
}

func summarize(u *database.User) AccountSummary {
	return AccountSummary{ID: u.ID, Username: u.Username, Status: u.Status, Roles: slices.Clone(u.Roles),
		Email: u.Email, EmailSource: u.EmailSource, HasPassword: u.PasswordHash != "", MergedIntoUserID: u.MergedIntoUserID}
}

// refuseForActiveOps refuses while a queued or running operation names the
// absorbed account (in its params or as its actor): it would write under an id
// the merge is about to retire. The merge's own operation is exempt.
func (s *Service) refuseForActiveOps(p *Plan, absorbedID string) error {
	rows, err := s.store.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("list active operations: %w", err)
	}
	for _, r := range rows {
		if r.DefID == OpDefID {
			continue
		}
		if (r.ActorUserID != nil && *r.ActorUserID == absorbedID) || strings.Contains(r.Params, absorbedID) {
			p.Refusals = append(p.Refusals, fmt.Sprintf("operation %s (%s) is queued or running for the account to merge away; wait for it or cancel it", r.ID, r.DefID))
		}
	}
	return nil
}

// OpDefID is the operation definition that runs a merge. It lives here so the
// grant, the plan's exemption and the plugin share one constant.
const OpDefID = "maintenance.user-merge"

// planAccounts fills the retirement (the two user-row changes) and the roles and
// email outcome.
func (s *Service) planAccounts(p *Plan, surv, abs *database.User) {
	survAfter := database.UserIdentityFields{Roles: slices.Clone(surv.Roles), Email: surv.Email, EmailSource: surv.EmailSource}
	for _, r := range abs.Roles {
		if !slices.Contains(survAfter.Roles, r) {
			survAfter.Roles = append(survAfter.Roles, r)
			p.RolesGained = append(p.RolesGained, r)
		}
	}
	switch {
	case surv.Email != "":
		p.EmailOutcome = "the surviving account keeps its own email address"
		sameAddr := abs.Email != "" && util.SameEmail(abs.Email, surv.Email)
		absVerified := abs.Email != "" && database.EmailSourceIsTrusted(abs.EmailSource)
		switch {
		case sameAddr && absVerified && !database.EmailSourceIsTrusted(surv.EmailSource):
			// Both accounts hold this address and only the merged-away one proved it.
			// Without this the survivor would keep an unverified copy, nobody would
			// hold the address verified, and the owner's next SSO login with it would
			// create a new, non-admin account (oauth.ResolveUser step 5). The proof
			// transfers: the address becomes verified (source admin) on the survivor.
			survAfter.EmailSource = database.EmailSourceAdmin
			p.EmailOutcome = "the surviving account keeps this address and it becomes verified: the merged-away account held it verified, so nothing is lost"
		case abs.Email != "" && !sameAddr && absVerified:
			p.EmailOutcome = "the surviving account keeps its own email address. WARNING: the merged-away account's verified address is released, so no account holds it after the merge; signing in with it, without a linked sign-in, would create a new non-admin account"
			p.Warnings = append(p.Warnings, "the merged-away account's verified email address is released and not carried over; link its sign-in to the surviving account first, or set that address on the surviving account")
		case abs.Email != "" && !sameAddr:
			p.Warnings = append(p.Warnings, "the merged-away account's email address is released and not carried over; the surviving account keeps its own")
		}
	case abs.Email != "":
		// The address moves with its trust level unchanged: a merge never makes an
		// unverified address verified.
		survAfter.Email, survAfter.EmailSource = abs.Email, abs.EmailSource
		p.EmailOutcome = "the surviving account has no email address and takes the merged-away account's, with the same verification"
	default:
		p.EmailOutcome = "neither account has an email address"
	}
	p.retire = database.UserRetirement{
		SurvivorID: surv.ID, AbsorbedID: abs.ID,
		SurvivorBefore: database.UserIdentityFields{Roles: slices.Clone(surv.Roles), Email: surv.Email, EmailSource: surv.EmailSource},
		SurvivorAfter:  survAfter,
		AbsorbedBefore: database.UserIdentityFields{Roles: slices.Clone(abs.Roles), Email: abs.Email, EmailSource: abs.EmailSource},
		AbsorbedAfter:  database.UserIdentityFields{Roles: slices.Clone(abs.Roles), MergedIntoUserID: surv.ID},
	}
}

// warnAboutHistory adds the informational warnings: history that stays under
// the absorbed account's id, and clients that must change their login.
func (s *Service) warnAboutHistory(p *Plan, absorbedID string) {
	p.Warnings = append(p.Warnings,
		"Audiobookshelf-compatible apps signed in as the merged-away account must sign in again as the surviving account (a different username)",
		"history written under the merged-away account's id (the activity log, operation actors, the change log) is left as it is; the account row stays so it still shows a name")
	for _, prefix := range []string{"merge:combine-journal:", "merge:survivor-reconcile:"} {
		pairs, err := s.store.ScanPrefix(prefix)
		if err != nil {
			continue
		}
		for _, kv := range pairs {
			if bytes.Contains(kv.Value, []byte(absorbedID)) {
				p.Warnings = append(p.Warnings, "a book-merge undo journal ("+strings.TrimSuffix(prefix, ":")+") names the merged-away account; undoing that book merge later restores listening state to the surviving account")
				break
			}
		}
	}
}

// reportIdentities fills the identity lists and the owner-identity answer.
func (s *Service) reportIdentities(p *Plan, surv, abs *database.UserData) error {
	owner := util.NormalizeEmail(s.ownerEmail())
	conv := func(rows []database.UserDataRow, into *[]Identity) (hit bool, err error) {
		for _, r := range rows {
			var id database.OAuthIdentity
			if err := json.Unmarshal(r.Value, &id); err != nil {
				// The row key stays out of the error (it reaches operation rows).
				return false, fmt.Errorf("decode a linked sign-in: %w", err)
			}
			isOwner := owner != "" && util.NormalizeEmail(id.Email) == owner
			hit = hit || isOwner
			*into = append(*into, Identity{ID: id.ID, Provider: id.Provider, Subject: maskSubject(id.Subject),
				Email: id.Email, LinkedAt: id.CreatedAt, OwnerEmail: isOwner})
		}
		return hit, nil
	}
	sHit, err := conv(surv.OAuthIdentities, &p.SurvivorIdentities)
	if err != nil {
		return err
	}
	aHit, err := conv(abs.OAuthIdentities, &p.AbsorbedIdentities)
	if err != nil {
		return err
	}
	switch {
	case owner == "":
		p.OwnerIdentityHolder = HolderNoOwner
		p.Warnings = append(p.Warnings, "no owner_email is configured, so the owner's sign-in cannot be told apart; check the linked sign-ins by hand")
	case sHit && aHit:
		p.OwnerIdentityHolder = HolderBoth
	case aHit:
		p.OwnerIdentityHolder = HolderAbsorbed
	case sHit:
		p.OwnerIdentityHolder = HolderSurvivor
	default:
		p.OwnerIdentityHolder = HolderNone
	}
	p.OwnerIdentityOnAbsorbed = aHit
	// Fail closed. Only the absorbed side's identities matter: that is the side
	// whose sign-ins the merge re-links away from the account they were created on.
	absUnknown := owner == "" && len(p.AbsorbedIdentities) > 0
	if owner != "" {
		for _, id := range p.AbsorbedIdentities {
			if id.Email == "" {
				absUnknown = true
			}
		}
		if absUnknown && !aHit {
			p.Warnings = append(p.Warnings, "a sign-in on the account being merged away has no address, so the plan cannot tell whether it is the owner's; the merge needs your acknowledgement")
		}
	}
	p.OwnerIdentityAckRequired = aHit || absUnknown
	// The same provider on both sides is allowed (both move) but worth saying.
	seen := map[string]bool{}
	for _, id := range p.SurvivorIdentities {
		seen[id.Provider] = true
	}
	for _, id := range p.AbsorbedIdentities {
		if seen[id.Provider] {
			p.Warnings = append(p.Warnings, "both accounts have a "+id.Provider+" sign-in linked; both will be linked to the surviving account")
			break
		}
	}
	return nil
}

func maskSubject(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("•", len(s))
	}
	return s[:2] + "…" + s[len(s)-2:]
}

// planData plans every per-user row of absorbed into survivor. It is also what
// the straggler sweep runs against whatever the absorbed account has left.
//
// Per-user scans stay sequential on purpose (CLAUDE.md, concurrency): the work
// per row is a point read of a key that lives under one user, a merge touches
// one pair of accounts (hundreds to a few thousand rows, not the library), and
// every op must be built against a stable view so the digest means something.
func (b *builder) planData(ctx context.Context, surv, abs *database.User, d *database.UserData) error {
	for _, u := range d.Unreadable {
		b.refuse("the merged-away account has a row the merge cannot read: %s", u)
	}
	steps := []func(context.Context, *database.User, *database.User, *database.UserData) error{
		b.planBookStates, b.planBookmarks, b.planSyncAliasUse, b.planPreferences,
		b.planPlaybackProgress, b.planPlaybackEvents, b.planListenStats,
		b.planOAuth, b.planAPIKeys, b.planOwnerFields,
	}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step(ctx, surv, abs, d); err != nil {
			return err
		}
	}
	return nil
}

func decode[T any](b *builder, family, key string, value []byte) (*T, bool) {
	var v T
	if err := json.Unmarshal(value, &v); err != nil {
		b.refuse("cannot read %s row %s: %v", family, key, err)
		return nil, false
	}
	return &v, true
}

// rekeyed is key moved from the absorbed account to the survivor.
func rekeyed(key string, abs, surv *database.User) string {
	k, ok := database.RekeyUserKey(key, abs.ID, surv.ID)
	if !ok {
		return ""
	}
	return k
}

// moveOrKeep is the shared rule for the families where the survivor wins a
// collision and an absorbed row with no counterpart moves over, its user id
// field (idField, "" when the value carries none) rewritten.
func (b *builder) moveOrKeep(family string, rows []database.UserDataRow, surv, abs *database.User, idField string) error {
	for _, r := range rows {
		b.count(family).Rows++
		nk := rekeyed(r.Key, abs, surv)
		if nk == "" {
			b.refuse("%s row %s is not keyed by the merged-away account", family, r.Key)
			continue
		}
		if err := b.del(family, r.Key, "moved to the surviving account"); err != nil {
			return err
		}
		cur, err := b.s.store.GetRaw(nk)
		if err != nil {
			return err
		}
		if cur != nil {
			b.conflict(family, nk, "the surviving account's row is kept; the merged-away account's is dropped")
			continue
		}
		val := r.Value
		if idField != "" {
			out, _, err := database.RewriteJSONField(r.Value, idField, surv.ID)
			if err != nil {
				b.refuse("cannot read %s row %s: %v", family, r.Key, err)
				continue
			}
			val = out
		}
		if err := b.set(family, nk, "moved from the merged-away account", val); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) planBookmarks(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	// progress.Bookmark has no json tags, so the user id field is "UserID".
	return b.moveOrKeep(FamBookmarks, d.Bookmarks, surv, abs, "UserID")
}

func (b *builder) planPreferences(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	return b.moveOrKeep(FamPreferences, d.Preferences, surv, abs, "")
}

func (b *builder) planSyncAliasUse(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	type use struct {
		FirstUsedAt time.Time `json:"first_used_at"`
	}
	for _, r := range d.SyncAliasUses {
		b.count(FamSyncAliasUse).Rows++
		nk := rekeyed(r.Key, abs, surv)
		if nk == "" {
			b.refuse("sync alias row %s is not keyed by the merged-away account", r.Key)
			continue
		}
		if err := b.del(FamSyncAliasUse, r.Key, "moved to the surviving account"); err != nil {
			return err
		}
		cur, err := b.s.store.GetRaw(nk)
		if err != nil {
			return err
		}
		if cur == nil {
			if err := b.set(FamSyncAliasUse, nk, "moved from the merged-away account", r.Value); err != nil {
				return err
			}
			continue
		}
		a, ok1 := decode[use](b, FamSyncAliasUse, r.Key, r.Value)
		k, ok2 := decode[use](b, FamSyncAliasUse, nk, cur)
		if !ok1 || !ok2 {
			continue
		}
		b.conflict(FamSyncAliasUse, nk, "the earlier first-use time is kept")
		if a.FirstUsedAt.Before(k.FirstUsedAt) {
			if err := b.set(FamSyncAliasUse, nk, "earlier first use from the merged-away account", r.Value); err != nil {
				return err
			}
		}
	}
	// The one-time seed ran for the merged-away account; its rows are carried
	// above, so the marker goes with the account.
	for _, r := range d.SyncAliasSeeded {
		if err := b.del(FamSyncAliasUse, r.Key, "the merged-away account's seed marker"); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) planPlaybackProgress(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	for _, r := range d.PlaybackProgress {
		b.count(FamPlaybackProgress).Rows++
		nk := rekeyed(r.Key, abs, surv)
		if nk == "" {
			b.refuse("playback progress row %s is not keyed by the merged-away account", r.Key)
			continue
		}
		moved, _, err := database.RewriteJSONField(r.Value, "user_id", surv.ID)
		if err != nil {
			b.refuse("cannot read playback progress row %s: %v", r.Key, err)
			continue
		}
		if err := b.del(FamPlaybackProgress, r.Key, "moved to the surviving account"); err != nil {
			return err
		}
		cur, err := b.s.store.GetRaw(nk)
		if err != nil {
			return err
		}
		if cur == nil {
			if err := b.set(FamPlaybackProgress, nk, "moved from the merged-away account", moved); err != nil {
				return err
			}
			continue
		}
		a, ok1 := decode[database.PlaybackProgress](b, FamPlaybackProgress, r.Key, r.Value)
		k, ok2 := decode[database.PlaybackProgress](b, FamPlaybackProgress, nk, cur)
		if !ok1 || !ok2 {
			continue
		}
		if a.UpdatedAt.After(k.UpdatedAt) {
			b.conflict(FamPlaybackProgress, nk, "the newer snapshot (the merged-away account's) wins")
			if err := b.set(FamPlaybackProgress, nk, "newer snapshot from the merged-away account", moved); err != nil {
				return err
			}
		} else {
			b.conflict(FamPlaybackProgress, nk, "the newer snapshot (the surviving account's) is kept")
		}
	}
	return nil
}

func (b *builder) planPlaybackEvents(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	for _, r := range d.PlaybackEvents {
		b.count(FamPlaybackEvents).Rows++
		nk := rekeyed(r.Key, abs, surv)
		if nk == "" {
			b.refuse("playback event row %s is not keyed by the merged-away account", r.Key)
			continue
		}
		moved, _, err := database.RewriteJSONField(r.Value, "user_id", surv.ID)
		if err != nil {
			b.refuse("cannot read playback event row %s: %v", r.Key, err)
			continue
		}
		if err := b.del(FamPlaybackEvents, r.Key, "moved to the surviving account"); err != nil {
			return err
		}
		cur, err := b.s.store.GetRaw(nk)
		if err != nil {
			return err
		}
		if cur != nil {
			// Same book at the same nanosecond: events are an append-only log, so
			// keeping the survivor's is lossless for any practical purpose.
			b.conflict(FamPlaybackEvents, nk, "the surviving account's event is kept")
			continue
		}
		if err := b.set(FamPlaybackEvents, nk, "moved from the merged-away account", moved); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) planListenStats(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	for _, r := range d.ListenStats {
		b.count(FamListenStats).Rows++
		add, err := strconv.Atoi(strings.TrimSpace(string(r.Value)))
		if err != nil {
			b.refuse("cannot read listen-seconds counter %s: %v", r.Key, err)
			continue
		}
		nk := rekeyed(r.Key, abs, surv)
		cur, err := b.s.store.GetRaw(nk)
		if err != nil {
			return err
		}
		have := 0
		if cur != nil {
			if have, err = strconv.Atoi(strings.TrimSpace(string(cur))); err != nil {
				b.refuse("cannot read listen-seconds counter %s: %v", nk, err)
				continue
			}
			b.conflict(FamListenStats, nk, "the two counters are added")
		}
		// The sum is not idempotent: written without the delete of the counter it
		// came from, a resume would add the same seconds again. Both writes are
		// one unit, so they are one batch and a chunk never splits them.
		b.unit = "listen:" + nk
		err = b.del(FamListenStats, r.Key, "added to the surviving account")
		if err == nil {
			err = b.set(FamListenStats, nk, "sum of both accounts", []byte(strconv.Itoa(have+add)))
		}
		b.unit = ""
		if err != nil {
			return err
		}
	}
	return nil
}

// planIndexed plans the two families reached through a per-user index entry
// (linked identities, API keys). The primary row keeps its key and its owner
// field alone changes (an owner-field op: an API key is rewritten on every
// request, and a merge must not compare or overwrite its use counters), and the
// index entry moves. The row and both index entries are one unit.
func (b *builder) planIndexed(family string, rows []database.UserDataRow, surv, abs *database.User, indexKey func(user, id string) string, idPrefix string) error {
	for _, r := range rows {
		b.count(family).Rows++
		id := strings.TrimPrefix(r.Key, idPrefix)
		owner, err := database.JSONStringField(r.Value, "user_id")
		if err != nil {
			b.refuse("cannot read %s row %s: %v", family, r.Key, err)
			continue
		}
		if owner != abs.ID {
			b.refuse("%s row %s names owner %q, not the merged-away account", family, r.Key, owner)
			continue
		}
		b.unit = family + ":" + id
		err = b.setOwner(family, r.Key, "user_id", abs.ID, surv.ID, "re-linked to the surviving account")
		if err == nil {
			err = b.del(family, indexKey(abs.ID, id), "moved to the surviving account")
		}
		if err == nil {
			err = b.set(family, indexKey(surv.ID, id), "moved from the merged-away account", []byte("1"))
		}
		b.unit = ""
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) planOAuth(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	return b.planIndexed(FamOAuthIdentities, d.OAuthIdentities, surv, abs, database.OAuthUserIndexKey, "oauthid:")
}

func (b *builder) planAPIKeys(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	return b.planIndexed(FamAPIKeys, d.APIKeys, surv, abs, database.APIKeyUserIndexKey, "apikey:")
}

// planOwnerFields rewrites the owner field of the families that have no
// per-user key: playlists, collections, invites, and invite email grants.
func (b *builder) planOwnerFields(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	for _, f := range []struct {
		family, field string
		rows          []database.UserDataRow
	}{
		{FamPlaylists, "created_by_user_id", d.Playlists},
		{FamCollections, "created_by_user_id", d.Collections},
		{FamInvites, "created_by_user_id", d.Invites},
		{FamInviteGrants, "user_id", d.InviteGrants},
	} {
		for i, r := range f.rows {
			b.count(f.family).Rows++
			// Refusals name the row by position, not key: an invite's key is its
			// token and an invite grant's carries an address.
			owner, err := database.JSONStringField(r.Value, f.field)
			if err != nil {
				b.refuse("cannot read %s row %d: %v", f.family, i+1, err)
				continue
			}
			if owner != abs.ID {
				b.refuse("%s row %d does not name the merged-away account as its owner", f.family, i+1)
				continue
			}
			// An owner-field op: only the owner is compared and rewritten, in the
			// row as it is when the op is applied, so an edit to the playlist or
			// invite between preview and apply is neither lost nor in the digest.
			if err := b.setOwner(f.family, r.Key, f.field, abs.ID, surv.ID, "re-owned by the surviving account"); err != nil {
				return err
			}
		}
	}
	return nil
}

// planBookStates plans the per-(user, book) listening state: the ubs: row, its
// status index entries and the upos: rows, combined by merge.CombineUserStates,
// the same rule a book merge uses for two accounts' state on two books.
func (b *builder) planBookStates(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	type bookRows struct {
		state     *database.UserDataRow
		positions []database.UserDataRow
	}
	books := map[string]*bookRows{}
	get := func(id string) *bookRows {
		if books[id] == nil {
			books[id] = &bookRows{}
		}
		return books[id]
	}
	for i, r := range d.BookStates {
		id := strings.TrimPrefix(r.Key, "ubs:"+abs.ID+":")
		get(id).state = &d.BookStates[i]
	}
	for _, r := range d.Positions {
		rest := strings.TrimPrefix(r.Key, "upos:"+abs.ID+":")
		id, _, ok := strings.Cut(rest, ":")
		if !ok {
			b.refuse("position row %s has no segment", r.Key)
			continue
		}
		get(id).positions = append(get(id).positions, r)
	}
	ids := make([]string, 0, len(books))
	for id := range books {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		br := books[id]
		b.count(FamBookState).Rows++
		if err := b.planBook(id, surv, abs, br.state, br.positions); err != nil {
			return err
		}
	}
	return nil
}

func decodePositions(b *builder, rows []database.UserDataRow) ([]database.UserPosition, bool) {
	out := make([]database.UserPosition, 0, len(rows))
	for _, r := range rows {
		var p database.UserPosition
		if err := json.Unmarshal(r.Value, &p); err != nil {
			b.refuse("cannot read position row %s: %v", r.Key, err)
			return nil, false
		}
		out = append(out, p)
	}
	return out, true
}

func (b *builder) planBook(bookID string, surv, abs *database.User, absState *database.UserDataRow, absPositions []database.UserDataRow) error {
	const fam = FamBookState
	// One unit per book: the survivor's ubs: row, both accounts' status index
	// entries and every upos: row move together, so an undo never reverts an index
	// entry while keeping the row it indexes (or the other way round).
	b.unit = "book:" + bookID
	defer func() { b.unit = "" }()
	var absSide, keptSide merge.UserStateSide
	if absState != nil {
		st, ok := decode[database.UserBookState](b, fam, absState.Key, absState.Value)
		if !ok {
			return nil
		}
		absSide.State = st
	}
	var ok bool
	if absSide.Positions, ok = decodePositions(b, absPositions); !ok {
		return nil
	}
	keptKey := database.UserBookStateKey(surv.ID, bookID)
	keptRaw, err := b.s.store.GetRaw(keptKey)
	if err != nil {
		return err
	}
	if keptRaw != nil {
		st, ok := decode[database.UserBookState](b, fam, keptKey, keptRaw)
		if !ok {
			return nil
		}
		keptSide.State = st
	}
	keptPos, err := b.s.store.ScanPrefix(database.UserPositionBookPrefix(surv.ID, bookID))
	if err != nil {
		return err
	}
	keptPosRows := make([]database.UserDataRow, 0, len(keptPos))
	for _, kv := range keptPos {
		keptPosRows = append(keptPosRows, database.UserDataRow(kv))
	}
	if keptSide.Positions, ok = decodePositions(b, keptPosRows); !ok {
		return nil
	}

	// The absorbed account's rows always leave; the combination decides what the
	// survivor ends with.
	if absState != nil {
		if err := b.del(fam, absState.Key, "moved to the surviving account"); err != nil {
			return err
		}
	}
	for _, k := range database.UserBookStateStatusKeys(abs.ID, bookID) {
		if err := b.del(fam, k, "moved to the surviving account"); err != nil {
			return err
		}
	}
	for _, r := range absPositions {
		if err := b.del(fam, r.Key, "moved to the surviving account"); err != nil {
			return err
		}
	}

	comb := merge.CombineUserStates(surv.ID, bookID, absSide, keptSide)
	if !comb.Carry {
		return nil // nothing the survivor needs (a drained or empty absorbed row)
	}
	if keptSide.State != nil || len(keptSide.Positions) > 0 {
		outcome := "the surviving account's position is kept"
		if comb.PositionsFromAbsorbed {
			outcome = "the merged-away account's position is newer and wins"
		}
		if comb.State != nil && comb.State.Status == database.UserBookStatusFinished {
			outcome += "; finished stays finished"
		}
		b.conflict(fam, bookID, outcome)
	}
	if comb.State != nil {
		data, err := json.Marshal(comb.State)
		if err != nil {
			return err
		}
		if err := b.set(fam, keptKey, "combined listening state", data); err != nil {
			return err
		}
		for _, st := range []string{database.UserBookStatusUnstarted, database.UserBookStatusInProgress, database.UserBookStatusFinished, database.UserBookStatusAbandoned} {
			k := database.UserBookStateStatusKey(surv.ID, st, bookID)
			if st == comb.State.Status {
				err = b.set(fam, k, "status index", []byte("1"))
			} else {
				err = b.del(fam, k, "status index")
			}
			if err != nil {
				return err
			}
		}
	}
	if comb.PositionsFromAbsorbed {
		for _, kv := range keptPos {
			if err := b.del(fam, kv.Key, "replaced by the merged-away account's newer positions"); err != nil {
				return err
			}
		}
		for _, r := range absPositions {
			out, _, err := database.RewriteJSONField(r.Value, "user_id", surv.ID)
			if err != nil {
				b.refuse("cannot read position row %s: %v", r.Key, err)
				continue
			}
			nk := rekeyed(r.Key, abs, surv)
			if err := b.set(fam, nk, "moved from the merged-away account", out); err != nil {
				return err
			}
		}
	}
	return nil
}

// digest hashes everything Apply would write: both account ids, both accounts'
// identity-field changes, and every op's key with its before- and after-hash. It
// excludes the absorbed account's status and credential epoch, which Apply's
// first step changes, so locking it does not invalidate a preview.
func digest(p *Plan) string {
	h := sha256.New()
	fmt.Fprintf(h, "usermerge/v1\nsurvivor=%s\nabsorbed=%s\n", p.SurvivorID, p.AbsorbedID)
	writeFields := func(label string, f database.UserIdentityFields) {
		fmt.Fprintf(h, "%s roles=%s email=%s source=%s merged=%s\n", label,
			strings.Join(sortedCopy(f.Roles), ","), util.NormalizeEmail(f.Email), f.EmailSource, f.MergedIntoUserID)
	}
	writeFields("survivor.before", p.retire.SurvivorBefore)
	writeFields("survivor.after", p.retire.SurvivorAfter)
	writeFields("absorbed.before", p.retire.AbsorbedBefore)
	writeFields("absorbed.after", p.retire.AbsorbedAfter)
	ops := slices.Clone(p.ops)
	sort.Slice(ops, func(i, j int) bool { return ops[i].Key < ops[j].Key })
	for _, op := range ops {
		fmt.Fprintf(h, "op %s %s %s\n", op.Key, op.BeforeHash(), op.AfterHash())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
