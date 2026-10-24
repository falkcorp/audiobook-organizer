// file: internal/decisions/vetoes_paths_test.go
// version: 1.0.0
// guid: 8e41c0b7-92a3-4d65-b1f8-3a6d07e52c19
// last-edited: 2026-10-24

package decisions

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// pathMap resolves an id to the path the map gives it (the id itself when it
// has none); an id in bad fails with the error the map gives.
func pathMap(paths map[string][]string, bad map[string]error) PathResolver {
	return func(_, id string) ([]string, error) {
		if err, ok := bad[id]; ok {
			return nil, err
		}
		if p, ok := paths[id]; ok {
			return p, nil
		}
		return []string{id}, nil
	}
}

func loadPaths(t testing.TB, s *Store, r PathResolver) *Vetoes {
	t.Helper()
	v, err := s.LoadPaths(context.Background(), r)
	require.NoError(t, err)
	return v
}

func rejectPair(t testing.TB, s *Store, a, b string) {
	t.Helper()
	mustRecord(t, s, humanReject(a, b))
}

// Both ends of a stored veto merged into one book. Retiring one of them onto
// that book is the join the owner rejected, whichever id the veto names.
func TestVetoes_CollapsedEdgeHoldsAJoinThroughItsPaths(t *testing.T) {
	// T is the live book. O was retired into T, S0 merged into S, S into T,
	// M merged into T.
	paths := map[string][]string{
		"O": {"O", "T"}, "S": {"S", "T"}, "M": {"M", "T"}, "S0": {"S0", "S", "T"},
		"X": {"X", "T"}, "Y": {"Y", "T"}, "S1": {"S1", "S", "T"}, "U": {"U", "T"},
	}
	for name, tc := range map[string]struct {
		edge       [2]string
		a, b       string
		wantVetoed bool
	}{
		"retired into the target, shell merged into it":      {[2]string{"O", "S"}, "S", "T", true},
		"the same, asked the other way round":                {[2]string{"O", "S"}, "T", "S", true},
		"a book merged into the target":                      {[2]string{"S", "M"}, "S", "T", true},
		"shell absorbed by a book that was merged on":        {[2]string{"S0", "T"}, "S", "T", true},
		"the absorbed shell's own row":                       {[2]string{"S0", "T"}, "S0", "T", true},
		"the very pair, one merged into the other":           {[2]string{"S", "T"}, "S", "T", true},
		"vetoed books merged into the target, either's row":  {[2]string{"X", "Y"}, "X", "T", true},
		"unrelated vetoed books merged into the same target": {[2]string{"X", "Y"}, "S", "T", false},
		"both ends merged through the shell: merged anyway":  {[2]string{"S0", "S1"}, "S", "T", false},
		"an unrelated book merged into the target":           {[2]string{"X", "Y"}, "U", "T", false},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newStore(t)
			rejectPair(t, s, tc.edge[0], tc.edge[1])
			v := loadPaths(t, s, pathMap(paths, nil))
			require.Equal(t, tc.wantVetoed, snapVetoed(t, v, testType, tc.a, tc.b, FamilySameWork))
			require.False(t, snapVetoed(t, v, testType, tc.a, tc.b, FamilySameCopy), "family scoped")
			_, pairs, _ := v.Counts()
			require.Zero(t, pairs, "a collapsed veto is not a pair of live ids")
		})
	}
}

// Without the paths (a plain IDResolver gives only [id, end]) the same rule
// holds a veto whose end merged into the other.
func TestVetoes_CollapsedEdgeThroughAPlainResolver(t *testing.T) {
	s, _ := newStore(t)
	rejectPair(t, s, "S", "T")
	v := mustLoad(t, s, mapResolver(map[string]string{"S": "T"}, nil))
	require.True(t, snapVetoed(t, v, testType, "S", "T", FamilySameWork))
	require.True(t, snapVetoed(t, v, testType, "T", "S", FamilySameWork))
	require.False(t, snapVetoed(t, v, testType, "T", "Z", FamilySameWork))
}

// The members of a rejected set veto each other pair by pair, so members that
// resolve to one book are collapsed vetoes too.
func TestVetoes_CollapsedSetMembersHoldAJoinThroughTheirPaths(t *testing.T) {
	s, _ := newStore(t)
	big := ids("M", MaxEdgeSet+5)
	mustRecord(t, s, humanReject(big...))
	paths := map[string][]string{"M000": {"M000", "T"}, "M001": {"M001", "T"}, "S": {"S", "T"}}
	v := loadPaths(t, s, pathMap(paths, nil))
	require.True(t, snapVetoed(t, v, testType, "M000", "T", FamilySameWork), "M000 retired onto T joins M001's work")
	require.True(t, snapVetoed(t, v, testType, "M001", "T", FamilySameWork))
	require.False(t, snapVetoed(t, v, testType, "S", "T", FamilySameWork), "S is no member of the set")
	require.True(t, snapVetoed(t, v, testType, "M005", "T", FamilySameWork), "an uncollapsed member still meets T, which holds M000")
}

