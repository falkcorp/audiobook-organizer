// file: internal/decisions/retired_versions_test.go
// version: 1.1.0
// guid: 2f8b05d3-7a19-4c64-9e50-c13d6a84b7e9
// last-edited: 2026-10-24

package decisions

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retiredBooks is a fake store of books, retired versions and the retired rows
// they hold.
type retiredBooks struct {
	books    map[string]*database.Book
	versions map[string][]database.BookVersion // shell id -> versions
	files    map[string][]database.BookFile    // holder id -> retired rows
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

func (r *retiredBooks) GetRetiredBookFiles(holder string) ([]database.BookFile, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.files[holder], nil
}

// retire records a retire of shell into holder: the version row and the
// retired row it holds.
func (r *retiredBooks) retire(shell, holder string) database.BookVersion {
	v := r.retireEmpty(shell, holder)
	if r.files == nil {
		r.files = map[string][]database.BookFile{}
	}
	r.files[holder] = append(r.files[holder], database.BookFile{ID: "f-" + v.ID, BookID: holder, VersionID: v.ID, Retired: true})
	return v
}

// retireEmpty is a retire whose rows are gone: the version row alone, as a
// revert (UnretireBookFiles) leaves it.
func (r *retiredBooks) retireEmpty(shell, holder string) database.BookVersion {
	if r.versions == nil {
		r.versions = map[string][]database.BookVersion{}
	}
	v := database.BookVersion{
		ID:     fmt.Sprintf("v-%s-%s-%d", shell, holder, len(r.versions[shell])),
		BookID: holder, Status: database.BookVersionStatusRetired, RetiredFromBookID: shell,
	}
	r.versions[shell] = append(r.versions[shell], v)
	return v
}

// unretire takes a version's rows back, as a revert does, leaving the version.
func (r *retiredBooks) unretire(v database.BookVersion) {
	kept := r.files[v.BookID][:0:0]
	for _, f := range r.files[v.BookID] {
		if f.VersionID != v.ID {
			kept = append(kept, f)
		}
	}
	r.files[v.BookID] = kept
}

func liveBook(id string) *database.Book { return &database.Book{ID: id} }

func trashedBook(id string) *database.Book {
	t := true
	return &database.Book{ID: id, MarkedForDeletion: &t}
}

func TestRetiredBookResolver_FollowsTheIndex(t *testing.T) {
	r := &retiredBooks{
		books: map[string]*database.Book{
			"HOLDER": liveBook("HOLDER"), "TRASHED": trashedBook("TRASHED"),
			"RESTORED": liveBook("RESTORED"), "OTHER": liveBook("OTHER"),
		},
	}
	r.retire("TRASHED", "HOLDER")
	r.retire("PURGED", "HOLDER")
	r.retire("RESTORED", "HOLDER") // stale: the book is live again
	r.versions["BAD"] = []database.BookVersion{{ID: "v1", BookID: "HOLDER", Status: database.BookVersionStatusAlt, RetiredFromBookID: "BAD"}}
	r.versions["WRONG"] = []database.BookVersion{{ID: "v2", BookID: "HOLDER", Status: database.BookVersionStatusRetired, RetiredFromBookID: "ELSEWHERE"}}
	r.files["HOLDER"] = append(r.files["HOLDER"],
		database.BookFile{ID: "f-v1", VersionID: "v1", Retired: true}, database.BookFile{ID: "f-v2", VersionID: "v2", Retired: true})
	res := RetiredBookResolver(r)
	for id, want := range map[string]string{
		"TRASHED":  "HOLDER", // a trashed shell
		"PURGED":   "HOLDER", // its book row is gone; the index is all that is left
		"RESTORED": "RESTORED", "HOLDER": "HOLDER",
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
	r := &retiredBooks{books: map[string]*database.Book{"S": trashedBook("S")}}
	r.retire("S", "H1")
	r.retire("S", "H2")
	r.retire("S", "H3")
	_, err := RetiredBookResolver(r)("book", "S")
	require.ErrorIs(t, err, ErrRetiredShellAmbiguous)
	require.ErrorIs(t, err, ErrIDUnresolvable, "the shell's own data: held per id, not a failed read")
	var ue *UnresolvableIDError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, []string{"H1", "H2", "H3"}, ue.Related, "every candidate holder is named, not the first two")

	r.err = errors.New("store down")
	_, err = RetiredBookResolver(r)("book", "S")
	require.ErrorContains(t, err, "store down")
	require.NotErrorIs(t, err, ErrIDUnresolvable, "a failed read is not an unresolvable id")
}

// Reverting a retire puts the rows back and leaves the version row: the
// emptied version must not redirect the shell, and retiring the shell again
// onto another book must not make it ambiguous.
func TestRetiredBookResolver_AnEmptiedVersionIsIgnored(t *testing.T) {
	r := &retiredBooks{books: map[string]*database.Book{"S": trashedBook("S"), "H1": liveBook("H1"), "H2": liveBook("H2")}}
	res := RetiredBookResolver(r)
	first := r.retire("S", "H1")
	got, err := res("book", "S")
	require.NoError(t, err)
	require.Equal(t, "H1", got)

	r.unretire(first)
	got, err = res("book", "S")
	require.NoError(t, err)
	require.Equal(t, "S", got, "the version holds no rows: the shell is not redirected")

	r.retire("S", "H2")
	got, err = res("book", "S")
	require.NoError(t, err, "reverted then retired again is not ambiguous")
	require.Equal(t, "H2", got)

	// A version emptied by a revert, with no second retire, is no holder either.
	r.retireEmpty("S2", "H1")
	got, err = res("book", "S2")
	require.NoError(t, err)
	require.Equal(t, "S2", got)

	// Rows of ANOTHER version under the holder do not make an empty one count.
	r.retireEmpty("S3", "H1")
	r.retire("S4", "H1")
	got, err = res("book", "S3")
	require.NoError(t, err)
	require.Equal(t, "S3", got)
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
	require.ErrorIs(t, err, ErrResolverCycle)
	require.ErrorIs(t, err, ErrIDUnresolvable)
	var ue *UnresolvableIDError
	require.ErrorAs(t, err, &ue)
	require.Contains(t, ue.Related, "A")
	require.Contains(t, ue.Related, "B")

	boom := errors.New("boom")
	_, err = ChainResolvers(func(string, string) (string, error) { return "", boom })("book", "A")
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrIDUnresolvable)
}

