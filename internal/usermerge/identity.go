// file: internal/usermerge/identity.go
// version: 0.1.0
// guid: 2f7a9c41-6e83-4d15-b0a2-9c4e7d1b8a36
// last-edited: 2026-11-21

package usermerge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The admin user merge once accounts live in core (F4-08 B10l). The books half
// is HEAD's own plan over the families books still owns (listening state,
// bookmarks, preferences, playback, listen stats, playlists, collections); the
// auth half (roles, e-mail, merged-into marker, sign-ins, API keys, invites,
// the allow-list row) is core's, planned by Admin.PlanUserMerge and staged by
// Admin.StageUserMerge. Apply commits both halves, and the merge journal, in
// ONE batch.

// ProblemMergeTooLarge is the problem code of the 409 a merge answers when its
// batch would exceed MergeBatchCap.
const ProblemMergeTooLarge = "merge_too_large"

// MergeBatchCap is the largest estimated batch, in bytes, an identity merge
// stages. Sized from a measurement: the largest per-user footprint in the merge
// families on a production snapshot (rehearsal run 20261122T081249Z, read-only)
// is 24.62 MiB, one user and all of it pref: rows; the rule is at least 4x
// headroom over the largest account, so 128 MiB (5.2x). It stays under core's
// default storage batch ceiling (core.storage.max_batch_bytes, 256 MiB). This is
// the one number to change when a larger account appears.
const MergeBatchCap int64 = 128 << 20

// ErrMergeTooLarge: the merge's estimated batch is over MergeBatchCap. Nothing
// was staged or written.
var ErrMergeTooLarge = errors.New("usermerge: " + ProblemMergeTooLarge + ": this merge is too large to apply as one batch")

// ErrNoPrincipal: an apply under identity needs the signed-in admin's own
// principal (core re-checks it on every call) and the request carried none, or
// carried one for another user, or one that no longer stands. It wraps
// coreauth.ErrDenied.
var ErrNoPrincipal = fmt.Errorf("usermerge: the merge needs the acting admin's own principal: %w", coreauth.ErrDenied)

// Refusal codes core decides, as Plan.RefusalCodes carries them.
const (
	CodeLastAdmin    = "last_admin"
	CodeEmailInUse   = "email_in_use"
	CodeInactiveUser = "inactive_user"
)

// CoreIdentity is what the merge needs of core once accounts live there. The
// three members come from module.Deps (AuthAdmin, Auth) and AuthRefresh.
type CoreIdentity struct {
	// Admin plans and stages the auth half. Every method re-checks the ctx
	// principal.
	Admin coreauth.Admin
	// Users reads accounts.
	Users coreauth.Identities
	// Refresh re-resolves a principal held past its request (Identity.Refresh).
	Refresh func(context.Context, *coreauth.Principal) (*coreauth.Principal, error)
}

func (c *CoreIdentity) ready() bool {
	return c != nil && c.Admin != nil && c.Users != nil && c.Refresh != nil
}

// WithCoreIdentity gives the service core's handles. With the flip wired and
// no handles the merge stays unavailable (ErrUnavailableUnderIdentity).
func WithCoreIdentity(c CoreIdentity) Option {
	return func(s *Service) { s.core = &c }
}

// CoreSummary is core's masked summary of the auth half, as the preview shows
// it: ids, provider names and counts, never a credential, a secret or a hash.
type CoreSummary struct {
	RolesTransferred  []string `json:"roles_transferred"`
	TargetHoldsAdmin  bool     `json:"target_holds_admin"`
	Email             string   `json:"email"`
	APIKeyIDs         []string `json:"api_key_ids"`
	IdentityProviders []string `json:"identity_providers"`
	AllowListRows     int      `json:"allow_list_rows"`
}

func summaryFrom(m coreauth.MergeSummary) *CoreSummary {
	roles := make([]string, len(m.RolesTransferred))
	for i, r := range m.RolesTransferred {
		roles[i] = string(r)
	}
	return &CoreSummary{
		RolesTransferred: roles, TargetHoldsAdmin: m.TargetHoldsAdmin, Email: string(m.Email),
		APIKeyIDs: slices.Clone(m.APIKeys), IdentityProviders: slices.Clone(m.IdentityProviders), AllowListRows: m.AllowListRows,
	}
}

