// file: internal/database/pebble_store_narrator_fold_index_test.go
// version: 1.0.0
// guid: 1185cd5a-8949-4471-826b-d4302ed5756b
// last-edited: 2026-10-30

package database

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Fixtures are Batman comics and characters only.

func nfNarratorCount(t *testing.T, p *PebbleStore) int {
	t.Helper()
	all, err := p.ListNarrators()
	require.NoError(t, err)
	return len(all)
}

func nfBook(t *testing.T, p *PebbleStore, title, credit string) *Book {
	t.Helper()
	b, err := p.CreateBook(&Book{Title: title, FilePath: "/lib/batman/" + title + ".m4b", Narrator: &credit})
	require.NoError(t, err)
	return b
}

func nfCreditedIDs(t *testing.T, p *PebbleStore, bookID string) []int {
	t.Helper()
	rows, err := p.GetBookNarrators(bookID)
	require.NoError(t, err)
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.NarratorID)
	}
	return ids
}

// nfFoldKeys is every narrator_fold: key as stored, sorted.
func nfFoldKeys(t *testing.T, p *PebbleStore) []string {
	t.Helper()
	var keys []string
	lower := []byte(narratorFoldPrefix)
	require.NoError(t, forEachKeyInRange(p.db, lower, prefixEnd(lower), func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}))
	sort.Strings(keys)
	return keys
}

// nfTruthKeys is the exact key set the narrator rows imply.
func nfTruthKeys(t *testing.T, p *PebbleStore) []string {
	t.Helper()
	all, err := p.ListNarrators()
	require.NoError(t, err)
	var keys []string
	for _, n := range all {
		if fold := util.FoldKey(n.Name); fold != "" {
			keys = append(keys, string(narratorFoldKey(fold, n.ID)))
		}
	}
	sort.Strings(keys)
	return keys
}

func requireFoldIndexMatchesRows(t *testing.T, p *PebbleStore, step string) {
	t.Helper()
	require.Equal(t, nfTruthKeys(t, p), nfFoldKeys(t, p), "narrator_fold index out of sync after: %s", step)
}

// forBothTrustStates runs fn with the lookup scanning the records (the state
// before a boot reconcile finishes) and reading the index (after).
func forBothTrustStates(t *testing.T, fn func(t *testing.T, p *PebbleStore)) {
	t.Helper()
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprintf("trusted=%v", trusted), func(t *testing.T) {
			p := newNarratorSyncTestStore(t)
			if trusted {
				_, err := p.ReconcileNarratorFoldIndex(context.Background())
				require.NoError(t, err)
				require.True(t, p.NarratorFoldIndexTrusted())
			}
			fn(t, p)
		})
	}
}

// A rescan that reads an unaccented tag back must link to the accented record.
func TestNarratorFold_UnaccentedTagLinksToAccentedRecord(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		existing, err := p.CreateNarrator("Renée Montoya")
		require.NoError(t, err)

		b := nfBook(t, p, "Gotham Central", "Renee Montoya")

		require.Equal(t, 1, nfNarratorCount(t, p), "no second record for the unaccented spelling")
		require.Equal(t, []int{existing.ID}, nfCreditedIDs(t, p, b.ID))
		require.Equal(t, []string{"Renée Montoya"}, narratorNamesOf(t, p, b.ID), "the record keeps its spelling")
		requireFoldIndexMatchesRows(t, p, "link")
	})
}

func TestNarratorFold_HyphenAndSpaceVariantLinks(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		existing, err := p.CreateNarrator("Jean-Paul Valley")
		require.NoError(t, err)

		b := nfBook(t, p, "Knightfall", "Jean Paul Valley")
		require.Equal(t, []int{existing.ID}, nfCreditedIDs(t, p, b.ID))
		require.Equal(t, 1, nfNarratorCount(t, p))

		// And the other direction: the space spelling is the record.
		other, err := p.CreateNarrator("Harvey Bullock")
		require.NoError(t, err)
		b2 := nfBook(t, p, "Batman Year One", "Harvey-Bullock")
		require.Equal(t, []int{other.ID}, nfCreditedIDs(t, p, b2.ID))
		require.Equal(t, 2, nfNarratorCount(t, p))
	})
}