func TestChainPathResolvers_ReturnsEveryHop(t *testing.T) {
	step := func(m map[string]string) PathResolver {
		return PathOf(func(_, id string) (string, error) {
			if n, ok := m[id]; ok {
				return n, nil
			}
			return id, nil
		})
	}
	chain := ChainPathResolvers(step(map[string]string{"B": "C", "D": "E"}), step(map[string]string{"C": "D"}))
	p, err := chain("book", "B")
	require.NoError(t, err)
	require.Equal(t, []string{"B", "C", "D", "E"}, p)
	p, err = chain("book", "Z")
	require.NoError(t, err)
	require.Equal(t, []string{"Z"}, p, "an id that does not move is a path of one")
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

	r := &retiredBooks{books: map[string]*database.Book{holder: liveBook(holder), other: liveBook(other)}} // the shell was purged
	r.retire(shell, holder)
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

// A retry finds its record wherever it sits on the chain, not only at the
// head: another record written in between does not make it a new event.
func TestRecordRetired_RetryAfterAnotherRecordIsStillOneRecord(t *testing.T) {
	s, _ := newStore(t)
	first, err := RecordRetiredApplied(s, retiredEvent("v1"))
	require.NoError(t, err)
	second, err := RecordRetiredApplied(s, retiredEvent("v2"))
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)

	again, err := RecordRetiredApplied(s, retiredEvent("v1"))
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID, "the retry of v1 finds its record under v2's")
	hist, err := s.History(OpKindRetiredVersions, first.SubjectKey)
	require.NoError(t, err)
	require.Len(t, hist, 2)

	// The same version under another op, or the other decision, is a new event.
	other := retiredEvent("v1")
	other.OpID = "op-elsewhere"
	third, err := RecordRetiredApplied(s, other)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, third.ID)
	undone, err := RecordRetiredUndone(s, retiredEvent("v1"))
	require.NoError(t, err)
	require.NotEqual(t, first.ID, undone.ID)
}

// The check and the write are one hold of the subject's lock: concurrent
// retries of one event write one record.
func TestRecordRetired_ConcurrentRetriesWriteOnce(t *testing.T) {
	s, _ := newStore(t)
	ev := retiredEvent("v1")
	const workers = 8
	ids := make([]string, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, err := RecordRetiredApplied(s, ev)
			if err == nil {
				ids[i] = rec.ID
			}
		}()
	}
	wg.Wait()
	for _, id := range ids {
		require.Equal(t, ids[0], id)
		require.NotEmpty(t, id)
	}
	key, err := RetiredVersionsSubjectKey(ev.ShellID)
	require.NoError(t, err)
	hist, err := s.History(OpKindRetiredVersions, key)
	require.NoError(t, err)
	require.Len(t, hist, 1)
}

// Without both the version id and the op id, "the same event" cannot be told
// from any other: an empty pair would match every earlier record that lacks it.
func TestRecordRetired_VersionAndOpAreRequired(t *testing.T) {
	s, _ := newStore(t)
	for name, mut := range map[string]func(*RetiredVersionsEvent){
		"no version": func(e *RetiredVersionsEvent) { e.VersionID = "" },
		"no op":      func(e *RetiredVersionsEvent) { e.OpID = "" },
	} {
		ev := retiredEvent("v1")
		mut(&ev)
		_, err := RecordRetiredApplied(s, ev)
		require.ErrorIs(t, err, ErrInvalid, name)
		_, err = RecordRetiredUndone(s, ev)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	key, err := RetiredVersionsSubjectKey(retiredEvent("v1").ShellID)
	require.NoError(t, err)
	hist, err := s.History(OpKindRetiredVersions, key)
	require.NoError(t, err)
	require.Empty(t, hist, "a refused event writes nothing")
}
