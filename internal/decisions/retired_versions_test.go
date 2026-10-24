// file: internal/decisions/retired_versions_test.go
// version: 1.0.0
// guid: 2f8b05d3-7a19-4c64-9e50-c13d6a84b7e9
// last-edited: 2026-10-23

package decisions

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredBooks is a fake store of books and retired versions.
type retiredBooks struct {
	books    map[string]*database.Book
	versions map[string][]database.BookVersion // shell id -> versions
	err      error
}

func (r *retiredBooks) GetBookByID(id string) (*database.Book, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.books[id], nil
}

func (r *retiredBooks) GetRetiredVersionsFromBook(shell string) ([]database.BookVersion, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.versions[shell], nil
}

func liveBook(id string) *database.Book { return &database.Book{ID: id} }

func trashedBook(id string) *database.Book {
	t := true
	return &database.Book{ID: id, MarkedForDeletion: &t}
}

func retiredVersion(shell, holder string) database.BookVersion {
	return database.BookVersion{ID: "v-" + shell, BookID: holder, Status: database.BookVersionStatusRetired, RetiredFromBookID: shell}
}

func TestRetiredBookResolver_FollowsTheIndex(t *testing.T) {
	r := &retiredBooks{
		books: map[string]*database.Book{
			"HOLDER": liveBook("HOLDER"), "TRASHED": trashedBook("TRASHED"),
			"RESTORED": liveBook("RESTORED"), "OTHER": liveBook("OTHER"),
		},
		versions: map[string][]database.BookVersion{
			"TRASHED":  {retiredVersion("TRASHED", "HOLDER")},
			"PURGED":   {retiredVersion("PURGED", "HOLDER")},
			"RESTORED": {retiredVersion("RESTORED", "HOLDER")}, // stale: the book is live again
			"BAD":      {{ID: "v1", BookID: "HOLDER", Status: database.BookVersionStatusAlt, RetiredFromBookID: "BAD"}},
			"WRONG":    {{ID: "v2", BookID: "HOLDER", Status: database.BookVersionStatusRetired, RetiredFromBookID: "ELSEWHERE"}},
		},
	}
	res := RetiredBookResolver(r)
	for id, want := range map[string]string{
		"TRASHED":  "HOLDER", // a trashed shell
		"PURGED":   "HOLDER", // its book row is gone; the index is all that is left
		"RESTORED": "RESTORED", "HHOLDER": "HHOLDER",
		"OTHER": "OTHER", "NEVER-EXISTED": "NEVER-EXISTED",
		"BAD": "BAD", "WRONG": "WRONG",
	} {
		got, err := res("book", id)
		require.NoError(t, err, id)
		require.Equal(t, want, got, id)
	}
	got, err := res("author", "TRASHED")
	require.NoError(t, err)
	require.Equal(t, "TRASHED", got, "only books resolve")
}

func TestRetiredBookResolver_AmbiguityAndReadErrorsFailClosed(t *testing.T) {
	r := &retiredBooks{
		books: map[string]*database.Book{"S": trashedBook("S")},
		versions: map[string][]database.BookVersion{
			"S": {retiredVersion("S", "H1"), {ID: "v-second", BookID: "H2", Status: database.BookVersionStatusRetired, RetiredFromBookID: "S"}},
		},
	}
	_, err := RetiredBookResolver(r)("book", "S")
	require.ErrorIs(t, err, ErrRetiredShellAmbiguous)

	r.err = errors.New("store down")
	_, err = RetiredBookResolver(r)("book", "S")
	require.ErrorContains(t, err, "store down")
}

func TestChainResolvers(t *testing.T) {
	step := func(m map[string]string) IDResolver {
		return func(_, id string) (string, error) {
			if n, ok := m[id]; ok {
				return n, nil
			}
			return id, nil
		}
	}
	// B -> C is the first resolver's, C -> D the second's, D -> E the first's
	// again: the chain goes on until nothing moves.
	chain := ChainResolvers(step(map[string]string{"B": "C", "D": "E"}), step(map[string]string{"C": "D"}))
	got, err := chain("book", "B")
	require.NoError(t, err)
	require.Equal(t, "E", got)
	got, err = chain("book", "Z")
	require.NoError(t, err)
	require.Equal(t, "Z", got)

	_, err = ChainResolvers(step(map[string]string{"A": "B", "B": "A"}))("book", "A")
	require.ErrorContains(t, err, "hops")

	boom := errors.New("boom")
	_, err = ChainResolvers(func(string, string) (string, error) { return "", boom })("book", "A")
	require.ErrorIs(t, err, boom)
}

