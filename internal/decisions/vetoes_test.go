// file: internal/decisions/vetoes_test.go
// version: 1.2.0
// guid: 47c2d9e5-1f80-4a36-b3d7-90e6a5c8142f
// last-edited: 2026-10-24

package decisions

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestVetoes_SubjectsPairsAndFamilies(t *testing.T) {
	s, kv := newStore(t)
	rec := mustRecord(t, s, humanReject("b1", "b2", "b3"))
	mustRecord(t, s, labelInput(DecisionApplied, "b7", "b8"))
	forceEdge(t, kv, testType, "b8", "b9", FamilyAny)

	v := mustLoad(t, s, nil)
	require.True(t, snapRejected(t, v, testKind, rec.SubjectKey, ""))
	require.False(t, snapRejected(t, v, "dedup:book-candidate", rec.SubjectKey, ""), "kind scoped")
	require.False(t, snapRejected(t, v, testKind, subjectOf(t, labelInput(DecisionApplied, "b7", "b8")), ""), "applied is no veto")

	for _, p := range [][2]string{{"b1", "b2"}, {"b2", "b1"}, {"b1", "b3"}, {"b2", "b3"}} {
		require.True(t, snapVetoed(t, v, testType, p[0], p[1], FamilySameWork), "%v", p)
	}
	require.False(t, snapVetoed(t, v, testType, "b1", "b2", FamilySameCopy))
	require.False(t, snapVetoed(t, v, testType, "b1", "b9", FamilySameWork))
	require.False(t, snapVetoed(t, v, testType, "b1", "b1", FamilySameWork), "an entity is not a pair with itself")
	require.True(t, snapVetoed(t, v, testType, "b9", "b8", FamilySameCopy), "never-merge style edge blocks all families")
	subjects, pairs, _ := v.Counts()
	require.Equal(t, 1, subjects)
	require.Equal(t, 4, pairs)
}

// The snapshot and the direct store lookup must always agree.
func TestVetoes_SnapshotAgreesWithDirectLookup(t *testing.T) {
	s, _ := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2", "b3"))
	mustRecord(t, s, humanReject("b3", "b4"))
	mustRecord(t, s, labelInput(DecisionUnreject, "b3", "b4"))
	big := humanReject(ids("g", MaxEdgeSet+3)...)
	mustRecord(t, s, big)
	v := mustLoad(t, s, nil)
	all := append([]string{"b1", "b2", "b3", "b4", "outsider"}, big.EntityIDs[:6]...)
	for _, a := range all {
		for _, b := range all {
			for _, fam := range []string{FamilySameWork, FamilySameCopy} {
				require.Equal(t, mustVetoed(t, s, testType, a, b, fam), snapVetoed(t, v, testType, a, b, fam), "%s %s %s", a, b, fam)
			}
		}
	}
}

func TestVetoes_UnrejectedAreNotLoaded(t *testing.T) {
	s, _ := newStore(t)
	rec := mustRecord(t, s, humanReject("b1", "b2"))
	mustRecord(t, s, labelInput(DecisionUnreject, "b1", "b2"))
	v := mustLoad(t, s, nil)
	require.False(t, snapRejected(t, v, testKind, rec.SubjectKey, ""))
	require.False(t, snapVetoed(t, v, testType, "b1", "b2", FamilySameWork))
}

// The snapshot is the single consult point of a plan run, so it must apply
// the same re-open rule as Store.Lookup, for every material hash a caller can
// pass.
func TestVetoes_SnapshotAgreesWithLookupOnMaterialHash(t *testing.T) {
	s, _ := newStore(t)
	h1 := MaterialHash(map[string]string{"title": "Old"})
	h2 := MaterialHash(map[string]string{"title": "New"})
	field := func(scope, hash string) Input {
		return Input{
			OpKind: "repairs:repair-junk-titles", EntityType: testType, EntityIDs: []string{"b1"}, Scope: scope,
			Decision: DecisionReject, Source: SourceHuman, ActorUserID: "u1",
			ReasonCode: "proposed_value_wrong", MaterialHash: hash,
		}
	}
	withHash := mustRecord(t, s, field("with-hash", h1))
	sticky := mustRecord(t, s, field("sticky", ""))
	v := mustLoad(t, s, nil)

	for _, rec := range []*Record{withHash, sticky} {
		for _, now := range []string{"", h1, h2} {
			st, err := s.Lookup(rec.OpKind, rec.SubjectKey, now)
			require.NoError(t, err)
			require.Equal(t, st.Blocked, snapRejected(t, v, rec.OpKind, rec.SubjectKey, now),
				"scope %q, material hash now %q: snapshot and Lookup disagree", rec.Scope, now)
		}
	}
	require.False(t, snapRejected(t, v, withHash.OpKind, withHash.SubjectKey, h2), "changed data is a new suggestion")
	require.True(t, snapRejected(t, v, withHash.OpKind, withHash.SubjectKey, h1))
	require.True(t, snapRejected(t, v, sticky.OpKind, sticky.SubjectKey, h2), "a sticky reject is never reopened by data")
}

// A group that grew by one member is still vetoed for the old pairs; this is
// what the exact-ID-set filter it replaces could not do.
func TestVetoes_GroupThatGrewKeepsItsOldPairsVetoed(t *testing.T) {
	s, _ := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2", "b3", "b4"))
	v := mustLoad(t, s, nil)
	for _, p := range [][2]string{{"b1", "b2"}, {"b3", "b4"}, {"b1", "b4"}} {
		require.True(t, snapVetoed(t, v, testType, p[0], p[1], FamilySameWork))
	}
	for _, old := range []string{"b1", "b2", "b3", "b4"} {
		require.False(t, snapVetoed(t, v, testType, old, "b5", FamilySameWork), "only the NEW pairs with b5 can be proposed")
	}
}

