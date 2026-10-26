// file: internal/server/user_data_access_wiring_test.go
// version: 1.1.0
// guid: b8b6eb72-5710-4aa4-bcd9-b827d8deba3d
// last-edited: 2026-10-26

// The library.view routes that serve records holding other users' listening
// state alongside library data, as the server wires them: the merge journals
// (GET /merge/combine-journal, /merge/sibling-journal) and the activity log
// (GET /activity). Each keeps the record and strips the per-user part for a
// caller who may not manage users; the admin reads all of it, which is the
// oracle that the server installed the check (the handlers strip for
// everyone when it is missing).

package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

const (
	mergeListener       = "invented-merge-listener"       // in each journal's progress
	mergeListenerWarned = "invented-merge-listener-noted" // in a journal warning naming a user
	combineJournalID    = "01JINVENTEDCOMBINEJOURNAL01"
	siblingJournalID    = "01JINVENTEDSIBLINGJOURNAL01"
	purgeSummary        = "invented-reader: 42%, at 1:02:03"
)

// seedMergeJournals writes one combine and one sibling-move journal, each
// carrying mergeListener's state.
func seedMergeJournals(t *testing.T, f *repairsOpAccessFixture) {
	t.Helper()
	store := f.srv.storeForWiring()
	state := &database.UserBookState{UserID: mergeListener, BookID: "book-invented-absorbed", Status: "finished", ProgressPct: 100}
	progress := []merge.CombineUserProgress{{UserID: mergeListener, AbsorbedState: state, SurvivorStateBefore: state}}
	cj := merge.CombineJournal{
		ID: combineJournalID, Status: "applied", SurvivorID: "book-invented-survivor",
		Absorbed: []merge.CombineAbsorbed{{BookID: "book-invented-absorbed", Progress: progress}},
		Warnings: []string{
			"progress of user " + mergeListenerWarned + " on book-invented-absorbed skipped: invented",
			"absorbed book book-invented-gone vanished after its files moved",
		},
	}
	sj := merge.SiblingMoveJournal{
		ID: siblingJournalID, Status: merge.SiblingJournalApplied, SurvivorID: "book-invented-survivor",
		Siblings:     []merge.MovedSibling{{BookID: "book-invented-sibling"}},
		StateFollows: []merge.StateFollow{{LoserID: "book-invented-loser", HolderID: "book-invented-sibling", Progress: progress}},
	}
	for key, v := range map[string]any{"merge:combine-journal:" + combineJournalID: cj, "merge:sibling-journal:" + siblingJournalID: sj} {
		data, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, store.SetRaw(key, data))
	}
}

// TestMergeJournals_ProgressIsAdminOnly: the viewer and the editor list both
// journals with their structure (ids, books, siblings, the warning that names
// no user) and without any user's progress or a warning naming a user; the
// admin reads the progress too.
// Not parallel: it swaps the process-wide config.
func TestMergeJournals_ProgressIsAdminOnly(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	seedMergeJournals(t, f)
	for _, tc := range []struct {
		who, token string
		seesUsers  bool
	}{
		{"viewer", f.viewer, false},
		{"editor", f.editor, false},
		{"admin", f.admin, true},
	} {
		for path, keep := range map[string][]string{
			"/api/v1/merge/combine-journal?limit=0": {combineJournalID, "book-invented-absorbed", "vanished after its files moved"},
			"/api/v1/merge/sibling-journal":         {siblingJournalID, "book-invented-loser", "book-invented-sibling"},
		} {
			w := getAs(f.srv, tc.token, path)
			require.Equal(t, 200, w.Code, "%s %s: %s", tc.who, path, w.Body.String())
			body := w.Body.String()
			for _, k := range keep {
				require.Contains(t, body, k, "%s %s: the journal's structure stays", tc.who, path)
			}
			if tc.seesUsers {
				require.Contains(t, body, mergeListener, "%s %s", tc.who, path)
				require.Contains(t, body, `"progress":[`, "%s %s", tc.who, path)
				continue
			}
			require.NotContains(t, body, mergeListener, "%s %s", tc.who, path)
			require.NotContains(t, body, `"progress"`, "%s %s", tc.who, path)
		}
	}
	w := getAs(f.srv, f.admin, "/api/v1/merge/combine-journal?limit=0")
	require.Contains(t, w.Body.String(), mergeListenerWarned, "the admin reads the warning naming a user")
}

// TestActivity_TrashPurgeProgressIsAdminOnly: the trash purge's activity row
// records every user's discarded progress; the viewer and the editor read the
// row without it, the admin with it.
// Not parallel: it swaps the process-wide config.
func TestActivity_TrashPurgeProgressIsAdminOnly(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	require.NotNil(t, f.srv.activityService, "the test server has an activity log")
	require.NoError(t, f.srv.activityService.Record(database.ActivityEntry{
		Timestamp: time.Now().UTC(), Tier: "audit", Type: activity.TypeTrashDiscardProgressPurge, Level: "info",
		Source: "trash", BookID: "book-invented-purged",
		Summary: "discarded listening progress and purged \"Invented Purged Book\" from the trash (1 user(s), 0 bookmark(s))",
		Details: map[string]any{
			"book_id":                      "book-invented-purged",
			activity.DetailProgressSummary: purgeSummary,
			"users_cleared":                1,
		},
	}))
	path := "/api/v1/activity?type=" + activity.TypeTrashDiscardProgressPurge
	for _, tc := range []struct {
		who, token string
		seesUsers  bool
	}{
		{"viewer", f.viewer, false},
		{"editor", f.editor, false},
		{"admin", f.admin, true},
	} {
		w := getAs(f.srv, tc.token, path)
		require.Equal(t, 200, w.Code, "%s: %s", tc.who, w.Body.String())
		body := w.Body.String()
		require.Contains(t, body, "book-invented-purged", "%s: the row stays", tc.who)
		require.Contains(t, body, `"users_cleared":1`, "%s: the counts stay", tc.who)
		if tc.seesUsers {
			require.Contains(t, body, purgeSummary, tc.who)
			continue
		}
		require.NotContains(t, body, purgeSummary, tc.who)
		require.NotContains(t, body, activity.DetailProgressSummary, tc.who)
	}
}

