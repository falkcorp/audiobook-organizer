// file: internal/merge/sibling_followups_test.go
// version: 1.1.0
// guid: 3e8c5a71-0d2b-4f69-9a14-7b6e2c8d1f53
// last-edited: 2026-10-05

package merge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// sjFaultStore wraps the fixture's real PebbleStore (embedded, so every
// capability the merge resolves still reaches it) and injects failures into
// the raw journal writes and chosen ModifyBook calls.
type sjFaultStore struct {
	*database.PebbleStore

	mu sync.Mutex
	// failSetRawPrefix fails SetRaw for keys with this prefix.
	failSetRawPrefix string
	// failModify fails the next ModifyBook for each of these ids, once.
	failModify map[string]bool
	// onModify runs before every ModifyBook (outside the book's lock).
	onModify func(id string)
}

func (s *sjFaultStore) SetRaw(key string, value []byte) error {
	if s.failSetRawPrefix != "" && strings.HasPrefix(key, s.failSetRawPrefix) {
		return fmt.Errorf("injected SetRaw failure for %s", key)
	}
	return s.PebbleStore.SetRaw(key, value)
}

func (s *sjFaultStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.mu.Lock()
	hook, fail := s.onModify, s.failModify[id]
	delete(s.failModify, id)
	s.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	if fail {
		return nil, fmt.Errorf("injected ModifyBook failure for %s", id)
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func journalByID(t *testing.T, svc *Service, id string) *SiblingMoveJournal {
	t.Helper()
	j, err := svc.GetSiblingMoveJournal(id)
	require.NoError(t, err)
	return j
}

func onlyJournal(t *testing.T, svc *Service) *SiblingMoveJournal {
	t.Helper()
	js, err := svc.ListSiblingMoveJournals(0)
	require.NoError(t, err)
	require.Len(t, js, 1)
	return &js[0]
}

// R1: undo, re-merge, repeat undo. The repeat is refused and the sibling the
// second merge moved stays where that merge put it.
func TestUndoSiblingMove_RepeatAfterReMergeIsRefused(t *testing.T) {
	f := siblingFixture(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
	svc := NewService(f.S)

	r1, err := svc.MergeBooks([]string{l, k}, k)
	require.NoError(t, err)
	_, err = svc.UndoSiblingMove(r1.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, "G", f.GroupOf(t, ls))

	l2 := f.Book(t, vptest.Spec{ID: "l2", Group: "G", Primary: "false"})
	r2, err := svc.MergeBooks([]string{l2, k}, k)
	require.NoError(t, err)
	require.NotEqual(t, r1.SiblingJournalID, r2.SiblingJournalID)
	require.Equal(t, "H", f.GroupOf(t, ls), "the second merge moved the sibling again")

	before := snapshotBooks(t, f, k, ls, l2)
	_, err = svc.UndoSiblingMove(r1.SiblingJournalID)
	require.ErrorIs(t, err, ErrSiblingUndoRefused)
	require.Equal(t, before, snapshotBooks(t, f, k, ls, l2), "a refused undo writes nothing")
	require.Equal(t, SiblingJournalApplied, journalByID(t, svc, r2.SiblingJournalID).Status)
}

// R1: an undo is refused while a NEWER applied or pending journal names the
// same sibling into the same group, even when the older journal was never
// undone (the sibling went back some other way and was merged again).
func TestUndoSiblingMove_RefusedWhileNewerJournalHoldsSibling(t *testing.T) {
	f := siblingFixture(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
	svc := NewService(f.S)

	r1, err := svc.MergeBooks([]string{l, k}, k)
	require.NoError(t, err)
	// Put the sibling back by hand, not through the journal.
	_, err = f.S.ModifyBook(ls, func(b *database.Book) error {
		g := "G"
		b.VersionGroupID = &g
		return nil
	})
	require.NoError(t, err)
	l2 := f.Book(t, vptest.Spec{ID: "l2", Group: "G", Primary: "true"})
	r2, err := svc.MergeBooks([]string{l2, k}, k)
	require.NoError(t, err)

	for _, status := range []string{SiblingJournalApplied, SiblingJournalPending} {
		j2 := journalByID(t, svc, r2.SiblingJournalID)
		j2.Status = status
		require.NoError(t, svc.putSiblingJournal(j2))
		_, err = svc.UndoSiblingMove(r1.SiblingJournalID)
		require.ErrorIs(t, err, ErrSiblingUndoRefused, "newer journal %s", status)
		require.Equal(t, "H", f.GroupOf(t, ls))
	}

	// Once the newer merge's move is undone, the older journal's undo finds
	// the sibling already back and succeeds.
	_, err = svc.UndoSiblingMove(r2.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, "G", f.GroupOf(t, ls))
	u, err := svc.UndoSiblingMove(r1.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, []string{ls}, u.AlreadyBack)
	require.Equal(t, SiblingJournalUndone, u.Status)
}

// R1: a merge refused after its journal was written and before its first
// write leaves an aborted journal, which cannot be undone, and writes nothing.
func TestMergeBooks_RefusedAfterJournalWriteLeavesAbortedJournal(t *testing.T) {
	cases := map[string]func(s *sjFaultStore, l string){
		// The group redirect is the next write after the journal.
		"redirect write fails": func(s *sjFaultStore, _ string) { s.failSetRawPrefix = groupRedirectPrefix },
		// The first participant's membership write fails.
		"first membership write fails": func(s *sjFaultStore, l string) { s.failModify = map[string]bool{l: true} },
	}
	for name, inject := range cases {
		t.Run(name, func(t *testing.T) {
			f := siblingFixture(t)
			k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
			l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
			ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
			fs := &sjFaultStore{PebbleStore: f.S}
			inject(fs, l)
			svc := NewService(fs)

			before := snapshotBooks(t, f, k, l, ls)
			res, err := svc.MergeBooks([]string{l, k}, k)
			require.Error(t, err)
			require.Nil(t, res)
			var partial *PartialMergeError
			require.False(t, errors.As(err, &partial), "nothing was written, so this is not a partial merge")
			require.Equal(t, before, snapshotBooks(t, f, k, l, ls), "a refused merge writes nothing")

			j := onlyJournal(t, svc)
			require.Equal(t, SiblingJournalAborted, j.Status)
			require.NotEmpty(t, j.LastError)
			r, err := readGroupRedirect(f.S, "G")
			require.NoError(t, err)
			require.Nil(t, r, "a refused merge leaves no group redirect")

			_, err = svc.UndoSiblingMove(j.ID)
			require.ErrorIs(t, err, ErrSiblingUndoRefused)
			require.Equal(t, before, snapshotBooks(t, f, k, l, ls))
		})
	}
}

// R3: the journal is written before the first write, and a journal that
// cannot be written refuses the merge with nothing changed.
func TestMergeBooks_SiblingJournalBeforeFirstWrite(t *testing.T) {
	t.Run("journal write fails", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
		ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
		fs := &sjFaultStore{PebbleStore: f.S, failSetRawPrefix: siblingJournalPrefix}
		svc := NewService(fs)
		before := snapshotBooks(t, f, k, l, ls)
		res, err := svc.MergeBooks([]string{l, k}, k)
		require.Nil(t, res)
		require.ErrorContains(t, err, "no undo record")
		require.Equal(t, before, snapshotBooks(t, f, k, l, ls))
		requireNoSiblingJournal(t, svc)
	})
	t.Run("pending journal exists at the first participant write", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
		ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
		fs := &sjFaultStore{PebbleStore: f.S}
		svc := NewService(fs)
		var seen []string
		fs.onModify = func(id string) {
			if len(seen) > 0 {
				return
			}
			rows, err := f.S.ScanPrefix(siblingJournalPrefix)
			require.NoError(t, err)
			require.Len(t, rows, 1, "the journal must exist before the first write (%s)", id)
			var j SiblingMoveJournal
			require.NoError(t, json.Unmarshal(rows[0].Value, &j))
			seen = append(seen, id, j.Status)
			require.Equal(t, []string{ls}, siblingIDs(j.Siblings))
		}
		_, err := svc.MergeBooks([]string{l, k}, k)
		require.NoError(t, err)
		require.Equal(t, []string{l, SiblingJournalPending}, seen)
	})
}

// R3 + S1: a merge that fails part way through its sibling moves returns a
// PartialMergeError naming the group, the siblings that moved and the
// journal; the journal stays pending and records what moved; the merge's own
// group and the left group are each handed a primary; and the pending
// journal's undo puts back the sibling that moved.
func TestMergeBooks_PartialSiblingFailure(t *testing.T) {
	f := siblingFixture(t)
	// k (the survivor) is not organized, so an organized member holds H's
	// flag: h2, crowned after the sibling moves. The second sibling's move
	// fails first, so H is left with no primary unless the failure hand-off
	// covers the merge's own group too.
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
	f.Book(t, vptest.Spec{ID: "h2", Group: "H", Primary: "false"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true", State: "imported"})
	ls1 := f.Book(t, vptest.Spec{ID: "ls1", Group: "G", Primary: "false"})
	ls2 := f.Book(t, vptest.Spec{ID: "ls2", Group: "G", Primary: "false"})
	fs := &sjFaultStore{PebbleStore: f.S, failModify: map[string]bool{ls2: true}}
	svc := NewService(fs)

	res, err := svc.MergeBooks([]string{l, k}, "")
	require.Nil(t, res)
	var partial *PartialMergeError
	require.True(t, errors.As(err, &partial), "want PartialMergeError, got %v", err)
	require.Equal(t, "H", partial.VersionGroupID)
	require.Equal(t, []string{ls1}, siblingIDs(partial.MovedSiblings))
	require.NotEmpty(t, partial.SiblingJournalID)
	require.ErrorContains(t, err, "injected ModifyBook failure")

	j := journalByID(t, svc, partial.SiblingJournalID)
	require.Equal(t, SiblingJournalPending, j.Status)
	require.Equal(t, []string{ls1}, j.Moved)
	require.Equal(t, []string{ls1, ls2}, siblingIDs(j.Siblings), "the plan is kept whole")
	require.Contains(t, j.LastError, "injected ModifyBook failure")

	require.Equal(t, "H", f.GroupOf(t, ls1))
	require.Equal(t, "G", f.GroupOf(t, ls2))
	require.Equal(t, "false", f.Flag(t, k))
	require.Len(t, f.LivePrimaries(t, "H"), 1, "the failure hand-off gives the merge's own group a primary")
	f.RequireSinglePrimary(t, "G", ls2)

	u, err := svc.UndoSiblingMove(partial.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, []string{ls1}, u.Restored)
	require.Equal(t, []string{ls2}, u.AlreadyBack)
	require.Equal(t, "G", f.GroupOf(t, ls1))
	require.Equal(t, SiblingJournalUndone, u.Status)
	f.RequireSinglePrimary(t, "G", "")
	f.RequireSinglePrimary(t, "H", "")
}

// R2: when a sibling held the merge's group's primary flag, the undo hands
// that group a primary again: exactly one live primary afterwards.
func TestUndoSiblingMove_MergeGroupKeepsOnePrimary(t *testing.T) {
	f := siblingFixture(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
	h2 := f.Book(t, vptest.Spec{ID: "h2", Group: "H", Primary: "false"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "true"})
	svc := NewService(f.S)

	res, err := svc.MergeBooks([]string{l, k}, "")
	require.NoError(t, err)
	require.Equal(t, k, res.PrimaryID)
	require.Equal(t, ls, res.GroupPrimaryID, "the organized sibling, already a primary, holds the flag")
	f.RequireSinglePrimary(t, "H", ls)

	_, err = svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, "G", f.GroupOf(t, ls))
	f.RequireSinglePrimary(t, "H", h2)
	f.RequireSinglePrimary(t, "G", ls)
}

// S5: with no explicit primary the group's flag goes to an organized copy --
// a moved sibling, or a member of the reused group -- so Audiobookshelf still
// lists the title; an explicit primary still wins.
func TestMergeBooks_OrganizedCopyHoldsFlag(t *testing.T) {
	t.Run("moved sibling", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported"})
		ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "true"})
		res, err := NewService(f.S).MergeBooks([]string{l, k}, "")
		require.NoError(t, err)
		require.Equal(t, k, res.PrimaryID, "the survivor is still the participant")
		require.Equal(t, ls, res.GroupPrimaryID)
		require.Equal(t, []string{ls}, absListed(t, f, "H"))
	})
	t.Run("reused-group member", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "false", State: "imported"})
		p := f.Book(t, vptest.Spec{ID: "p", Group: "H", Primary: "true"})
		// No audio route, so k is the survivor and H the merge's group.
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true", State: "imported", NoFile: true})
		res, err := NewService(f.S).MergeBooks([]string{l, k}, "")
		require.NoError(t, err)
		require.Equal(t, k, res.PrimaryID)
		require.Equal(t, p, res.GroupPrimaryID)
		f.RequireSinglePrimary(t, "H", p)
		require.Equal(t, []string{p}, absListed(t, f, "H"))
	})
	t.Run("participant: audio quality picks the survivor", func(t *testing.T) {
		// Owner 2026-10-05 12:03: organized does not pick the copy a merge
		// keeps. The unorganized m4b survives; the organized mp3 is the
		// loser and is retired, so it cannot hold the flag.
		f := siblingFixture(t)
		m4b := f.Book(t, vptest.Spec{ID: "m4b", Group: "A", Primary: "true", State: "imported"})
		mp3 := f.Book(t, vptest.Spec{ID: "mp3", Group: "B", Primary: "true"})
		setAudio(t, f, m4b, "m4b", 256)
		setAudio(t, f, mp3, "mp3", 64)
		res, err := NewService(f.S).MergeBooks([]string{mp3, m4b}, "")
		require.NoError(t, err)
		require.Equal(t, m4b, res.PrimaryID, "the m4b survives")
		b, err := f.S.GetBookByID(mp3)
		require.NoError(t, err)
		require.True(t, b.IsSoftDeleted())
	})
	t.Run("m4b survives and the organized mp3 holds the flag", func(t *testing.T) {
		// The organized low-bitrate mp3 is a version of the loser, so it
		// stays live and takes the flag; the m4b is still the survivor.
		f := siblingFixture(t)
		m4b := f.Book(t, vptest.Spec{ID: "m4b", Group: "H", Primary: "true", State: "imported"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported"})
		mp3 := f.Book(t, vptest.Spec{ID: "mp3", Group: "G", Primary: "true"})
		setAudio(t, f, m4b, "m4b", 256)
		setAudio(t, f, l, "mp3", 32)
		setAudio(t, f, mp3, "mp3", 64)
		res, err := NewService(f.S).MergeBooks([]string{l, m4b}, "")
		require.NoError(t, err)
		require.Equal(t, m4b, res.PrimaryID)
		require.Equal(t, mp3, res.GroupPrimaryID)
		f.RequireSinglePrimary(t, "H", mp3)
		require.Equal(t, []string{mp3}, absListed(t, f, "H"))
	})
	t.Run("survivor with an iTunes PID keeps the flag", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
		files, err := f.S.GetBookFiles(k)
		require.NoError(t, err)
		files[0].ITunesPersistentID = "ABCDEF0123456789"
		require.NoError(t, f.S.UpdateBookFile(files[0].ID, &files[0]))
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported"})
		ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "true"})
		res, err := NewService(f.S).MergeBooks([]string{l, k}, "")
		require.NoError(t, err)
		require.Empty(t, res.GroupPrimaryID, "demoting k would take its track out of iTunes")
		require.Equal(t, HiddenFromABSITunesSurvivor, res.HiddenFromABS, "the hidden title is recorded")
		require.Empty(t, absListed(t, f, "H"))
		f.RequireSinglePrimary(t, "H", k)
		require.Equal(t, "false", f.Flag(t, ls))
	})
	t.Run("explicit primary wins", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported"})
		ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "true"})
		res, err := NewService(f.S).MergeBooks([]string{l, k}, k)
		require.NoError(t, err)
		require.Empty(t, res.GroupPrimaryID)
		f.RequireSinglePrimary(t, "H", k)
		require.Equal(t, "false", f.Flag(t, ls))
	})
}