// A veto that names a purged shell resolves to the book that holds the shell's
// files, and so still blocks; without the resolver it fails open.
func TestRetiredBookResolver_VetoOnAPurgedShellStillBlocks(t *testing.T) {
	s, _ := newStore(t)
	const shell, holder, other = "SHELL000000000000000000001", "HOLDER00000000000000000001", "OTHER000000000000000000001"
	in := humanReject(shell, other)
	mustRecord(t, s, in)

	bare := mustLoad(t, s, nil)
	require.False(t, snapVetoed(t, bare, testType, holder, other, FamilySameWork), "unresolved: the veto names the shell, not its holder")

	r := &retiredBooks{
		books:    map[string]*database.Book{holder: liveBook(holder), other: liveBook(other)}, // the shell was purged
		versions: map[string][]database.BookVersion{shell: {retiredVersion(shell, holder)}},
	}
	v := mustLoad(t, s, ChainResolvers(RetiredBookResolver(r)))
	require.True(t, snapVetoed(t, v, testType, holder, other, FamilySameWork))
	require.True(t, snapVetoed(t, v, testType, shell, other, FamilySameWork), "the old id resolves too")
	require.False(t, snapVetoed(t, v, testType, holder, other, FamilySameCopy), "still family scoped")
}

func TestRetiredVersionsSubjectKey(t *testing.T) {
	k, err := RetiredVersionsSubjectKey("01hzx3k9q4abcdefghjkmnpqrs")
	require.NoError(t, err)
	want, err := SubjectKey("repairs:retired-versions", "book", []string{"01HZX3K9Q4ABCDEFGHJKMNPQRS"}, "")
	require.NoError(t, err)
	require.Equal(t, want, k, "ids are canonicalised")
	_, err = RetiredVersionsSubjectKey(" ")
	require.ErrorIs(t, err, ErrInvalid)
}

func retiredEvent(version string) RetiredVersionsEvent {
	return RetiredVersionsEvent{
		ShellID: "SHELL000000000000000000001", TargetID: "HOLDER00000000000000000001", VersionID: version,
		OpID: "op-" + version, PlanOpID: "plan-1", PlanRowID: "SHELL000000000000000000001",
	}
}

func TestRecordRetired_AppliedThenUndoneIsOneChainAndNeverVetoes(t *testing.T) {
	s, _ := newStore(t)
	ev := retiredEvent("v1")

	applied, err := RecordRetiredApplied(s, ev)
	require.NoError(t, err)
	require.Equal(t, DecisionApplied, applied.Decision)
	require.Equal(t, SourceAuto, applied.Source, "no actor: auto")
	require.Equal(t, OpKindRetiredVersions, applied.OpKind)
	require.Equal(t, []string{ev.ShellID}, applied.EntityIDs)
	require.Equal(t, "plan-1", applied.PlanOpID)
	require.Equal(t, ev.TargetID, applied.Snapshot.Current["target_id"])
	require.Equal(t, "v1", applied.Snapshot.Current["version_id"])
	require.Len(t, applied.Snapshot.Entities, 2)

	ev.ActorUserID, ev.ActorName, ev.OpID = "u1", "Owner", "op-undo"
	undone, err := RecordRetiredUndone(s, ev)
	require.NoError(t, err)
	require.Equal(t, DecisionUndone, undone.Decision)
	require.Equal(t, SourceHuman, undone.Source)
	require.Equal(t, applied.SubjectKey, undone.SubjectKey, "one subject")
	require.Equal(t, applied.ID, undone.SupersedesID)

	hist, err := s.History(OpKindRetiredVersions, applied.SubjectKey)
	require.NoError(t, err)
	require.Len(t, hist, 2)
	require.Equal(t, DecisionUndone, hist[0].Decision, "newest first")

	// Labels: neither vetoes.
	v := mustLoad(t, s, nil)
	require.False(t, snapRejected(t, v, OpKindRetiredVersions, applied.SubjectKey, ""))
	st, err := s.Lookup(OpKindRetiredVersions, applied.SubjectKey, "")
	require.NoError(t, err)
	require.False(t, st.Blocked)
}

// A label does not lift a reject made on the same subject, and a retry that
// finds its own record already written adds nothing.
func TestRecordRetired_LabelsKeepARejectAndRetriesAreIdempotent(t *testing.T) {
	s, _ := newStore(t)
	ev := retiredEvent("v1")
	key, err := RetiredVersionsSubjectKey(ev.ShellID)
	require.NoError(t, err)
	mustRecord(t, s, Input{
		OpKind: OpKindRetiredVersions, EntityType: "book", EntityIDs: []string{ev.ShellID},
		Decision: DecisionReject, Source: SourceHuman, ActorUserID: "u1", ReasonCode: ReasonOther,
	})
	v := mustLoad(t, s, nil)
	require.True(t, snapRejected(t, v, OpKindRetiredVersions, key, ""), "the reject holds the shell")

	first, err := RecordRetiredApplied(s, ev)
	require.NoError(t, err)
	again, err := RecordRetiredApplied(s, ev)
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID, "a retry writes no second record")
	next := retiredEvent("v2")
	third, err := RecordRetiredApplied(s, next)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, third.ID, "a different version is a different event")

	hist, err := s.History(OpKindRetiredVersions, key)
	require.NoError(t, err)
	require.Len(t, hist, 3, "reject, applied v1, applied v2")
	v = mustLoad(t, s, nil)
	require.True(t, snapRejected(t, v, OpKindRetiredVersions, key, ""), "applied does not lift the reject")

	_, err = RecordRetiredApplied(s, RetiredVersionsEvent{})
	require.ErrorIs(t, err, ErrInvalid)
}