// A cast keeps its order and roles when some names fold onto records and
// others do not.
func TestNarratorFold_MixedCastKeepsOrderAndCreatesOnlyTheNewName(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		montoya, err := p.CreateNarrator("Renée Montoya")
		require.NoError(t, err)

		b := nfBook(t, p, "The Long Halloween", "Selina Kyle, Renee Montoya")

		ids := nfCreditedIDs(t, p, b.ID)
		require.Len(t, ids, 2)
		require.Equal(t, montoya.ID, ids[1])
		require.Equal(t, 2, nfNarratorCount(t, p), "only Selina Kyle was created")
		require.Equal(t, []string{"Selina Kyle", "Renée Montoya"}, narratorNamesOf(t, p, b.ID))
		requireFoldIndexMatchesRows(t, p, "mixed cast")
	})
}

// Several records fold together: the exact normalized match wins.
func TestNarratorFold_ExactMatchWinsAmongFoldMatches(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		accented, err := p.CreateNarrator("Renée Montoya")
		require.NoError(t, err)
		plain, err := p.CreateNarrator("Renee Montoya")
		require.NoError(t, err)
		require.NotEqual(t, accented.ID, plain.ID)

		got, err := p.resolveNarratorFolded("renee  MONTOYA")
		require.NoError(t, err)
		require.Equal(t, plain.ID, got.ID, "exact normalized spelling wins")
		got, err = p.resolveNarratorFolded("Renée Montoya")
		require.NoError(t, err)
		require.Equal(t, accented.ID, got.ID)
		require.Equal(t, 2, nfNarratorCount(t, p))
	})
}

// Several records fold together and none is exact: the record with the most
// credits wins, lowest id on a tie.
func TestNarratorFold_AmbiguousFoldPicksMostCredits(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		accented, err := p.CreateNarrator("Renée Montoya")
		require.NoError(t, err)
		plain, err := p.CreateNarrator("Renee Montoya")
		require.NoError(t, err)

		// Tie at zero credits: lowest id.
		got, err := p.resolveNarratorFolded("Renée-Montoya")
		require.NoError(t, err)
		require.Equal(t, accented.ID, got.ID)

		// plain gets two books, accented one.
		for i, nid := range []int{plain.ID, plain.ID, accented.ID} {
			b, err := p.CreateBook(&Book{Title: fmt.Sprintf("Gotham Central %d", i), FilePath: fmt.Sprintf("/lib/batman/gc%d.m4b", i)})
			require.NoError(t, err)
			require.NoError(t, p.SetBookNarrators(b.ID, []BookNarrator{{BookID: b.ID, NarratorID: nid, Role: "narrator"}}))
		}
		got, err = p.resolveNarratorFolded("Renée-Montoya")
		require.NoError(t, err)
		require.Equal(t, plain.ID, got.ID, "most credits wins")
		require.Equal(t, 2, nfNarratorCount(t, p), "an ambiguous fold creates nothing")
	})
}

func TestNarratorFold_NoMatchCreatesWithGivenSpelling(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		_, err := p.CreateNarrator("Harvey Bullock")
		require.NoError(t, err)

		b := nfBook(t, p, "Hush", "Selina Kyle")

		require.Equal(t, []string{"Selina Kyle"}, narratorNamesOf(t, p, b.ID))
		require.Equal(t, 2, nfNarratorCount(t, p))
		requireFoldIndexMatchesRows(t, p, "create")
	})
}

// Different people whose names merely resemble each other stay apart.
func TestNarratorFold_DistinctNamesStayDistinct(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		a, err := p.resolveNarratorFolded("Harvey Dent")
		require.NoError(t, err)
		b, err := p.resolveNarratorFolded("Harvey Bullock")
		require.NoError(t, err)
		require.NotEqual(t, a.ID, b.ID)
	})
}

// A name with no letter or digit folds to nothing: no key, no fold match.
func TestNarratorFold_EmptyFoldIsNeverIndexedOrMatched(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		a, err := p.resolveNarratorFolded("...")
		require.NoError(t, err)
		b, err := p.resolveNarratorFolded("---")
		require.NoError(t, err)
		require.NotEqual(t, a.ID, b.ID)
		require.Empty(t, nfFoldKeys(t, p))
		requireFoldIndexMatchesRows(t, p, "empty fold")
	})
}