func TestVetoes_UnresolvableStoredIDHoldsOnlyWhatItCouldTouch(t *testing.T) {
	s, _ := newStore(t)
	// B was retired into H1 and H2 (an ambiguous shell). The owner rejected
	// (B, Y), and Y was merged into the shell S.
	rejectPair(t, s, "B", "Y")
	rejectPair(t, s, "C", "Z")
	ambiguous := func(id string, holders ...string) error {
		return &UnresolvableIDError{EntityType: testType, ID: id, Kind: ErrRetiredShellAmbiguous, Related: holders}
	}
	v := loadPaths(t, s, pathMap(
		map[string][]string{"Y": {"Y", "S"}},
		map[string]error{"B": ambiguous("B", "H1", "H2"), "C": ambiguous("C", "H3", "H4")}))

	un := v.Unresolved()
	require.Len(t, un, 2, "every unresolvable id is listed, not the first")
	require.Equal(t, []string{"B", "C"}, []string{un[0].ID, un[1].ID})
	for _, u := range un {
		require.ErrorIs(t, u.Err, ErrRetiredShellAmbiguous)
	}

	// (S, H1) touches the veto: Y's end is S, and B's holders are unknown.
	_, err := v.PairVetoed(testType, "S", "H1", FamilySameWork)
	var ue *UnresolvableError
	require.ErrorAs(t, err, &ue)
	require.ErrorIs(t, err, ErrIDUnresolvable)
	require.Len(t, ue.IDs, 1)
	require.Equal(t, "B", ue.IDs[0].ID)
	// The veto of (C, Z) is not touched by (S, H1): only B is named.

	// A pair that reaches neither veto goes ahead.
	got, err := v.PairVetoed(testType, "P", "Q", FamilySameWork)
	require.NoError(t, err)
	require.False(t, got)
	// A veto in another family is not touched.
	got, err = v.PairVetoed(testType, "S", "H1", FamilySameCopy)
	require.NoError(t, err)
	require.False(t, got)

	// A pair that touches both bad vetoes is told about both.
	_, err = v.PairVetoed(testType, "S", "Z", FamilySameWork)
	require.ErrorAs(t, err, &ue)
	require.Len(t, ue.IDs, 2)
}

func TestVetoes_UnresolvableQueryIDsAreAllReported(t *testing.T) {
	s, _ := newStore(t)
	rejectPair(t, s, "P", "Q")
	cyc := func(id string) error {
		return &UnresolvableIDError{EntityType: testType, ID: id, Kind: ErrResolverCycle, Related: []string{"A", "B"}}
	}
	v := loadPaths(t, s, pathMap(nil, map[string]error{"B1": cyc("B1"), "B2": cyc("B2")}))
	require.Empty(t, v.Unresolved(), "nothing stored is unresolvable")

	_, err := v.PairVetoed(testType, "B1", "B2", FamilySameWork)
	var ue *UnresolvableError
	require.ErrorAs(t, err, &ue)
	require.Len(t, ue.IDs, 2, "both ids are reported")
	require.ErrorIs(t, err, ErrResolverCycle)

	_, err = v.PairVetoed(testType, "B1", "P", FamilySameWork)
	require.ErrorAs(t, err, &ue)
	require.Len(t, ue.IDs, 1)
	require.Equal(t, "B1", ue.IDs[0].ID)

	got, err := v.PairVetoed(testType, "P", "Q", FamilySameWork)
	require.NoError(t, err)
	require.True(t, got, "the rest of the snapshot is intact")
}

// With no end of a stored veto resolvable, the best known is the footprints of
// the unresolved ends: candidate holders and the ids of a cycle.
func TestVetoes_VetoWithEveryEndUnresolvableHoldsItsCandidates(t *testing.T) {
	s, _ := newStore(t)
	rejectPair(t, s, "B", "C")
	amb := func(id string, holders ...string) error {
		return &UnresolvableIDError{EntityType: testType, ID: id, Kind: ErrRetiredShellAmbiguous, Related: holders}
	}
	v := loadPaths(t, s, pathMap(nil, map[string]error{"B": amb("B", "H1", "H2"), "C": amb("C", "H3", "H4")}))
	_, err := v.PairVetoed(testType, "H1", "H3", FamilySameWork)
	require.ErrorIs(t, err, ErrIDUnresolvable)
	got, err := v.PairVetoed(testType, "P", "Q", FamilySameWork)
	require.NoError(t, err)
	require.False(t, got)
}

// A definite veto is reported as one, even with unresolvable ids in play.
func TestVetoes_DefiniteVetoBeatsAnUnresolvedOne(t *testing.T) {
	s, _ := newStore(t)
	rejectPair(t, s, "P", "Q")
	rejectPair(t, s, "B", "P")
	v := loadPaths(t, s, pathMap(nil, map[string]error{"B": &UnresolvableIDError{EntityType: testType, ID: "B", Kind: ErrResolverCycle}}))
	got, err := v.PairVetoed(testType, "P", "Q", FamilySameWork)
	require.NoError(t, err)
	require.True(t, got)
}

// Only a failed READ refuses the load; and it still does.
func TestVetoes_FailedReadStillRefusesTheLoad(t *testing.T) {
	s, _ := newStore(t)
	rejectPair(t, s, "P", "Q")
	boom := errors.New("pebble read failed")
	_, err := s.LoadPaths(context.Background(), pathMap(nil, map[string]error{"P": fmt.Errorf("resolve P: %w", boom)}))
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrIDUnresolvable)
}

func TestAuthorTombstoneResolver_ALongChainIsUnresolvableNotARead(t *testing.T) {
	res := AuthorTombstoneResolver(loopTombstone{})
	_, err := res("author", "1")
	require.ErrorIs(t, err, ErrIDUnresolvable)
	require.ErrorIs(t, err, ErrResolverCycle)
}

// loopTombstone redirects author n to n+1 forever.
type loopTombstone struct{}

func (loopTombstone) GetAuthorTombstone(n int) (int, error) { return n + 1, nil }
