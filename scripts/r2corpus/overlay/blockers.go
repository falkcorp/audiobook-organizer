// file: scripts/r2corpus/overlay/blockers.go
// version: 1.1.0
// guid: 7f2a9d4c-1e6b-4b83-9d05-8c3e6a1b4f72
// last-edited: 2026-11-10

//go:build r2corpus_overlay

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// The blocker corpus (09 §9.2.3 "The cutover preflight"; 05-storage §5S.3.13
// "Cutover blockers"): each case in its own copy of stage 1, so a test can
// boot it alone and see the preflight refuse that one row.

// ── identity rows M3 cannot convert ────────────────────────────────────────

func writeBlockerCorruptUser(w *writer, label string) {
	id := newID()
	w.setTorn("u:"+id, `{"id":"`+id+`","username":"jervis.tetch","email":"","password_hash_algo":"argon2id","password_hash":"$argon2id$v=19$m=65536,t=3,p=2$`)
}

func writeBlockerCorruptAPIKey(w *writer, label string) {
	id := newID()
	owner := mustUser(w.db, "lucius.fox")
	w.setTorn("apikey:"+id, `{"id":"`+id+`","user_id":"`+owner.ID+`","name":"mad-hatter","description":"","token_hash":"`)
}

func writeBlockerCorruptOAuthID(w *writer, label string) {
	id := newID()
	owner := mustUser(w.db, "barbara.gordon")
	w.setTorn("oauthid:"+id, `{"id":"`+id+`","provider":"github","subject":"spoiler-1992","email":"oracle@clocktower.invalid","user_id":"`+owner.ID)
}

// writeBlockerUserRejected writes a user row that decodes but that
// auth.Importer must reject: it has no username (the native store indexes
// every user by its folded name). HEAD's handlers refuse an empty name; its
// store writer does not, so this is what an out-of-band write leaves.
func writeBlockerUserRejected(w *writer, label string) {
	w.createUser("", []string{"viewer"}, "active")
}

// writeBlockerAPIKeyOrphan writes a decodable key whose owner does not exist.
func writeBlockerAPIKeyOrphan(w *writer, label string) {
	ghost := &database.User{ID: newID()}
	w.createKey(ghost, "owner-gone", []string{"library.view"}, "active")
}

// writeBlockerOAuthIDOrphan writes a decodable identity whose user does not
// exist.
func writeBlockerOAuthIDOrphan(w *writer, label string) {
	ghost := newID()
	_, err := w.db.CreateOAuthIdentity(&database.OAuthIdentity{
		Provider: "google", Subject: "red-hood-1988", Email: "red.hood@crime-alley.invalid", UserID: ghost,
	})
	must(err, "create orphan oauth identity")
}

// ── merge journals (05-authn §11 "Merges at the cutover") ──────────────────

// mergePair creates a survivor and an absorbed account; the absorbed one holds
// an API key, an SSO identity and an invite it sent, which a merge re-owns
// (so the journal's images hold apikey:, oauthid: and invite: keys).
func (w *writer) mergePair(survivor, absorbed string) (*database.User, *database.User) {
	// HEAD merges only into an admin account (usermerge plan: "the surviving
	// account is not an admin").
	s := w.createUser(survivor, []string{"admin"}, "active")
	a := w.createUser(absorbed, []string{"viewer"}, "active")
	w.createKey(a, absorbed+"-script", []string{"library.view"}, "active")
	_, err := w.db.CreateOAuthIdentity(&database.OAuthIdentity{
		Provider: "github", Subject: "merge-" + lower(a.ID), Email: absorbed + "@gotham.invalid", UserID: a.ID,
	})
	must(err, "identity for "+absorbed)
	_, err = w.db.CreateInvite(&database.Invite{
		Token: "r2corpus-merge-invite-" + lower(newID()), Username: absorbed + ".guest", RoleID: "viewer",
		CreatedByUserID: a.ID, ExpiresAt: time.Now().Add(10 * 365 * 24 * time.Hour),
	})
	must(err, "invite from "+absorbed)
	return s, a
}

// mergeService is HEAD's admin user-merge service over the store.
func (w *writer) mergeService() *usermerge.Service {
	svc, err := usermerge.New(w.db)
	must(err, "user merge service")
	return svc
}

