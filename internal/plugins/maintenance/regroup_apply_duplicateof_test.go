// file: internal/plugins/maintenance/regroup_apply_duplicateof_test.go
// version: 1.2.0
// guid: 3d81b47a-52e9-4c6f-9a13-8be07f2c65d1
// last-edited: 2026-09-02

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// fakeCandidates is a stand-in for the dedup track's candidate rows: the ONLY
// place the "this folder duplicates that book" relationship is recorded.
//
// It is STATUS-AWARE, like the real store: a non-empty status argument returns
// only rows in that status. The earlier version of this fake ignored the
// argument, which is exactly why the "" status passed by ApplyDuplicateOf went
// unnoticed — a status-blind fake cannot observe a status-blind caller.
type fakeCandidates struct {
	byEntity map[string][]database.DedupCandidate
	err      error
	// ignoreStatus makes the fake behave like a lister that does not filter by
	// status at all, so a test can prove the caller's own veto holds even when
	// the store's filter is absent.
	ignoreStatus bool
}

func (f *fakeCandidates) ListCandidatesForEntity(_, entityID, status string) ([]database.DedupCandidate, error) {
	if f.err != nil {
		return nil, f.err
	}
	rows := f.byEntity[entityID]
	if status == "" || f.ignoreStatus {
		return rows, nil
	}
	var out []database.DedupCandidate
	for _, c := range rows {
		if c.Status == status {
			out = append(out, c)
		}
	}
	return out, nil
}

// pendingCand is a live candidate row: the only kind that may nominate.
func pendingCand(a, b string) database.DedupCandidate {
	return database.DedupCandidate{EntityType: "book", EntityAID: a, EntityBID: b, Status: "pending"}
}

func duplicateOfItem(t *testing.T, folder string, memberIDs []string) database.ReviewItem {
	t.Helper()
	payload, err := json.Marshal(regroupPayload{
		Folder:        folder,
		MemberBookIDs: memberIDs,
		SurvivorTitle: "Canonical",
		Confidence:    "review",
	})
	require.NoError(t, err)
	return database.ReviewItem{
		ID:        ulid.Make().String(),
		Kind:      "regroup.ambiguous",
		FolderRef: folder,
		Payload:   string(payload),
	}
}

func mkDupBook(t *testing.T, store database.Store, id, title, path string) {
	t.Helper()
	_, err := store.CreateBook(&database.Book{ID: id, Title: title, Format: "mp3", FilePath: path})
	require.NoError(t, err)
}

// The happy path: the dedup track names exactly one book outside the folder, so the
// debris merges INTO it via CombineBooks — the same call combine uses. The canonical
// book must survive even though it is not the smallest ULID; the survivor is chosen
// by the duplicate relationship, NOT by pickPrimary.
func TestApplyDuplicateOf_MergesDebrisIntoCanonicalBook(t *testing.T) {
	store := newApplyTestStore(t)

	// "zzz" prefix guarantees the canonical book is NOT the smallest ID, so if the
	// implementation fell back to pickPrimary this test fails.
	canonical := "zzz" + ulid.Make().String()
	debris1 := "aaa" + ulid.Make().String()
	debris2 := "aab" + ulid.Make().String()
	mkDupBook(t, store, canonical, "The Real Book", "/lib/real/book.mp3")
	mkDupBook(t, store, debris1, "Debris One", "/lib/junk/d1.mp3")
	mkDupBook(t, store, debris2, "Debris Two", "/lib/junk/d2.mp3")

	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{
		debris1: {pendingCand(debris1, canonical)},
		debris2: {pendingCand(canonical, debris2)},
	}}

	apply := ApplyDuplicateOf(store, merge.NewService(store), cands)
	err := apply(context.Background(), duplicateOfItem(t, "/lib/junk", []string{debris1, debris2}))
	require.NoError(t, err)

	// Canonical survives...
	got, err := store.GetBookByID(canonical)
	require.NoError(t, err)
	require.NotNil(t, got, "the canonical book must be the survivor")
	require.False(t, got.IsSoftDeleted(), "the canonical book must not be merged away")

	// ...and the debris is gone (hard-deleted or soft-deleted by CombineBooks).
	for _, id := range []string{debris1, debris2} {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		if b != nil {
			require.True(t, b.IsSoftDeleted(), "debris %s should be merged away", id)
		}
	}
}