// Pre-existing: a reused-group member the merge demotes gets the scan-state
// and iTunes refusals before anything is written; one it leaves alone (an
// explicit false) does not block the merge.
func TestMergeBooks_GuardsDemotedReusedGroupMembers(t *testing.T) {
	t.Run("provisional member", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "false"})
		p := f.Book(t, vptest.Spec{ID: "p", Group: "H", Primary: "true"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
		files, err := f.S.GetBookFiles(p)
		require.NoError(t, err)
		files[0].Scan.NeedsDeep = true
		require.NoError(t, f.S.UpdateBookFile(files[0].ID, &files[0]))
		before := snapshotBooks(t, f, k, p, l)
		svc := NewService(f.S)
		_, err = svc.MergeBooks([]string{l, k}, k)
		var prov *ProvisionalScanError
		require.True(t, errors.As(err, &prov), "want ProvisionalScanError, got %v", err)
		require.Equal(t, p, prov.BookID)
		require.Equal(t, before, snapshotBooks(t, f, k, p, l))
		requireNoSiblingJournal(t, svc)
		r, err := readGroupRedirect(f.S, "G")
		require.NoError(t, err)
		require.Nil(t, r)
	})
	t.Run("iTunes member", func(t *testing.T) {
		f := siblingFixture(t)
		withITunesConfig(t, func(c *config.ITunesConfig) {
			c.LibraryReadPath = testITunesLibFolder + "/iTunes Library.itl"
			c.MediaRoot = testITunesMedia
		})
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "false"})
		p := f.Book(t, vptest.Spec{ID: "p", Group: "H", Primary: "true", NoFile: true})
		_, err := f.S.ModifyBook(p, func(b *database.Book) error {
			b.FilePath = testITunesMedia + "/Author/Book/p.m4b"
			return nil
		})
		require.NoError(t, err)
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
		before := snapshotBooks(t, f, k, p, l)
		_, err = NewService(f.S).MergeBooks([]string{l, k}, k)
		var prot *ITunesProtectedError
		require.True(t, errors.As(err, &prot), "want ITunesProtectedError, got %v", err)
		require.Equal(t, before, snapshotBooks(t, f, k, p, l))
	})
	t.Run("member already explicit false is not guarded", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
		q := f.Book(t, vptest.Spec{ID: "q", Group: "H", Primary: "false"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
		files, err := f.S.GetBookFiles(q)
		require.NoError(t, err)
		files[0].Scan.NeedsDeep = true
		require.NoError(t, f.S.UpdateBookFile(files[0].ID, &files[0]))
		_, err = NewService(f.S).MergeBooks([]string{l, k}, k)
		require.NoError(t, err)
	})
}