// emailOutcomeText is HEAD's wording of each e-mail outcome.
func emailOutcomeText(code string) string {
	switch coreauth.MergeEmailOutcome(code) {
	case coreauth.MergeEmailKept:
		return "the surviving account keeps its own email address"
	case coreauth.MergeEmailMoved:
		return "the surviving account has no email address and takes the merged-away account's, with the same verification"
	case coreauth.MergeEmailVerifiedTransferred:
		return "the surviving account keeps this address and it becomes verified: the merged-away account held it verified, so nothing is lost"
	case coreauth.MergeEmailReleasedVerified:
		return "the surviving account keeps its own email address. WARNING: the merged-away account's verified address is released, so no account holds it after the merge; signing in with it, without a linked sign-in, would create a new non-admin account"
	case coreauth.MergeEmailNone:
		return "neither account has an email address"
	}
	return "e-mail outcome: " + code
}

// flipped reports whether the service is wired for core identity.
func (s *Service) flipped() bool { return s.identityFlipped != nil && s.identityFlipped() }

// identityReady is the wiring the identity merge needs: core's handles and a
// store that can commit one batch with them.
func (s *Service) identityReady() bool { return s.core.ready() && s.idMerger != nil }

// ResolveAccount resolves a user id taken from a request to the id of the
// stored account, false when there is none. Under identity the account is
// core's; otherwise it is the books store's.
func (s *Service) ResolveAccount(ctx context.Context, raw string) (string, bool, error) {
	if s.flipped() {
		u, err := s.coreUser(ctx, raw)
		if err != nil || u == nil {
			return "", false, err
		}
		return string(u.ID), true, nil
	}
	u, err := s.store.GetUserByID(raw)
	if err != nil || u == nil {
		return "", false, err
	}
	return u.ID, true, nil
}

// coreUser reads one account from core: nil, nil when there is none.
func (s *Service) coreUser(ctx context.Context, id string) (*coreauth.User, error) {
	if id == "" {
		return nil, nil
	}
	u, err := s.core.Users.User(ctx, coreauth.UserID(id))
	if errors.Is(err, coreauth.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read account: %w", err)
	}
	return u, nil
}

func (s *Service) coreActorIsAdmin(ctx context.Context, id string) (bool, error) {
	if !s.identityReady() {
		return false, nil
	}
	u, err := s.coreUser(ctx, id)
	if err != nil || u == nil {
		return false, err
	}
	return u.Status == coreauth.StatusActive && slices.Contains(u.Roles, rbac.RoleAdmin), nil
}

func summarizeCore(u *coreauth.User) AccountSummary {
	roles := make([]string, len(u.Roles))
	for i, r := range u.Roles {
		roles[i] = string(r)
	}
	return AccountSummary{ID: string(u.ID), Username: u.Username, Status: string(u.Status), Roles: roles,
		Email: u.Email, HasPassword: u.HasPassword, MergedIntoUserID: string(u.MergedInto)}
}

func isSystemCoreAccount(u *coreauth.User) bool {
	return u.Status == coreauth.StatusSystem || u.ID == coreauth.SystemUserID
}

// refusalOf classifies a core error that is a refusal of the merge (not a
// failure of the store and not a denial of the actor): its code and its text.
func refusalOf(err error) (code, text string, ok bool) {
	var mr *coreauth.MergeRefusal
	switch {
	case errors.As(err, &mr):
		return mr.Code, mr.Detail, true
	case errors.Is(err, coreauth.ErrLastAdmin):
		return CodeLastAdmin, "the merge would leave no active administrator", true
	case errors.Is(err, coreauth.ErrEmailInUse):
		return CodeEmailInUse, "another account holds the address the merge would give the surviving account verified", true
	case errors.Is(err, coreauth.ErrInactiveUser):
		return CodeInactiveUser, "the surviving account is not active", true
	}
	return "", "", false
}

