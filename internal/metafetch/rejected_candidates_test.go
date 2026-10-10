// file: internal/metafetch/rejected_candidates_test.go
// version: 1.2.0
// guid: 3f0c9a51-7e2d-4b8a-9c16-5d4e8f2a7b30
// last-edited: 2026-10-10

package metafetch

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// memRejections is an in-memory RejectionStore.
type memRejections map[string][]byte

func (m memRejections) ScanPrefix(prefix string) ([]database.KVPair, error) {
	var out []database.KVPair
	for k, v := range m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, database.KVPair{Key: k, Value: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (m memRejections) GetRaw(key string) ([]byte, error) { return m[key], nil }
func (m memRejections) SetRaw(key string, v []byte) error { m[key] = v; return nil }
func (m memRejections) DeleteRaw(key string) error        { delete(m, key); return nil }

// ModifyBook stands in for the book write stripe: every book exists, and
// nothing is written.
func (m memRejections) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	b := &database.Book{ID: id}
	if err := fn(b); err != nil && !errors.Is(err, database.ErrSkipBookWrite) {
		return nil, err
	}
	return b, nil
}

func loadSet(t *testing.T, m memRejections, bookID string) RejectedSet {
	t.Helper()
	set, err := LoadRejectedCandidates(m, bookID)
	require.NoError(t, err)
	return set
}

// The owner's 2026-10-10 rule, all four combinations: identifiers decide
// when both sides carry one; source+title decides otherwise.
func TestRejectedSet_IdentifierMatchingCombinations(t *testing.T) {
	abridged := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ASIN: "B0SYNABR01"}
	unabridged := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ASIN: "B0SYNUNA02"}
	noID := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3"}

	t.Run("both carry an ASIN: the identifier decides", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", abridged))
		set := loadSet(t, m, "b1")
		require.True(t, set.HasCandidate(abridged))
		require.True(t, set.HasCandidate(&MetadataCandidate{Source: "AUDIBLE", Title: "synthetic field notes 3", ASIN: "b0synabr01"}),
			"case folds on source, title and ASIN")
		require.False(t, set.HasCandidate(unabridged), "the unabridged record with the same title is not refused")
	})
	t.Run("rejection has an ASIN, candidate none: source+title decides", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", abridged))
		require.True(t, loadSet(t, m, "b1").HasCandidate(noID))
	})
	t.Run("rejection has none, candidate an ASIN: source+title decides", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", noID))
		set := loadSet(t, m, "b1")
		require.True(t, set.HasCandidate(abridged))
		require.True(t, set.HasCandidate(unabridged))
	})
	t.Run("neither carries one: source+title decides", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", noID))
		set := loadSet(t, m, "b1")
		require.True(t, set.HasCandidate(noID))
		require.False(t, set.HasCandidate(&MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 4"}))
	})
	t.Run("ISBN when there is no ASIN, any ISBN form", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", &MetadataCandidate{Source: "Open Library", Title: "Synthetic Field Notes 3", ISBN13: "978-0-00-000001-9", ISBN10: "0-00-000001-X"}))
		set := loadSet(t, m, "b1")
		require.True(t, set.HasCandidate(&MetadataCandidate{Source: "Open Library", Title: "Synthetic Field Notes 3", ISBN: "000000001x"}))
		require.False(t, set.HasCandidate(&MetadataCandidate{Source: "Open Library", Title: "Synthetic Field Notes 3", ISBN13: "9780000000027"}))
	})
	t.Run("no shared identifier kind: source+title decides", func(t *testing.T) {
		m := memRejections{}
		require.NoError(t, RecordRejection(m, "b1", abridged))
		require.True(t, loadSet(t, m, "b1").HasCandidate(&MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ISBN13: "9780000000027"}))
	})
}

// Rejecting a second record of the same source+title keeps the first.
func TestRecordRejection_MergesAndKeepsLegacyBroad(t *testing.T) {
	a := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ASIN: "B0SYNABR01"}
	b := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ASIN: "B0SYNUNA02"}
	c := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ASIN: "B0SYNOTH03"}

	m := memRejections{}
	require.NoError(t, RecordRejection(m, "b1", a))
	require.NoError(t, RecordRejection(m, "b1", b))
	set := loadSet(t, m, "b1")
	require.True(t, set.HasCandidate(a))
	require.True(t, set.HasCandidate(b))
	require.False(t, set.HasCandidate(c))

	// A legacy key ("1") rejects every record and stays that way.
	legacy := memRejections{RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 3"): []byte("1")}
	require.NoError(t, RecordRejection(legacy, "b1", a))
	require.True(t, loadSet(t, legacy, "b1").HasCandidate(c))
}

// After un-rejecting a candidate it matches no rejection, whatever the key
// held; and a record the owner still rejects stays rejected.
func TestRemoveRejection_Postcondition(t *testing.T) {
	a := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ASIN: "B0SYNABR01"}
	b := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ASIN: "B0SYNUNA02"}
	noID := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3"}
	isbnOnly := &MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 3", ISBN13: "9780000000027"}
	key := RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 3")

	for _, tc := range []struct {
		name     string
		setup    func(m memRejections)
		unreject *MetadataCandidate
		stillRej *MetadataCandidate // nil: nothing should stay rejected
		wantGone bool
	}{
		{"one of two identifiers", func(m memRejections) { _ = RecordRejection(m, "b1", a); _ = RecordRejection(m, "b1", b) }, a, b, false},
		{"the only identifier", func(m memRejections) { _ = RecordRejection(m, "b1", a) }, a, nil, true},
		{"legacy key, candidate with an ASIN", func(m memRejections) { m[key] = []byte("1") }, a, nil, true},
		{"identifier key, candidate with none", func(m memRejections) { _ = RecordRejection(m, "b1", a) }, noID, nil, true},
		{"ASIN key, ISBN-only candidate", func(m memRejections) { _ = RecordRejection(m, "b1", a) }, isbnOnly, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := memRejections{}
			tc.setup(m)
			changed, err := RemoveRejection(m, "b1", tc.unreject)
			require.NoError(t, err)
			require.True(t, changed)
			set := loadSet(t, m, "b1")
			require.False(t, set.HasCandidate(tc.unreject), "un-rejected candidate still matches")
			_, present := m[key]
			require.Equal(t, tc.wantGone, !present)
			if tc.stillRej != nil {
				require.True(t, set.HasCandidate(tc.stillRej), "un-reject removed another record's rejection")
			}
		})
	}
}

// N1: un-reject removes every case variant of the key, and a legacy key
// stored in mixed case matches a differently-cased candidate.
func TestRejectionKeys_CaseVariants(t *testing.T) {
	m := memRejections{
		RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 3"): []byte("1"),
		RejectedCandidateStoreKey("b1", "AUDIBLE", "SYNTHETIC FIELD NOTES 3"): []byte("1"),
		RejectedCandidateStoreKey("b1", "audible", "synthetic field notes 3"): RejectionValue(nil, CandidateIdentifiers("B0SYNUNA02", "", "", "")),
		RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 4"): []byte("1"),
	}
	cand := &MetadataCandidate{Source: "aUdIbLe", Title: "Synthetic FIELD Notes 3", ASIN: "B0SYNUNA02"}
	require.True(t, loadSet(t, m, "b1").HasCandidate(cand), "legacy mixed-case key matches a differently-cased candidate")

	changed, err := RemoveRejection(m, "b1", cand)
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, m, 1, "every case variant of the key is gone")
	require.Contains(t, m, RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 4"), "another title's rejection is kept")
	require.False(t, loadSet(t, m, "b1").HasCandidate(cand))
}