// S2: after a successful merge each left group is handed off, so a book a
// lock-free writer (the scanner's new-row CreateBook) put in it during the
// merge is not left without a primary.
func TestMergeBooks_LeftGroupBackstopHandOff(t *testing.T) {
	f := siblingFixture(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	fs := &sjFaultStore{PebbleStore: f.S}
	svc := NewService(fs)
	var once sync.Once
	fs.onModify = func(id string) {
		if id != k {
			return
		}
		once.Do(func() {
			// No group lock, as the scanner's CreateBook takes none.
			f.Book(t, vptest.Spec{ID: "x", Group: "G", Primary: "false"})
		})
	}
	_, err := svc.MergeBooks([]string{l, k}, k)
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "G", "x")
}

// S3: a member of the loser's group that was already in the trash when the
// merge emptied the group is restored into the merge's group, with its
// family; after an undo puts a sibling back, it is restored into its own
// group again.
func TestRestoreFromTrash_FollowsEmptiedGroupRedirect(t *testing.T) {
	setup := func(t *testing.T) (*vptest.Fixture, *Service, string, string, string, *Result) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
		ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
		d := f.Book(t, vptest.Spec{ID: "d", Group: "G", Primary: "false"})
		_, err := f.S.ModifyBook(d, func(b *database.Book) error {
			v, at := true, time.Now().Add(-time.Hour)
			b.MarkedForDeletion, b.MarkedForDeletionAt = &v, &at
			return nil
		})
		require.NoError(t, err)
		svc := NewService(f.S)
		res, err := svc.MergeBooks([]string{l, k}, k)
		require.NoError(t, err)
		require.Equal(t, "G", f.GroupOf(t, d), "the merge does not touch the trashed row")
		return f, svc, k, ls, d, res
	}
	t.Run("restore lands with the family", func(t *testing.T) {
		f, _, k, _, d, _ := setup(t)
		r, err := RestoreFromTrash(f.S, d, nil, nil)
		require.NoError(t, err)
		require.True(t, r.Restored)
		require.Equal(t, "H", f.GroupOf(t, d))
		f.RequireSinglePrimary(t, "H", k)
	})
	t.Run("after an undo the group is a family again", func(t *testing.T) {
		f, svc, _, ls, d, res := setup(t)
		_, err := svc.UndoSiblingMove(res.SiblingJournalID)
		require.NoError(t, err)
		require.Equal(t, "G", f.GroupOf(t, ls))
		r, err := readGroupRedirect(f.S, "G")
		require.NoError(t, err)
		require.Nil(t, r, "the undo clears the redirect")
		_, err = RestoreFromTrash(f.S, d, nil, nil)
		require.NoError(t, err)
		require.Equal(t, "G", f.GroupOf(t, d))
	})
	t.Run("a row trashed after the merge is not redirected", func(t *testing.T) {
		f, _, _, _, d, _ := setup(t)
		_, err := f.S.ModifyBook(d, func(b *database.Book) error {
			at := time.Now().Add(time.Hour)
			b.MarkedForDeletionAt = &at
			return nil
		})
		require.NoError(t, err)
		_, err = RestoreFromTrash(f.S, d, nil, nil)
		require.NoError(t, err)
		require.Equal(t, "G", f.GroupOf(t, d))
	})
}