// buildIdentityPlan is BuildPlan once accounts live in core. Books' own
// refusals and its plan come first: a merge books refuses never reaches core,
// so core's answer (email_linkable among them) cannot pre-empt the survivor
// rule. Core's plan and summary are asked only for a merge books would accept.
func (s *Service) buildIdentityPlan(ctx context.Context, req PlanRequest) (*Plan, error) {
	p := newPlan(req)
	if req.SurvivorID == "" || req.AbsorbedID == "" {
		p.Refusals = append(p.Refusals, "both a surviving and an absorbed account are required")
		return p, nil
	}
	if req.SurvivorID == req.AbsorbedID {
		p.Refusals = append(p.Refusals, "an account cannot be merged into itself")
		return p, nil
	}
	surv, err := s.coreUser(ctx, req.SurvivorID)
	if err != nil {
		return nil, fmt.Errorf("read survivor: %w", err)
	}
	abs, err := s.coreUser(ctx, req.AbsorbedID)
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
	p.Survivor, p.Absorbed = summarizeCore(surv), summarizeCore(abs)
	p.coreSurvivor, p.coreAbsorbed = surv, abs

	if req.ActorID != "" {
		isAdmin, err := s.coreActorIsAdmin(ctx, req.ActorID)
		if err != nil {
			return nil, err
		}
		switch {
		case !isAdmin:
			p.Refusals = append(p.Refusals, "merging accounts needs an active admin")
		case req.ActorID == string(abs.ID):
			p.Refusals = append(p.Refusals, "you cannot merge away the account you are signed in as")
		}
	}
	if isSystemCoreAccount(surv) {
		p.Refusals = append(p.Refusals, "the surviving account is a system account; system accounts are never merged")
	}
	if isSystemCoreAccount(abs) {
		p.Refusals = append(p.Refusals, "the account to merge away is a system account; system accounts are never merged")
	}
	if surv.Status != coreauth.StatusActive {
		p.Refusals = append(p.Refusals, "the surviving account is not active")
	}
	// Kept in books, as HEAD has it: this is what makes core's email_linkable
	// and sole-admin-source refusals unreachable through the app.
	if !slices.Contains(surv.Roles, rbac.RoleAdmin) {
		p.Refusals = append(p.Refusals, "the surviving account is not an admin")
	}
	if surv.MergedInto != "" {
		p.Refusals = append(p.Refusals, "the surviving account was itself merged into another account")
	}
	if abs.MergedInto != "" {
		p.Refusals = append(p.Refusals, "the account to merge away was already merged into another account")
	}
	if id := s.backfillUserID(); id != "" && id == string(abs.ID) {
		p.Refusals = append(p.Refusals, "config abs_itunes_position_backfill_user_id pins the account to merge away; change it to the surviving account first")
	}
	if err := s.refuseForActiveOps(p, string(abs.ID)); err != nil {
		return nil, err
	}

	absData, err := s.idMerger.ScanBooksUserData(string(abs.ID))
	if err != nil {
		return nil, fmt.Errorf("scan absorbed account: %w", err)
	}
	b := newBuilder(s, p)
	if err := b.planIdentityData(ctx, string(surv.ID), string(abs.ID), absData); err != nil {
		return nil, err
	}
	p.Refusals = append(p.Refusals, b.refused...)
	p.rowRefusals = slices.Clone(b.refused)
	s.warnAboutHistory(p, string(abs.ID))
	p.ops = b.finalize()
	p.Ops = len(p.ops)
	for fam, c := range b.counts {
		p.Counts[fam] = *c
	}
	if len(p.Refusals) == 0 {
		if err := s.planCoreHalf(ctx, p, surv, abs); err != nil {
			return nil, err
		}
	}
	// A merge too large for one batch is not a refusal the admin can act on in
	// the preview: it answers 409 merge_too_large, as an apply does.
	if n := s.estimateBatchBytes(p.ops); p.Applicable() && n > s.mergeBatchCap() {
		return nil, fmt.Errorf("%w (estimated %d bytes, cap %d)", ErrMergeTooLarge, n, s.mergeBatchCap())
	}
	p.Digest = identityDigest(p)
	return p, nil
}