// Zero nominations: the dedup track has no opinion, so there is nothing to merge
// into. This MUST error rather than return nil — a nil return marks the hold
// "applied", and "decided" is sticky, so the debris would stay on disk forever with
// the queue claiming it was handled.
func TestApplyDuplicateOf_NoCanonicalBookIsAnError(t *testing.T) {
	store := newApplyTestStore(t)
	debris := ulid.Make().String()
	mkDupBook(t, store, debris, "Lonely Debris", "/lib/junk/d1.mp3")

	apply := ApplyDuplicateOf(store, merge.NewService(store), &fakeCandidates{})
	err := apply(context.Background(), duplicateOfItem(t, "/lib/junk", []string{debris}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "names no book outside the folder")
}

// Two different nominations: merging would hard-delete rows on a guess. Refuse and
// name both, mirroring ApplyVersionGroup's cross-group refusal.
func TestApplyDuplicateOf_AmbiguousTargetRefuses(t *testing.T) {
	store := newApplyTestStore(t)
	debris := ulid.Make().String()
	canonA, canonB := "zza"+ulid.Make().String(), "zzb"+ulid.Make().String()
	mkDupBook(t, store, debris, "Debris", "/lib/junk/d1.mp3")
	mkDupBook(t, store, canonA, "Candidate A", "/lib/a/a.mp3")
	mkDupBook(t, store, canonB, "Candidate B", "/lib/b/b.mp3")

	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{
		debris: {pendingCand(debris, canonA), pendingCand(debris, canonB)},
	}}

	apply := ApplyDuplicateOf(store, merge.NewService(store), cands)
	err := apply(context.Background(), duplicateOfItem(t, "/lib/junk", []string{debris}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")

	// Nothing was merged — both nominees and the debris all still live.
	for _, id := range []string{debris, canonA, canonB} {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b, "%s must be untouched by a refused apply", id)
		require.False(t, b.IsSoftDeleted(), "%s must be untouched by a refused apply", id)
	}
}

// A candidate row can outlive the book it names. Merging into a corpse would move
// the debris onto a soft-deleted survivor, losing all of it.
func TestApplyDuplicateOf_StaleCandidateTargetRefuses(t *testing.T) {
	store := newApplyTestStore(t)
	debris := ulid.Make().String()
	ghost := "zzz" + ulid.Make().String()
	mkDupBook(t, store, debris, "Debris", "/lib/junk/d1.mp3")
	// ghost is named by the candidate row but was never created.

	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{
		debris: {pendingCand(debris, ghost)},
	}}

	apply := ApplyDuplicateOf(store, merge.NewService(store), cands)
	err := apply(context.Background(), duplicateOfItem(t, "/lib/junk", []string{debris}))
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "gone or merged away"),
		"want a stale-candidate refusal, got: %v", err)

	b, err := store.GetBookByID(debris)
	require.NoError(t, err)
	require.NotNil(t, b, "debris must survive a refused apply")
}

// Members that reference each other must not nominate each other as the canonical
// book — the survivor has to live OUTSIDE the folder. Without the member filter a
// two-piece folder would merge into itself and report success.
func TestApplyDuplicateOf_MembersDoNotNominateEachOther(t *testing.T) {
	store := newApplyTestStore(t)
	d1, d2 := "aaa"+ulid.Make().String(), "aab"+ulid.Make().String()
	mkDupBook(t, store, d1, "Debris One", "/lib/junk/d1.mp3")
	mkDupBook(t, store, d2, "Debris Two", "/lib/junk/d2.mp3")

	// The only candidate row links the two members to each other.
	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{
		d1: {pendingCand(d1, d2)},
		d2: {pendingCand(d1, d2)},
	}}

	apply := ApplyDuplicateOf(store, merge.NewService(store), cands)
	err := apply(context.Background(), duplicateOfItem(t, "/lib/junk", []string{d1, d2}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "names no book outside the folder")
}