// S4: two losers from different groups. Undoing one loser's siblings leaves
// the other's in place and the journal applied; undoing the second finishes
// it.
func TestUndoSiblingMoveForLoser_TwoLosersInDifferentGroups(t *testing.T) {
	f := siblingFixture(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l1 := f.Book(t, vptest.Spec{ID: "l1", Group: "G1", Primary: "true"})
	s1 := f.Book(t, vptest.Spec{ID: "s1", Group: "G1", Primary: "false"})
	l2 := f.Book(t, vptest.Spec{ID: "l2", Group: "G2", Primary: "true"})
	s2 := f.Book(t, vptest.Spec{ID: "s2", Group: "G2", Primary: "false"})
	svc := NewService(f.S)
	res, err := svc.MergeBooks([]string{l1, l2, k}, k)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{s1, s2}, siblingIDs(res.MovedSiblings))

	u, err := svc.UndoSiblingMoveForLoser(res.SiblingJournalID, l1)
	require.NoError(t, err)
	require.Equal(t, []string{s1}, u.Restored)
	require.Equal(t, SiblingJournalApplied, u.Status)
	require.Equal(t, "G1", f.GroupOf(t, s1))
	require.Equal(t, "H", f.GroupOf(t, s2))

	// A repeat for the same loser has nothing left to do.
	u, err = svc.UndoSiblingMoveForLoser(res.SiblingJournalID, l1)
	require.NoError(t, err)
	require.Empty(t, u.Restored)

	u, err = svc.UndoSiblingMoveForLoser(res.SiblingJournalID, l2)
	require.NoError(t, err)
	require.Equal(t, []string{s2}, u.Restored)
	require.Equal(t, SiblingJournalUndone, u.Status)
	require.Equal(t, "G2", f.GroupOf(t, s2))
	f.RequireSinglePrimary(t, "H", k)
}