// planCoreHalf asks core for the auth half and folds its answer into p: a
// refusal becomes a plan refusal with its code, a denial or a failure is the
// call's error.
func (s *Service) planCoreHalf(ctx context.Context, p *Plan, surv, abs *coreauth.User) error {
	cplan, sum, err := s.core.Admin.PlanUserMerge(ctx, abs.ID, surv.ID)
	if err != nil {
		if code, text, ok := refusalOf(err); ok {
			p.Refusals = append(p.Refusals, text)
			p.RefusalCodes = append(p.RefusalCodes, code)
			return nil
		}
		return fmt.Errorf("plan the account half of the merge: %w", err)
	}
	p.corePlan = cplan
	p.Core = summaryFrom(sum)
	p.RolesGained = slices.Clone(p.Core.RolesTransferred)
	p.EmailOutcome = emailOutcomeText(p.Core.Email)
	switch coreauth.MergeEmailOutcome(p.Core.Email) {
	case coreauth.MergeEmailReleasedVerified:
		p.Warnings = append(p.Warnings, "the merged-away account's verified email address is released and not carried over; link its sign-in to the surviving account first, or set that address on the surviving account")
	}
	// Core's summary names the providers of the sign-ins that move, not their
	// addresses, so the plan cannot tell whether one is the owner's: it fails
	// closed and asks for the acknowledgement whenever a sign-in moves.
	p.OwnerIdentityHolder = HolderUnchecked
	if len(p.Core.IdentityProviders) > 0 {
		p.OwnerIdentityAckRequired = true
		p.Warnings = append(p.Warnings, "a sign-in on the account being merged away moves to the surviving account and the plan cannot tell whether it is the owner's; the merge needs your acknowledgement")
	}
	return nil
}

// planIdentityData plans the books half: every per-user row books owns, with
// none of core's families (sign-ins, API keys, invites, invite grants), which
// core's StageUserMerge re-points and books must never write.
func (b *builder) planIdentityData(ctx context.Context, survID, absID string, d *database.UserData) error {
	surv, abs := &database.User{ID: survID}, &database.User{ID: absID}
	for _, u := range d.Unreadable {
		b.refuse("the merged-away account has a row the merge cannot read: %s", u)
	}
	for _, step := range []func(context.Context, *database.User, *database.User, *database.UserData) error{
		b.planBookStates, b.planBookmarks, b.planSyncAliasUse, b.planPreferences,
		b.planPlaybackProgress, b.planPlaybackEvents, b.planListenStats, b.planBooksOwnerFields,
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step(ctx, surv, abs, d); err != nil {
			return err
		}
	}
	return nil
}

// planBooksOwnerFields is planOwnerFields for the two families books owns
// (playlists, collections).
func (b *builder) planBooksOwnerFields(_ context.Context, surv, abs *database.User, d *database.UserData) error {
	return b.planOwnerFamilies(surv, abs, []ownerFamily{
		{FamPlaylists, "created_by_user_id", d.Playlists},
		{FamCollections, "created_by_user_id", d.Collections},
	})
}

// identityDigest names exactly what an identity Apply will write: both ids,
// every books op's key with its before- and after-hash, and core's summary of
// the auth half. An apply whose preview differs in either half is refused.
func identityDigest(p *Plan) string {
	h := sha256.New()
	fmt.Fprintf(h, "usermerge/v2-identity\nsurvivor=%s\nabsorbed=%s\n", p.SurvivorID, p.AbsorbedID)
	ops := slices.Clone(p.ops)
	sort.Slice(ops, func(i, j int) bool { return ops[i].Key < ops[j].Key })
	for _, op := range ops {
		fmt.Fprintf(h, "op %s %s %s\n", op.Key, op.BeforeHash(), op.AfterHash())
	}
	if c := p.Core; c != nil {
		fmt.Fprintf(h, "core roles=%s admin=%t email=%s keys=%s providers=%s allow=%d\n",
			strings.Join(sortedCopy(c.RolesTransferred), ","), c.TargetHoldsAdmin, c.Email,
			strings.Join(sortedCopy(c.APIKeyIDs), ","), strings.Join(sortedCopy(c.IdentityProviders), ","), c.AllowListRows)
	}
	fmt.Fprintf(h, "refusals=%d codes=%s\n", len(p.Refusals), strings.Join(sortedCopy(p.RefusalCodes), ","))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) mergeBatchCap() int64 {
	if s.batchCap > 0 {
		return s.batchCap
	}
	return MergeBatchCap
}

// perOpOverhead is the bytes one staged write costs beyond its key and value
// (batch framing, the journal's JSON field names).
const perOpOverhead = 128

// estimateBatchBytes is what the merge's one batch carries: each op's key and
// images staged once for the row and once more in the journal, which holds the
// before- and after-images.
func (s *Service) estimateBatchBytes(ops []database.UserMergeOp) int64 {
	var n int64
	for _, op := range ops {
		n += int64(len(op.Key)) + perOpOverhead
		n += 2 * int64(len(op.Before)+len(op.After)+len(op.Key)+perOpOverhead)
	}
	return n + 4096
}