// A human dismissed the pair. That is the human saying "these are not the same
// book", and the apply must not turn it into a merge — even though the row still
// names the canonical book. Before the fix the nomination path asked the store
// for candidates in status "" (any status) and this merged over the dismissal.
func TestApplyDuplicateOf_DismissedCandidateDoesNotNominate(t *testing.T) {
	store := newApplyTestStore(t)

	canonical := "zzz" + ulid.Make().String()
	debris := "aaa" + ulid.Make().String()
	mkDupBook(t, store, canonical, "The Real Book", "/lib/real/book.mp3")
	mkDupBook(t, store, debris, "Debris", "/lib/junk/d1.mp3")

	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{
		debris: {{EntityType: "book", EntityAID: debris, EntityBID: canonical, Status: "dismissed"}},
	}}

	apply := ApplyDuplicateOf(store, merge.NewService(store), cands)
	err := apply(context.Background(), duplicateOfItem(t, "/lib/junk", []string{debris}))
	require.Error(t, err, "a dismissed candidate is not evidence of a duplicate")
	require.Contains(t, err.Error(), "names no book outside the folder")

	// Nothing was merged: both books still exist, untouched.
	for _, id := range []string{canonical, debris} {
		got, gerr := store.GetBookByID(id)
		require.NoError(t, gerr)
		require.NotNil(t, got, "book %s must survive — dismissed means do NOT merge", id)
	}
}

// candidateMayNominate must agree with the store about what a verdict is. The
// vocabulary is pending|dismissed|merged|stale-fp|stale-drain (embedding_store.go);
// a hand-copied veto set once named "rejected" and "separate" instead, which no
// writer produces, so this pins the gate to database.IsTerminalCandidateStatus
// rather than to a list of strings.
func TestCandidateMayNominate_AgreesWithStoreVocabulary(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{"pending", true},
		{"dismissed", false},
		{"merged", false},
		{"stale-fp", false},
		{"stale-drain", false},
		{"", false},
		{"rejected", false}, // not in the vocabulary; an unknown status is inert
	} {
		got := candidateMayNominate(database.DedupCandidate{Status: tc.status})
		require.Equal(t, tc.want, got, "status %q", tc.status)
		if database.IsTerminalCandidateStatus(tc.status) {
			require.False(t, got, "a terminal status (%q) is a verdict and must never nominate", tc.status)
		}
	}
}

// The veto must hold in Go, not only in the store query. A lister that ignores
// the status argument (or a row whose status flipped between the query and the
// read) hands back every row; the caller must still refuse the dismissed one.
// The statuses here are the store's real vocabulary minus "pending".
func TestApplyDuplicateOf_VetoHoldsWhenListerIgnoresStatus(t *testing.T) {
	store := newApplyTestStore(t)

	canonical := "zzz" + ulid.Make().String()
	debris := "aaa" + ulid.Make().String()
	mkDupBook(t, store, canonical, "The Real Book", "/lib/real/book.mp3")
	mkDupBook(t, store, debris, "Debris", "/lib/junk/d1.mp3")

	for _, status := range []string{"dismissed", "merged", "stale-fp", "stale-drain", ""} {
		t.Run("status="+status, func(t *testing.T) {
			cands := &fakeCandidates{
				ignoreStatus: true,
				byEntity: map[string][]database.DedupCandidate{
					debris: {{EntityType: "book", EntityAID: debris, EntityBID: canonical, Status: status}},
				},
			}
			apply := ApplyDuplicateOf(store, merge.NewService(store), cands)
			err := apply(context.Background(), duplicateOfItem(t, "/lib/junk", []string{debris}))
			require.Error(t, err, "status %q must not nominate", status)
			require.Contains(t, err.Error(), "names no book outside the folder")
		})
	}

	// And the same lister with a live row DOES nominate, so the veto is not
	// simply "nothing ever nominates".
	live := &fakeCandidates{
		ignoreStatus: true,
		byEntity:     map[string][]database.DedupCandidate{debris: {pendingCand(debris, canonical)}},
	}
	apply := ApplyDuplicateOf(store, merge.NewService(store), live)
	require.NoError(t, apply(context.Background(), duplicateOfItem(t, "/lib/junk", []string{debris})))
	got, err := store.GetBookByID(debris)
	require.NoError(t, err)
	require.Nil(t, got, "the live pending candidate must still merge the debris away")
}