// Two goroutines resolving two spellings of one new name must mint ONE record.
// The fold check and the create are one step under nameIdx.narrator; run with
// -race.
func TestNarratorFold_ConcurrentSpellingsMintOneRecord(t *testing.T) {
	forBothTrustStates(t, func(t *testing.T, p *PebbleStore) {
		const rounds = 40
		spellings := [][2]string{{"Renée Montoya", "Renee Montoya"}, {"Renee Montoya", "Renée Montoya"}}
		for i := 0; i < rounds; i++ {
			pair := spellings[i%2]
			sfx := strings.Repeat("x", i+1) // a fresh fold every round
			names := [2]string{pair[0] + " " + sfx, pair[1] + " " + sfx}
			var wg sync.WaitGroup
			var ids [4]int
			start := make(chan struct{})
			for g := 0; g < 4; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					n, err := p.resolveNarratorFolded(names[g%2])
					if err == nil {
						ids[g] = n.ID
					}
				}()
			}
			close(start)
			wg.Wait()
			for _, id := range ids {
				require.NotZero(t, id)
				require.Equal(t, ids[0], id, "round %d: every spelling resolved to one record", i)
			}
		}
		require.Equal(t, rounds, nfNarratorCount(t, p), "one record per round")
		requireFoldIndexMatchesRows(t, p, "concurrent creates")
	})
}

// The index is exactly what the rows imply after every writer: create, delete,
// and a merge (repoint the credits, then delete the loser).
func TestNarratorFoldIndex_StaysInSyncAcrossWrites(t *testing.T) {
	p := newNarratorSyncTestStore(t)
	requireFoldIndexMatchesRows(t, p, "empty store")

	keep, err := p.CreateNarrator("Renée Montoya")
	require.NoError(t, err)
	requireFoldIndexMatchesRows(t, p, "create accented")
	loser, err := p.CreateNarrator("Renee Montoya") // exact-distinct, fold-equal
	require.NoError(t, err)
	_, err = p.CreateNarrator("...")
	require.NoError(t, err)
	_, err = p.CreateNarrator("Jean-Paul Valley")
	require.NoError(t, err)
	requireFoldIndexMatchesRows(t, p, "creates")
	require.Len(t, nfFoldKeys(t, p), 3, "the empty-fold name has no key")

	b, err := p.CreateBook(&Book{Title: "Gotham Central", FilePath: "/lib/batman/gc.m4b"})
	require.NoError(t, err)
	require.NoError(t, p.SetBookNarrators(b.ID, []BookNarrator{{BookID: b.ID, NarratorID: loser.ID, Role: "narrator"}}))

	// Merge: repoint to the keeper, then delete the loser.
	require.NoError(t, p.SetBookNarrators(b.ID, []BookNarrator{{BookID: b.ID, NarratorID: keep.ID, Role: "narrator"}}))
	require.NoError(t, p.DeleteNarrator(loser.ID))
	requireFoldIndexMatchesRows(t, p, "merge (repoint + delete)")
	require.NotContains(t, nfFoldKeys(t, p), string(narratorFoldKey("reneemontoya", loser.ID)))
	require.Contains(t, nfFoldKeys(t, p), string(narratorFoldKey("reneemontoya", keep.ID)))

	require.NoError(t, p.DeleteNarrator(keep.ID))
	requireFoldIndexMatchesRows(t, p, "delete keeper")
	require.NoError(t, p.DeleteNarrator(keep.ID), "deleting a missing id is a no-op")
	requireFoldIndexMatchesRows(t, p, "delete again")
}

// Reconcile writes only absent keys, is idempotent, sets the sentinel and the
// trust flag, and heals what a build without the index left.
func TestNarratorFoldIndex_ReconcileWritesOnlyAbsentKeys(t *testing.T) {
	p := newNarratorSyncTestStore(t)
	a, err := p.CreateNarrator("Renée Montoya")
	require.NoError(t, err)
	_, err = p.CreateNarrator("Jean-Paul Valley")
	require.NoError(t, err)
	require.False(t, p.NarratorFoldIndexTrusted())

	// A pre-index binary wrote this narrator with no fold key.
	require.NoError(t, p.db.Delete(narratorFoldKey("reneemontoya", a.ID), nil))
	require.Len(t, nfFoldKeys(t, p), 1)

	rep, err := p.ReconcileNarratorFoldIndex(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, rep.Scanned)
	require.Equal(t, 1, rep.Written, "only the absent key is written")
	require.True(t, rep.FirstBuild)
	require.True(t, p.NarratorFoldIndexTrusted())
	requireFoldIndexMatchesRows(t, p, "reconcile")

	rep, err = p.ReconcileNarratorFoldIndex(context.Background())
	require.NoError(t, err)
	require.Zero(t, rep.Written, "a second pass finds nothing to write")
	require.False(t, rep.FirstBuild)
}