// NIT: the list marks a journal still pending after
// SiblingJournalStaleAfter as stale, and only that one.
func TestListSiblingMoveJournals_MarksStalePending(t *testing.T) {
	f := siblingFixture(t)
	svc := NewService(f.S)
	old := newSiblingJournal("k", "H", []string{"l"}, nil)
	old.CreatedAt = time.Now().UTC().Add(-2 * SiblingJournalStaleAfter)
	fresh := newSiblingJournal("k", "H", []string{"l"}, nil)
	oldApplied := newSiblingJournal("k", "H", []string{"l"}, nil)
	oldApplied.CreatedAt = old.CreatedAt
	oldApplied.Status = SiblingJournalApplied
	for _, j := range []*SiblingMoveJournal{old, fresh, oldApplied} {
		require.NoError(t, svc.putSiblingJournal(j))
	}
	js, err := svc.ListSiblingMoveJournals(0)
	require.NoError(t, err)
	stale := map[string]bool{}
	for _, j := range js {
		stale[j.ID] = j.Stale
	}
	require.Equal(t, map[string]bool{old.ID: true, fresh.ID: false, oldApplied.ID: false}, stale)
	got := journalByID(t, svc, old.ID)
	require.False(t, got.Stale, "Stale is computed, never stored")
}

func absListed(t *testing.T, f *vptest.Fixture, gid string) []string {
	t.Helper()
	members, err := f.S.GetBooksByVersionGroup(gid)
	require.NoError(t, err)
	var out []string
	for i := range members {
		if database.ABSLibraryFilter().Matches(&members[i]) {
			out = append(out, members[i].ID)
		}
	}
	return out
}