// mapResolver redirects ids like author_tombstone: does.
func mapResolver(redirects map[string]string, calls *int) IDResolver {
	return func(_, id string) (string, error) {
		if calls != nil {
			*calls++
		}
		for {
			next, ok := redirects[id]
			if !ok {
				return id, nil
			}
			id = next
		}
	}
}

func TestVetoes_MergedAwayIDsResolveToTheirSurvivor(t *testing.T) {
	s, _ := newStore(t)
	in := humanReject("7", "9")
	in.OpKind, in.EntityType, in.Family, in.ReasonCode = "authors:group-merge", "author", FamilySamePerson, "different_people"
	mustRecord(t, s, in)

	// 9 was later merged into 4.
	v := mustLoad(t, s, mapResolver(map[string]string{"9": "4"}, nil))
	require.True(t, snapVetoed(t, v, "author", "7", "4", FamilySamePerson), "the edge was stored as (7,9) and follows 9 to 4")
	require.True(t, snapVetoed(t, v, "author", "7", "9", FamilySamePerson), "the old id still finds it")
	require.True(t, snapVetoed(t, v, "author", "4", "7", FamilySamePerson))
	require.False(t, snapVetoed(t, v, "author", "7", "5", FamilySamePerson))

	// Without a resolver the redirected id would slip through: the resolver is
	// what makes the veto survive a merge.
	plain := mustLoad(t, s, nil)
	require.False(t, snapVetoed(t, plain, "author", "7", "4", FamilySamePerson))
}

func TestVetoes_EdgeWhoseEndsMergedIsKeptAsACollapsedVetoAndSetMembersResolve(t *testing.T) {
	s, _ := newStore(t)
	in := humanReject("7", "9")
	in.OpKind, in.EntityType, in.Family, in.ReasonCode = "authors:group-merge", "author", FamilySamePerson, "different_people"
	mustRecord(t, s, in)
	v := mustLoad(t, s, mapResolver(map[string]string{"9": "7"}, nil))
	// 9 was merged into 7 against the owner's veto. There is no pair of live
	// authors left (no edge is stored), but a caller asking to join 7 and 9 is
	// asking for the join the owner rejected, so the veto still answers.
	require.True(t, snapVetoed(t, v, "author", "7", "9", FamilySamePerson))
	require.False(t, snapVetoed(t, v, "author", "7", "9", FamilySameCopy), "family scoped")
	_, pairs, _ := v.Counts()
	require.Zero(t, pairs, "a collapsed veto is not a pair of live ids")

	// Set membership resolves too.
	big := make([]string, 0, MaxEdgeSet+5)
	for i := range MaxEdgeSet + 5 {
		big = append(big, strconv.Itoa(1000+i))
	}
	bigIn := in
	bigIn.EntityIDs = big
	mustRecord(t, s, bigIn)
	v = mustLoad(t, s, mapResolver(map[string]string{"1004": "55"}, nil))
	require.True(t, snapVetoed(t, v, "author", "55", "1010", FamilySamePerson), "member 1004 now answers to 55")
}

func TestVetoes_ResolverErrorFailsClosed(t *testing.T) {
	s, _ := newStore(t)
	mustRecord(t, s, humanReject("b1", "b2"))
	boom := errors.New("tombstone read failed")
	_, err := s.Load(context.Background(), func(_, id string) (string, error) { return "", boom })
	require.ErrorIs(t, err, boom, "a resolver error while loading is a load error, never a quiet no-veto")

	v := mustLoad(t, s, nil)
	v.resolve = func(_, _ string) ([]string, error) { return nil, boom }
	_, err = v.PairVetoed(testType, "b1", "b2", FamilySameWork)
	require.ErrorIs(t, err, boom)
}

func TestVetoes_LoadFailsClosedWhenTheStoreCannotBeRead(t *testing.T) {
	kv := newKV(t)
	good := New(kv)
	mustRecord(t, good, humanReject("b1", "b2"))
	for _, prefix := range []string{prefixActive, prefixEdge, prefixMember} {
		s := New(&failingKV{KV: kv, failScan: prefix})
		v, err := s.Load(context.Background(), nil)
		require.Error(t, err, prefix)
		require.Nil(t, v, "no half-loaded snapshot a caller could mistake for 'no vetoes'")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := good.Load(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestAuthorTombstoneResolver_WithARealStore(t *testing.T) {
	kv := newKV(t)
	require.NoError(t, kv.CreateAuthorTombstone(9, 4))
	require.NoError(t, kv.CreateAuthorTombstone(4, 2)) // a chain
	res := AuthorTombstoneResolver(kv)

	for in, want := range map[string]string{"9": "2", "4": "2", "2": "2", "77": "77", "not-a-number": "not-a-number"} {
		got, err := res("author", in)
		require.NoError(t, err)
		require.Equal(t, want, got, in)
	}
	got, err := res("book", "9")
	require.NoError(t, err)
	require.Equal(t, "9", got, "only authors are redirected")

	require.NoError(t, kv.CreateAuthorTombstone(2, 9)) // a cycle
	_, err = res("author", "9")
	require.Error(t, err)

	boom := errors.New("read failed")
	_, err = AuthorTombstoneResolver(errTombstones{boom})("author", "9")
	require.ErrorIs(t, err, boom)
}

type errTombstones struct{ err error }

func (e errTombstones) GetAuthorTombstone(int) (int, error) { return 0, e.err }

var _ KV = (*database.PebbleStore)(nil)