// CoreJournal is the identity-era record of the auth half of a merge, in the
// journal beside HEAD's ops (Journal.Core). It names what a reversal of core's
// StageUserMerge would need and no credential: ids and codes only. Absent on a
// journal written before the identity flip.
type CoreJournal struct {
	// Era is "identity": the marker an identity-era journal carries.
	Era string `json:"era"`
	// SourceStatusBefore, SourceEmailBefore, SourceEmailVerifiedBefore,
	// SourceRolesBefore, SourceVersionBefore and SourceEpochBefore are the
	// merged-away account as core held it when the merge was planned (read
	// before and after planning; equal versions on both reads).
	SourceStatusBefore        string   `json:"source_status_before"`
	SourceEmailBefore         string   `json:"source_email_before,omitempty"`
	SourceEmailVerifiedBefore bool     `json:"source_email_verified_before"`
	SourceRolesBefore         []string `json:"source_roles_before"`
	SourceVersionBefore       int64    `json:"source_version_before"`
	SourceEpochBefore         int64    `json:"source_epoch_before"`
	// TargetEmailBefore, TargetEmailVerifiedBefore, TargetRolesBefore and
	// TargetVersionBefore are the survivor as core held it, likewise.
	TargetEmailBefore         string   `json:"target_email_before,omitempty"`
	TargetEmailVerifiedBefore bool     `json:"target_email_verified_before"`
	TargetRolesBefore         []string `json:"target_roles_before"`
	TargetVersionBefore       int64    `json:"target_version_before"`
	// RolesAdded are the roles the merge gave the survivor.
	RolesAdded []string `json:"roles_added"`
	// EmailOutcome is core's e-mail outcome code.
	EmailOutcome string `json:"email_outcome"`
	// APIKeyIDs are the ids of the keys that moved (never a secret or a hash).
	APIKeyIDs []string `json:"api_key_ids"`
	// IdentityProviders are the providers of the sign-ins that moved. Core's
	// summary does not carry the sign-ins' ids, and none is derived here.
	IdentityProviders []string `json:"identity_providers"`
	AllowListRows     int      `json:"allow_list_rows"`
	// IdentityIDs are the external subject ids of the sign-ins that moved, in
	// the order of IdentityProviders, and APIKeyVersions the version each moved
	// key was left at (the one the merge committed, StageMerge's bump included).
	// An unmerge reverses exactly these and only while each is still as the merge
	// left it. A subject id names a person at an identity provider, so these two
	// live in the journal alone: CoreSummary (the preview and the plan view) does
	// not carry them and no response or log line prints this struct (the
	// journal's Core is shown only as counts; TestCoreJournal_SubjectStaysInTheJournal).
	IdentityIDs    []string         `json:"identity_ids,omitempty"`
	APIKeyVersions map[string]int64 `json:"api_key_versions,omitempty"`
	// SweepPending is true from the committed batch until the post-commit
	// straggler sweep has finished cleanly and the journal has been re-saved
	// without it. It starts true IN THE BATCH, so a crash, a failed sweep or a
	// failed final save all leave it true and none has to write anything to be
	// seen: an unmerge (B10p) that finds it true must sweep the absorbed
	// account's rows again before it trusts Ops to be the whole books half.
	SweepPending bool `json:"sweep_pending,omitempty"`
}

const eraIdentity = "identity"

func rolesOf(u *coreauth.User) []string {
	out := make([]string, len(u.Roles))
	for i, r := range u.Roles {
		out[i] = string(r)
	}
	return out
}

// coreJournalOf records p's core half from the accounts as read around the
// plan (src, tgt) and core's summary.
func coreJournalOf(p *Plan, src, tgt *coreauth.User) *CoreJournal {
	c := p.Core
	rcpt := p.corePlan.Receipt()
	return &CoreJournal{
		Era:                       eraIdentity,
		SourceStatusBefore:        string(src.Status),
		SourceEmailBefore:         src.Email,
		SourceEmailVerifiedBefore: src.EmailVerified,
		SourceRolesBefore:         rolesOf(src),
		SourceVersionBefore:       src.Version,
		SourceEpochBefore:         src.CredentialEpoch,
		TargetEmailBefore:         tgt.Email,
		TargetEmailVerifiedBefore: tgt.EmailVerified,
		TargetRolesBefore:         rolesOf(tgt),
		TargetVersionBefore:       tgt.Version,
		RolesAdded:                slices.Clone(c.RolesTransferred),
		EmailOutcome:              c.Email,
		APIKeyIDs:                 slices.Clone(c.APIKeyIDs),
		IdentityProviders:         slices.Clone(c.IdentityProviders),
		AllowListRows:             c.AllowListRows,
		IdentityIDs:               slices.Clone(rcpt.IdentityIDs),
		APIKeyVersions:            maps.Clone(rcpt.APIKeyVersions),
		SweepPending:              true,
	}
}