func setAudio(t *testing.T, f *vptest.Fixture, id, format string, kbps int) {
	t.Helper()
	_, err := f.S.ModifyBook(id, func(b *database.Book) error {
		b.Format = format
		b.Bitrate = &kbps
		return nil
	})
	require.NoError(t, err)
}

// SF2: the losers' user state and sync redirect go to the flag holder (the
// book Audiobookshelf shows) when it is not the survivor, journaled; a whole
// undo that sends the flag holder back puts the loser's state on the
// survivor instead, and a per-loser undo puts it back on the loser.
func TestMergeBooks_UserStateFollowsFlagHolder(t *testing.T) {
	setup := func(t *testing.T) (*vptest.Fixture, *Service, *database.User, string, string, string, *Result) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
		// No audio route, so the user state on l does not make it the
		// survivor (PreferUserStateSurvivor keeps a book with audio).
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported", NoFile: true})
		ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "true"})
		user := seedSyncUser(t, f.S)
		require.NoError(t, f.S.SetUserBookState(&database.UserBookState{
			UserID: user.ID, BookID: l, Status: database.UserBookStatusInProgress,
			ProgressPct: 60, LastActivityAt: time.Now(),
		}))
		require.NoError(t, f.S.SetUserPosition(user.ID, l, "seg-1", 1234))
		ids := database.AsSyncIdentityStore(f.S)
		_, err := ids.MintOrGetSyncID(l)
		require.NoError(t, err)
		svc := NewService(f.S)
		res, err := svc.MergeBooks([]string{l, k}, "")
		require.NoError(t, err)
		require.Equal(t, k, res.PrimaryID)
		require.Equal(t, ls, res.GroupPrimaryID)
		require.Equal(t, ls, res.StateHolderID)
		return f, svc, user, k, l, ls, res
	}
	progressOn := func(t *testing.T, f *vptest.Fixture, userID, bookID string) int {
		t.Helper()
		st, err := f.S.GetUserBookState(userID, bookID)
		require.NoError(t, err)
		pos, err := f.S.ListUserPositionsForBook(userID, bookID)
		require.NoError(t, err)
		if st == nil || len(pos) == 0 {
			return 0
		}
		return st.ProgressPct
	}
	redirectOf := func(t *testing.T, f *vptest.Fixture, bookID string) string {
		t.Helper()
		ids := database.AsSyncIdentityStore(f.S)
		sid, _, err := ids.GetSyncIDForBook(bookID)
		require.NoError(t, err)
		item, err := ids.ResolveSyncItem(sid)
		require.NoError(t, err)
		require.NotNil(t, item)
		return item.CurrentBookID
	}

	t.Run("merge moves state onto the flag holder", func(t *testing.T) {
		f, svc, user, k, l, ls, res := setup(t)
		require.Equal(t, 60, progressOn(t, f, user.ID, ls))
		require.Zero(t, progressOn(t, f, user.ID, k))
		require.Equal(t, ls, redirectOf(t, f, l), "the loser's ABS id reaches the flag holder")
		j := journalByID(t, svc, res.SiblingJournalID)
		require.Equal(t, ls, j.FlagHolderID)
		require.Len(t, j.StateFollows, 1)
		require.Equal(t, l, j.StateFollows[0].LoserID)
	})
	t.Run("whole undo puts it on the survivor", func(t *testing.T) {
		f, svc, user, k, l, ls, res := setup(t)
		_, err := svc.UndoSiblingMove(res.SiblingJournalID)
		require.NoError(t, err)
		require.Equal(t, "G", f.GroupOf(t, ls))
		require.Zero(t, progressOn(t, f, user.ID, ls), "the flag holder's own (empty) state is back")
		require.Equal(t, 60, progressOn(t, f, user.ID, k))
		require.Equal(t, k, redirectOf(t, f, l))
	})
	t.Run("per-loser undo puts it back on the loser", func(t *testing.T) {
		f, svc, user, _, l, ls, res := setup(t)
		_, err := svc.UndoSiblingMoveForLoser(res.SiblingJournalID, l)
		require.NoError(t, err)
		require.Zero(t, progressOn(t, f, user.ID, ls))
		require.Equal(t, 60, progressOn(t, f, user.ID, l))
	})
}