// applyMerge runs HEAD's merge of absorbed into survivor. stopAt, when set, is
// the progress message at which the context is canceled, which leaves the
// journal where a crash or a restart at that point leaves it.
func (w *writer) applyMerge(svc *usermerge.Service, s, a *database.User, stopAt string) *usermerge.Journal {
	admin := mustUser(w.db, "admin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plan, err := svc.BuildPlan(ctx, usermerge.PlanRequest{ActorID: admin.ID, SurvivorID: s.ID, AbsorbedID: a.ID})
	must(err, "plan merge")
	prog := func(done, total int, msg string) {
		if stopAt != "" && msg == stopAt {
			cancel()
		}
	}
	j, err := svc.Apply(ctx, usermerge.ApplyRequest{
		ActorID: admin.ID, SurvivorID: s.ID, AbsorbedID: a.ID, Digest: plan.Digest, AcknowledgeOwnerIdentity: true,
	}, prog)
	if stopAt == "" {
		must(err, "apply merge")
	} else if !errors.Is(err, context.Canceled) {
		must(fmt.Errorf("the apply was to stop at %q, got %v", stopAt, err), "apply merge")
	}
	if j == nil {
		must(fmt.Errorf("no journal"), "apply merge")
	}
	return j
}

func journalStatus(w *writer, id string) string {
	j, err := w.mergeService().LoadJournal(id)
	must(err, "load journal")
	return j.Status
}

func expectJournal(w *writer, id, want string) {
	if got := journalStatus(w, id); got != want {
		must(fmt.Errorf("journal %s is %s, want %s", id, got, want), "merge journal")
	}
}

// writeBlockerMergeApplying cancels an apply at its first "merging" report, as
// a restart mid-merge leaves it: the journal is applying (resumable,
// reversible). The merge's few ops fit one chunk, so they are written and the
// absorbed account retired; the canceled context stops the straggler sweep.
func writeBlockerMergeApplying(w *writer, label string) {
	s, a := w.mergePair("harvey.dent", "two.face")
	j := w.applyMerge(w.mergeService(), s, a, "merging")
	expectJournal(w, j.ID, usermerge.StatusApplying)
}

// writeBlockerMergeUndoing applies a merge, then starts its undo with a context
// that is already canceled, as a restart right after the undo was started
// leaves it: Undo saves the journal as undoing, and undoOps stops before its
// first chunk (the merge's few ops fit one chunk, so a cancel after a chunk
// would let the undo finish).
func writeBlockerMergeUndoing(w *writer, label string) {
	s, a := w.mergePair("dick.grayson.sr", "nightwing")
	svc := w.mergeService()
	j := w.applyMerge(svc, s, a, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Undo(ctx, usermerge.UndoRequest{JournalID: j.ID}, nil)
	if !errors.Is(err, context.Canceled) {
		must(fmt.Errorf("the undo was to stop part-way, got %v", err), "undo merge")
	}
	expectJournal(w, j.ID, usermerge.StatusUndoing)
}

// writeBlockerMergeApplied is a finished merge: the control that must not
// block.
func writeBlockerMergeApplied(w *writer, label string) {
	s, a := w.mergePair("jean.paul.valley", "azrael")
	j := w.applyMerge(w.mergeService(), s, a, "")
	expectJournal(w, j.ID, usermerge.StatusApplied)
}

// writeBlockerMergeUndone is a merge applied and then undone in full: the
// other terminal state, also a control.
func writeBlockerMergeUndone(w *writer, label string) {
	s, a := w.mergePair("tim.drake", "red.robin")
	svc := w.mergeService()
	j := w.applyMerge(svc, s, a, "")
	_, err := svc.Undo(context.Background(), usermerge.UndoRequest{JournalID: j.ID}, nil)
	must(err, "undo merge")
	expectJournal(w, j.ID, usermerge.StatusUndone)
}

// ── the case lists ─────────────────────────────────────────────────────────

var blockerCases = []blockerCase{
	{"blocker-identity-corrupt-user", "blocker-identity-corrupt-user", writeBlockerCorruptUser},
	{"blocker-identity-corrupt-apikey", "blocker-identity-corrupt-apikey", writeBlockerCorruptAPIKey},
	{"blocker-identity-corrupt-oauthid", "blocker-identity-corrupt-oauthid", writeBlockerCorruptOAuthID},
	{"blocker-identity-user-rejected", "blocker-identity-user-rejected", writeBlockerUserRejected},
	{"blocker-identity-apikey-orphan", "blocker-identity-apikey-orphan", writeBlockerAPIKeyOrphan},
	{"blocker-identity-oauthid-orphan", "blocker-identity-oauthid-orphan", writeBlockerOAuthIDOrphan},
	{"blocker-merge-applying", "blocker-merge-applying", writeBlockerMergeApplying},
	{"blocker-merge-undoing", "blocker-merge-undoing", writeBlockerMergeUndoing},
	{"blocker-merge-applied", "blocker-merge-terminal", writeBlockerMergeApplied},
	{"blocker-merge-undone", "blocker-merge-terminal", writeBlockerMergeUndone},
}

var overlayCases = []overlayCase{
	// M2
	{"corrupt-settings-secret", writeCorruptSettingsSecret},
	{"blob-field-unmapped", writeBlobFieldUnmapped},
	// M3
	{"role-every-atom", writeRoleEveryAtom},
	{"role-subset", writeRoleSubset},
	{"role-unknown-atom", writeRoleUnknownAtom},
	{"role-all-unknown", writeRoleAllUnknown},
	{"corrupt-role", writeCorruptRole},
	{"corrupt-inviteallow", writeCorruptInviteAllow},
	{"key-outside-owner", writeKeyOutsideOwner},
	{"key-every-atom-custom-owner", writeKeyEveryAtomCustomOwner},
	{"key-empty-custom-owner", writeKeyEmptyCustomOwner},
	{"status-edge-user-padded-active", writeUserStatusEdge},
	{"status-edge-user-upper-disabled", writeUserStatusEdge},
	{"status-edge-user-empty", writeUserStatusEdge},
	{"status-edge-user-padded-inactive", writeUserStatusEdge},
	{"status-edge-user-unknown", writeUserStatusEdge},
	{"status-edge-key-padded-active", writeKeyStatusEdge},
	{"status-edge-key-upper-inactive", writeKeyStatusEdge},
	{"status-edge-key-empty", writeKeyStatusEdge},
	{"status-edge-key-padded-revoked", writeKeyStatusEdge},
	{"status-edge-key-active-revoked-at", writeKeyStatusEdge},
	{"status-edge-key-unknown", writeKeyStatusEdge},
	{"status-edge-key-no-expiry", writeKeyStatusEdge},
	{"oauthid-linked", writeOAuthIdentity},
	{"inviteallow-accepted", writeInviteAllow},
	// M4
	{"run-running", writeRunRunning},
	{"run-stopping-shutdown", writeRunStopping},
	{"run-stopping-user", writeRunStopping},
	{"run-stopping-quiesce", writeRunStopping},
	{"run-stopping-timeout", writeRunStopping},
	{"run-stopping-watchdog", writeRunStopping},
	{"run-interrupted-crash", writeRunInterruptedCrash},
	{"run-interrupted-quiesced", writeRunInterruptedQuiesced},
	{"run-queued-deps", writeRunQueuedDeps},
	{"run-dropped", writeRunDropped},
	{"run-awaiting-decision", writeRunAwaitingDecision},
	{"run-interrupted-apply-rowmarks", writeRunInterruptedApplyRowMarks},
	{"run-standdown-marker", writeStandDownMarker},
	{"run-debounce-bucket", writeDebounceBucket},
	{"run-requires-stamps", writeRequiresStamps},
	{"corrupt-run", writeCorruptRun},
	// M5
	{"corrupt-syslog", writeCorruptSyslog},
	// M6
	{"integration-field-invalid", writeIntegrationFieldInvalid},
	{"corrupt-import-root", writeCorruptImportRoot},
	{"corrupt-aijob", writeCorruptAIJob},
	{"aijob-pending", writeAIJob},
	{"aijob-submitted", writeAIJob},
	{"aijob-completed-with-errors", writeAIJob},
	{"aijob-failed", writeAIJob},
}

// writeCorruptRole is a role row that does not decode (05-authn §11: a role
// row is quarantined, never a blocker).
func writeCorruptRole(w *writer, label string) {
	id := newID()
	w.setTorn("role:"+id, `{"id":"`+id+`","name":"Court of Owls","permissions":["library.view","library.ed`)
}

// writeCorruptInviteAllow is an invite allow-list row that does not decode:
// the preflight does not read the allow-list, and M3 quarantines it (09
// §9.2.3).
func writeCorruptInviteAllow(w *writer, label string) {
	key := "inviteallow:owlman@court-of-owls.invalid"
	w.setTorn(key, `{"user_id":"`+newID()+`","granted_at":"2026-11-10T`)
}