// bindPrincipal is ctx carrying the acting admin's own principal, re-validated
// now (core's Refresh: the seal still holds, the user still stands, the session
// or key is alive, the credential epoch has not moved, the role set is
// current). The principal in ctx is the one Refresh RETURNS, never the copy the
// grant carried. It is the one the request that minted the grant held; a
// background run has no request of its own, and this is the only principal it
// may act under. Nothing is synthesized and there is no fallback: a missing,
// foreign, tampered or stale principal refuses the merge. A store that cannot
// answer is told apart from a principal that no longer stands, so the first is
// a retryable failure and the second a denial.
func (s *Service) bindPrincipal(ctx context.Context, req ApplyRequest) (context.Context, error) {
	p := req.Principal
	if p == nil || p.User == nil || req.ActorID == "" || string(p.User.ID) != req.ActorID {
		return nil, ErrNoPrincipal
	}
	np, err := s.core.Refresh(ctx, p)
	switch {
	case errors.Is(err, coreauth.ErrStoreUnavailable):
		return nil, fmt.Errorf("check the acting admin's session: %w", err)
	case err != nil:
		return nil, fmt.Errorf("%w (%v)", ErrNoPrincipal, err)
	case np == nil || np.User == nil || string(np.User.ID) != req.ActorID:
		return nil, ErrNoPrincipal
	}
	return coreauth.WithPrincipal(ctx, np), nil
}