// TestActivity_AccountAuditRowsAreAdminOnly: the audit rows that name other
// accounts (email displacement, guest service tokens, user merges) are withheld
// from the viewer and the editor whatever the filter, tier or type they ask
// for, and the admin reads them; the audit rows about a book (the admin debug
// edit, the trash purge) stay visible but lose the actor's username. Before
// this the "account <id>" summaries and the account ids in Details went to every
// library.view caller (tier=audit, type=auth_email_displaced).
// Not parallel: it swaps the process-wide config.
func TestActivity_AccountAuditRowsAreAdminOnly(t *testing.T) {
	const (
		acct    = "01INVENTEDACCOUNT00000000A"
		cfText  = "invented-cloudflare-refusal-text"
		actorNm = "selina.kyle (01INVENTEDUSER0000000000A)"
	)
	f := newRepairsOpAccessFixture(t)
	require.NotNil(t, f.srv.activityService, "the test server has an activity log")
	rec := func(typ, source, summary string, details map[string]any) {
		t.Helper()
		require.NoError(t, f.srv.activityService.Record(database.ActivityEntry{
			Timestamp: time.Now().UTC(), Tier: "audit", Type: typ, Level: "info", Source: source,
			Summary: summary, Details: details, BookID: "book-invented-hush",
		}))
	}
	accountRows := map[string]string{
		activity.TypeAuthEmailDisplaced:                        "their email was cleared and account " + acct + " was created",
		activity.TypeGuestServiceTokenPrefix + "minted":        "a per-guest token was minted (account " + acct + ")",
		activity.TypeGuestServiceTokenPrefix + "mint_failed":   "a per-guest token could not be minted (account " + acct + ")",
		activity.TypeGuestServiceTokenPrefix + "revoked":       "a per-guest token was revoked (account " + acct + ")",
		activity.TypeGuestServiceTokenPrefix + "revoke_failed": "revoking a token failed (account " + acct + ")",
		activity.TypeUserMerge:                                 "merged the first account into the second",
	}
	for typ, summary := range accountRows {
		rec(typ, "invite", summary, map[string]any{"user_id": acct, "detail": cfText})
	}
	rec(activity.TypeAdminDebugEdit, "admin_debug", "admin debug edit: book book-invented-hush (title)", map[string]any{"actor": actorNm, "entity_id": "book-invented-hush"})
	rec(activity.TypeAdminDebugUndo, "admin_debug", "admin debug undo of edit e1: book book-invented-hush (title)", map[string]any{"actor": actorNm, "entity_id": "book-invented-hush"})
	rec(activity.TypeTrashDiscardProgressPurge, "trash", "discarded listening progress and purged \"Hush\" from the trash (1 user(s), 0 bookmark(s))", map[string]any{"actor": actorNm, "book_id": "book-invented-hush"})

	paths := []string{
		"/api/v1/activity?tier=audit&limit=100",
		"/api/v1/activity?type=" + activity.TypeAuthEmailDisplaced,
		"/api/v1/activity?type=" + activity.TypeGuestServiceTokenPrefix + "minted",
		"/api/v1/activity?book_id=book-invented-hush&limit=100",
		"/api/v1/activity?search=account&limit=100",
	}
	for _, path := range paths {
		for _, tc := range []struct {
			who, token string
			sees       bool
		}{{"viewer", f.viewer, false}, {"editor", f.editor, false}, {"admin", f.admin, true}} {
			w := getAs(f.srv, tc.token, path)
			require.Equal(t, 200, w.Code, "%s %s: %s", tc.who, path, w.Body.String())
			body := w.Body.String()
			if tc.sees {
				if path == paths[0] {
					require.Contains(t, body, acct, "admin %s", path)
					require.Contains(t, body, cfText, "admin %s", path)
					require.Contains(t, body, actorNm, "admin %s", path)
				}
				continue
			}
			require.NotContains(t, body, acct, "%s %s", tc.who, path)
			require.NotContains(t, body, cfText, "%s %s", tc.who, path)
			require.NotContains(t, body, actorNm, "%s %s: the actor's username", tc.who, path)
			for typ := range accountRows {
				require.NotContains(t, body, `"`+typ+`"`, "%s %s: the %s row is withheld", tc.who, path, typ)
			}
			if path == paths[0] || path == paths[3] {
				require.Contains(t, body, activity.TypeAdminDebugEdit, "%s %s: the book audit row stays", tc.who, path)
				require.Contains(t, body, activity.TypeTrashDiscardProgressPurge, "%s %s: the book audit row stays", tc.who, path)
			}
		}
	}
}