// The reconcile covers a library larger than one chunk.
func TestNarratorFoldIndex_ReconcileSpansChunks(t *testing.T) {
	p := newNarratorSyncTestStore(t)
	n := narratorFoldChunk*2 + 17
	for i := 0; i < n; i++ {
		_, err := p.CreateNarrator(fmt.Sprintf("Gotham Voice %d", i))
		require.NoError(t, err)
	}
	require.NoError(t, p.db.DeleteRange([]byte(narratorFoldPrefix), prefixEnd([]byte(narratorFoldPrefix)), nil))
	require.Empty(t, nfFoldKeys(t, p))

	rep, err := p.ReconcileNarratorFoldIndex(context.Background())
	require.NoError(t, err)
	require.Equal(t, n, rep.Written)
	requireFoldIndexMatchesRows(t, p, "chunked reconcile")
}

// A cancelled context stops the pass and leaves the index untrusted.
func TestNarratorFoldIndex_CancelledReconcileDoesNotTrust(t *testing.T) {
	p := newNarratorSyncTestStore(t)
	_, err := p.CreateNarrator("Renée Montoya")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.ReconcileNarratorFoldIndex(ctx)
	require.Error(t, err)
	require.False(t, p.NarratorFoldIndexTrusted())
	set, err := p.narratorFoldSentinelSet()
	require.NoError(t, err)
	require.False(t, set, "no sentinel from an interrupted pass")
}

// Once trusted, the lookup reads the index: an extra key whose row is gone or
// whose name no longer folds to it is ignored (point-verify), and a record
// before the reconcile is found by the scan.
func TestNarratorFoldIndex_PointVerifiesCandidates(t *testing.T) {
	p := newNarratorSyncTestStore(t)
	existing, err := p.CreateNarrator("Renée Montoya")
	require.NoError(t, err)

	// Untrusted: found by scan even with no key.
	require.NoError(t, p.db.Delete(narratorFoldKey("reneemontoya", existing.ID), nil))
	got, err := p.narratorsByFold("Renee Montoya")
	require.NoError(t, err)
	require.Len(t, got, 1)

	_, err = p.ReconcileNarratorFoldIndex(context.Background())
	require.NoError(t, err)

	// Extras: a key for a missing id, and a key under the wrong fold.
	require.NoError(t, p.db.Set(narratorFoldKey("reneemontoya", 9999), []byte{}, nil))
	require.NoError(t, p.db.Set(narratorFoldKey("reneemontoya", existing.ID+100), []byte{}, nil))
	got, err = p.narratorsByFold("Renee Montoya")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, existing.ID, got[0].ID)

	// Trusted reads the index: with the key gone, the record is not offered.
	require.NoError(t, p.db.Delete(narratorFoldKey("reneemontoya", existing.ID), nil))
	got, err = p.narratorsByFold("Renee Montoya")
	require.NoError(t, err)
	require.Empty(t, got, "a trusted lookup reads narrator_fold:, not the records")
}

func TestNarratorFoldIndex_ResetClearsTrust(t *testing.T) {
	p := newNarratorSyncTestStore(t)
	_, err := p.ReconcileNarratorFoldIndex(context.Background())
	require.NoError(t, err)
	require.True(t, p.NarratorFoldIndexTrusted())
	require.NoError(t, p.Reset())
	require.False(t, p.NarratorFoldIndexTrusted())
	set, err := p.narratorFoldSentinelSet()
	require.NoError(t, err)
	require.False(t, set)
}

// CreateNarrator keeps its exact-name semantics for its other callers: the
// API and the maintenance ops that mean "this exact spelling".
func TestNarratorFold_CreateNarratorStaysExactNameOnly(t *testing.T) {
	p := newNarratorSyncTestStore(t)
	a, err := p.CreateNarrator("Renée Montoya")
	require.NoError(t, err)
	b, err := p.CreateNarrator("Renee Montoya")
	require.NoError(t, err)
	require.NotEqual(t, a.ID, b.ID)
	again, err := p.CreateNarrator("  RENÉE   montoya ")
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
}