// applyIdentity is Apply once accounts live in core: plan (books half, core's
// plan), refuse on a refusal or a digest that is not the preview's, then under
// the locks HEAD's apply takes plan again, compare again, check the size cap,
// and commit the books rows, the journal and core's StageUserMerge in one
// batch. Nothing is half-applied, so there is no cursor to resume. The caller
// holds runMu.
func (s *Service) applyIdentity(ctx context.Context, req ApplyRequest, prog Progress) (*Journal, error) {
	ctx, err := s.bindPrincipal(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := s.refuseIfOpenJournal(req.SurvivorID, req.AbsorbedID); err != nil {
		return nil, err
	}
	plan, err := s.BuildPlan(ctx, PlanRequest{ActorID: req.ActorID, SurvivorID: req.SurvivorID, AbsorbedID: req.AbsorbedID})
	if err != nil {
		return nil, err
	}
	if !plan.Applicable() {
		return nil, &RefusedError{Plan: plan}
	}
	if req.Digest == "" || req.Digest != plan.Digest {
		return nil, ErrDigestMismatch
	}
	if plan.OwnerIdentityAckRequired && !req.AcknowledgeOwnerIdentity {
		return nil, ErrOwnerIdentityNotAcknowledged
	}
	prog.report(0, plan.Ops, "taking the merge locks")
	j, after, err := s.commitIdentity(ctx, req, plan, prog)
	if err != nil {
		return nil, err
	}
	// The locks are released: publish what core staged (the source's
	// invalidation, rbac's assignment change) exactly once, for a commit that
	// succeeded and never otherwise.
	if after != nil {
		after(ctx)
	}
	sweepErr := s.sweepAfterCommit(ctx, j)
	// The merge is committed whatever the sweep did, so the audit entry is
	// written either way.
	if s.audit != nil {
		s.audit(fmt.Sprintf("merged user %s into %s", j.AbsorbedID, j.SurvivorID), map[string]any{
			"journal_id": j.ID, "survivor_id": j.SurvivorID, "absorbed_id": j.AbsorbedID,
			"actor_id": j.ActorID, "ops": len(j.Ops), "conflicts": j.ConflictTotal,
			"sweep_pending": j.Core != nil && j.Core.SweepPending,
		})
	}
	if sweepErr != nil {
		return j, sweepErr
	}
	prog.report(len(j.Ops), len(j.Ops), "merge applied")
	return j, nil
}

// commitIdentity is the part of an identity apply that runs under the locks:
// it plans again, compares the digest, checks the cap, builds the journal and
// commits the one batch. It returns core's AfterCommit only for a commit that
// succeeded.
func (s *Service) commitIdentity(ctx context.Context, req ApplyRequest, first *Plan, prog Progress) (*Journal, coreauth.AfterCommit, error) {
	// HEAD's locks: the merge lock and the per-(user, book) stripes every
	// writer of listening state also takes. They are held through the commit.
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	unlock := database.LockUserBookStates(lockKeys(first.ops, req.SurvivorID, req.AbsorbedID)...)
	defer unlock()

	plan, err := s.BuildPlan(ctx, PlanRequest{ActorID: req.ActorID, SurvivorID: req.SurvivorID, AbsorbedID: req.AbsorbedID})
	if err != nil {
		return nil, nil, err
	}
	if !plan.Applicable() {
		return nil, nil, &RefusedError{Plan: plan}
	}
	if plan.Digest != req.Digest {
		return nil, nil, ErrDigestMismatch
	}
	// The cap comes before anything is staged or written.
	if n := s.estimateBatchBytes(plan.ops); n > s.mergeBatchCap() {
		return nil, nil, fmt.Errorf("%w (estimated %d bytes, cap %d)", ErrMergeTooLarge, n, s.mergeBatchCap())
	}
	// The second read of the sandwich: the accounts the journal records are the
	// ones the plan (and so StageUserMerge's pins) read.
	src, err := s.coreUser(ctx, req.AbsorbedID)
	if err != nil || src == nil || src.Version != plan.coreAbsorbed.Version {
		return nil, nil, conflictErr(err, "the merged-away account changed while the merge was planned")
	}
	tgt, err := s.coreUser(ctx, req.SurvivorID)
	if err != nil || tgt == nil || tgt.Version != plan.coreSurvivor.Version {
		return nil, nil, conflictErr(err, "the surviving account changed while the merge was planned")
	}

	now := s.now()
	j := &Journal{
		ID: s.newID(), Status: StatusApplied, SurvivorID: req.SurvivorID, AbsorbedID: req.AbsorbedID,
		ActorID: req.ActorID, Digest: plan.Digest, AbsorbedStatusBefore: string(src.Status),
		Ops: plan.ops, Cursor: len(plan.ops), Retired: true,
		Counts: plan.Counts, ConflictTotal: plan.ConflictTotal, Warnings: plan.Warnings,
		Core:      coreJournalOf(plan, src, tgt),
		CreatedAt: now, UpdatedAt: now, AppliedAt: &now,
	}
	data, err := json.Marshal(j)
	if err != nil {
		return nil, nil, err
	}
	prog.report(0, len(plan.ops), "merging")
	var after coreauth.AfterCommit
	err = s.idMerger.ApplyUserMergeIdentity(plan.ops, journalKey(j.ID), data, func(b *storage.Batch) error {
		a, err := s.core.Admin.StageUserMerge(ctx, b, plan.corePlan)
		if err != nil {
			return err
		}
		after = a
		return nil
	})
	if err != nil {
		return nil, nil, s.mapApplyError(err, plan)
	}
	return j, after, nil
}

// ErrSweepIncomplete is a merge that COMMITTED but whose post-commit straggler
// sweep did not finish cleanly. The merge stands (core's half and the books rows
// are one committed batch, and the journal is in it); what is left is that rows
// the absorbed account's in-flight requests wrote after the plan may still sit
// under it. The journal says so (Core.SweepPending stays true), the error log
// names the merge, and the operation fails with this error so the run is not
// reported as a clean success.
var ErrSweepIncomplete = errors.New("usermerge: the merge committed but its straggler sweep did not finish")

// SweepError is ErrSweepIncomplete with the merge it concerns.
type SweepError struct {
	JournalID string
	Cause     error
}

func (e *SweepError) Error() string {
	return fmt.Sprintf("%v (merge %s; journal marked sweep_pending, run the sweep again before an unmerge): %v",
		ErrSweepIncomplete, e.JournalID, e.Cause)
}

func (e *SweepError) Unwrap() []error { return []error{ErrSweepIncomplete, e.Cause} }

// sweepAfterCommit moves books rows the absorbed account's in-flight requests
// wrote after the plan read it. The merge is committed and the account is
// already disabled (its sessions fail on core's next check), so the window is
// small, but it is not silent: the straggler rows and the journal that records
// them cannot be in the one commit (the rows do not exist when it is staged),
// so each pass is its own locked write of rows AND journal together
// (applyRemaining), and the result is tracked by Core.SweepPending, which is true
// from the commit until a clean sweep has been recorded. Every way the sweep can
// fail returns a *SweepError and logs at error with the merge id; none can leave
// the journal claiming a clean sweep.
func (s *Service) sweepAfterCommit(ctx context.Context, j *Journal) error {
	fail := func(msg string, cause error) error {
		// Ops past the cursor were planned but never written: keep the journal
		// to what happened.
		j.Ops = j.Ops[:j.Cursor]
		j.Warnings = append(j.Warnings, msg)
		if err := s.save(j); err != nil {
			s.log.Error("merge %s: saving the journal after a failed straggler sweep failed too (sweep_pending stays set in the committed journal): %v", j.ID, err)
		}
		s.log.Error("merge %s (absorbed %s into %s) committed but its straggler sweep failed: %s: %v", j.ID, j.AbsorbedID, j.SurvivorID, msg, cause)
		return &SweepError{JournalID: j.ID, Cause: cause}
	}
	clean := false
	for range maxSweeps {
		more, err := s.sweepIdentityOnce(ctx, j)
		if err != nil {
			return fail("a sweep for rows written under the merged-away account stopped: "+failText(err), err)
		}
		if !more {
			clean = true
			break
		}
	}
	if !clean {
		more, err := s.hasStragglers(ctx, j)
		switch {
		case err != nil:
			return fail("the final straggler check failed: "+failText(err), err)
		case more:
			return fail("rows were still appearing under the merged-away account after the final sweep", errors.New("stragglers remain"))
		}
	}
	if j.Core != nil {
		j.Core.SweepPending = false
	}
	if err := s.save(j); err != nil {
		if j.Core != nil {
			j.Core.SweepPending = true
		}
		s.log.Error("merge %s: recording the clean straggler sweep failed (sweep_pending stays set in the committed journal): %v", j.ID, err)
		return &SweepError{JournalID: j.ID, Cause: err}
	}
	return nil
}

func (s *Service) sweepIdentityOnce(ctx context.Context, j *Journal) (bool, error) {
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	ops, err := s.stragglerOps(ctx, j)
	if err != nil || len(ops) == 0 {
		return false, err
	}
	tagUnits(ops, "s"+strconv.Itoa(len(j.Ops)))
	unlock := database.LockUserBookStates(lockKeys(ops, j.SurvivorID, j.AbsorbedID)...)
	defer unlock()
	j.Ops = append(j.Ops, ops...)
	j.Sweeps++
	return true, s.applyRemaining(ctx, j, nil)
}

// identityStragglerOps is stragglerOps over the families books owns.
func (s *Service) identityStragglerOps(ctx context.Context, j *Journal) ([]database.UserMergeOp, error) {
	if !s.identityReady() {
		return nil, ErrUnavailableUnderIdentity
	}
	d, err := s.idMerger.ScanBooksUserData(j.AbsorbedID)
	if err != nil {
		return nil, err
	}
	b := newBuilder(s, &Plan{Counts: map[string]FamilyCount{}})
	if err := b.planIdentityData(ctx, j.SurvivorID, j.AbsorbedID, d); err != nil {
		return nil, err
	}
	if len(b.refused) > 0 {
		return nil, fmt.Errorf("%w: %d row(s) the merge can no longer read or does not own", ErrJournalState, len(b.refused))
	}
	return b.finalize(), nil
}

func conflictErr(err error, msg string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrDigestMismatch, msg)
}

// mapApplyError answers a failed apply with the error the callers map: a row
// or record that changed since the preview is ErrDigestMismatch (the stale
// answer HEAD gives), a core refusal is a RefusedError carrying its code, and
// anything else (a denial, a store failure) is returned as it is.
func (s *Service) mapApplyError(err error, plan *Plan) error {
	switch {
	case errors.Is(err, coreauth.ErrMergeConflict), errors.Is(err, storage.ErrConflict), errors.Is(err, database.ErrUserMergeConflict):
		return fmt.Errorf("%w (%w)", ErrDigestMismatch, err)
	}
	if code, text, ok := refusalOf(err); ok {
		refused := *plan
		refused.Refusals = append(slices.Clone(plan.Refusals), text)
		refused.RefusalCodes = append(slices.Clone(plan.RefusalCodes), code)
		return &RefusedError{Plan: &refused}
	}
	return err
}
