// file: internal/books/store/dedup_automerge_journal_rows_test.go
// version: 1.1.0
// guid: 6a3d8e15-c2f7-49b0-8d14-e5b09a7c3f62
// last-edited: 2026-11-17

package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A provisional row lists EVERY participant, and holds a floor for each, not only
// for its own winner and loser. The book here is neither: only the participant
// list reaches it.
func TestPrune_ProvisionalRowPinsEveryParticipantItLists(t *testing.T) {
	f := newPinFixture(t)
	bystander, ts := f.bookWithSnapshots(t, "Alfred", 6)
	_, err := f.es.PutAutoMergeJournalEntry(AutoMergeJournalEntry{
		WinnerID: "batman", LoserID: "robin", MergedAt: time.Now().UnixNano(), Provisional: true,
		WinnerBaselineTS: 1, LoserBaselineTS: 1, BaselinesRecorded: true,
		Participants: []AutoMergeJournalParticipant{
			{BookID: "batman", BaselineTS: 1}, {BookID: "robin", BaselineTS: 1}, {BookID: bystander, BaselineTS: ts[1]},
		},
	})
	require.NoError(t, err)

	deleted, err := f.s.PruneBookSnapshots(bystander, 1)
	require.NoError(t, err)
	require.Equal(t, 4, deleted, "6 snapshots: the newest and the first above the participant's baseline stay")
	require.Equal(t, []int64{ts[2], ts[5]}, f.timestamps(t, bystander))
}

// A row from before the participant list existed pins its winner and loser and
// nothing else: it is read exactly as it always was.
func TestPrune_ProvisionalRowWithoutParticipantsPinsOnlyItsWinnerAndLoser(t *testing.T) {
	f := newPinFixture(t)
	bystander, bts := f.bookWithSnapshots(t, "Alfred", 5)
	winner, wts := f.bookWithSnapshots(t, "Batman", 5)
	old := `{"winner_id":"` + winner + `","loser_id":"robin","merged_at":1,"provisional":true,` +
		`"winner_baseline_ts":` + itoa(wts[1]) + `,"loser_baseline_ts":1,"baselines_recorded":true}`
	require.NoError(t, f.s.SetRaw(dedupAutoMergeJournalPfx+"0000000000000001", []byte(old)))
	autoMergeJournalGen.Add(1)

	_, err := f.s.PruneBookSnapshots(bystander, 1)
	require.NoError(t, err)
	require.Equal(t, []int64{bts[4]}, f.timestamps(t, bystander), "a book the old row does not name keeps only the newest")
	_, err = f.s.PruneBookSnapshots(winner, 1)
	require.NoError(t, err)
	require.Equal(t, []int64{wts[2], wts[4]}, f.timestamps(t, winner), "the row's own winner is still pinned from its baseline")
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// A row written now is still readable by a binary that knows only the earlier
// fields: the winner and loser baselines it reads are there, and the new fields
// are additions it ignores.
func TestAutoMergeJournalEntry_NewFieldsAreAdditionsAnOlderReaderIgnores(t *testing.T) {
	e := AutoMergeJournalEntry{
		WinnerID: "batman", LoserID: "robin", Provisional: true, MergedAt: 9, RowID: "r1",
		WinnerBaselineTS: 11, LoserBaselineTS: 12, BaselinesRecorded: true,
		Participants: []AutoMergeJournalParticipant{{BookID: "batman", BaselineTS: 11}, {BookID: "robin", BaselineTS: 12}},
	}
	data, err := json.Marshal(e)
	require.NoError(t, err)
	var older struct {
		WinnerID          string `json:"winner_id"`
		LoserID           string `json:"loser_id"`
		Provisional       bool   `json:"provisional"`
		WinnerBaselineTS  int64  `json:"winner_baseline_ts"`
		LoserBaselineTS   int64  `json:"loser_baseline_ts"`
		BaselinesRecorded bool   `json:"baselines_recorded"`
	}
	require.NoError(t, json.Unmarshal(data, &older))
	require.Equal(t, "batman", older.WinnerID)
	require.True(t, older.Provisional)
	require.Equal(t, int64(11), older.WinnerBaselineTS)
	require.Equal(t, int64(12), older.LoserBaselineTS)
	require.True(t, older.BaselinesRecorded)

	var plain AutoMergeJournalEntry
	require.NoError(t, json.Unmarshal([]byte(`{"winner_id":"a","loser_id":"b","provisional":true}`), &plain))
	require.Empty(t, plain.Participants)
	require.Empty(t, plain.RowID)
}

func TestPutAutoMergeJournalEntryIfAbsent_NeverReplacesAndOnlyAWriteMovesTheGeneration(t *testing.T) {
	f := newPinFixture(t)
	first := AutoMergeJournalEntry{WinnerID: "batman", LoserID: "robin", MergedAt: 42, Tag: "first"}
	g0 := AutoMergeJournalGeneration()
	key, created, err := f.es.PutAutoMergeJournalEntryIfAbsent(first)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, string(autoMergeJournalKey(42)), key)
	g1 := AutoMergeJournalGeneration()
	require.Greater(t, g1, g0, "a write moves the generation, so a prune re-plans")

	second := AutoMergeJournalEntry{WinnerID: "alfred", LoserID: "catwoman", MergedAt: 42, Tag: "second"}
	key2, created, err := f.es.PutAutoMergeJournalEntryIfAbsent(second)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, key, key2)
	require.Equal(t, g1, AutoMergeJournalGeneration(), "nothing was written, so nothing moved")
	got, err := f.es.GetAutoMergeJournalEntry(key)
	require.NoError(t, err)
	require.Equal(t, "first", got.Tag)
	require.Equal(t, "batman", got.WinnerID)
}

func TestCloseAutoMergeJournalEntry_ClosesOnlyTheRowItWasGivenTheIdentityOf(t *testing.T) {
	f := newPinFixture(t)
	mine := AutoMergeJournalEntry{WinnerID: "batman", LoserID: "robin", MergedAt: 100, Provisional: true, RowID: "mine", Tag: "mine"}
	key, created, err := f.es.PutAutoMergeJournalEntryIfAbsent(mine)
	require.NoError(t, err)
	require.True(t, created)

	for name, rowID := range map[string]string{"another merge's identity": "theirs", "blank identity": ""} {
		g := AutoMergeJournalGeneration()
		closed, cerr := f.es.CloseAutoMergeJournalEntry(key, rowID, 777)
		require.NoError(t, cerr, name)
		require.False(t, closed, name)
		require.Equal(t, g, AutoMergeJournalGeneration(), "%s: nothing was written", name)
		got, gerr := f.es.GetAutoMergeJournalEntry(key)
		require.NoError(t, gerr)
		require.Zero(t, got.AbortedAt, name)
	}
	closed, err := f.es.CloseAutoMergeJournalEntry(string(autoMergeJournalKey(555)), "mine", 777)
	require.NoError(t, err)
	require.False(t, closed, "an absent row is not closed into existence")
	absent, gerr := f.es.GetAutoMergeJournalEntry(string(autoMergeJournalKey(555)))
	require.NoError(t, gerr)
	require.Nil(t, absent)

	g := AutoMergeJournalGeneration()
	closed, err = f.es.CloseAutoMergeJournalEntry(key, "mine", 777)
	require.NoError(t, err)
	require.True(t, closed)
	require.Greater(t, AutoMergeJournalGeneration(), g, "closing a row releases pins, so the generation moves")
	got, err := f.es.GetAutoMergeJournalEntry(key)
	require.NoError(t, err)
	require.Equal(t, int64(777), got.AbortedAt)
	require.True(t, got.Provisional, "it stays provisional for a binary that predates AbortedAt")
	require.Equal(t, "mine", got.Tag, "the rest of the row is as it was written")

	// Once the merge patched the row (no RowID any more) nobody can close it.
	patched := AutoMergeJournalEntry{WinnerID: "batman", LoserID: "robin", MergedAt: 100, WinnerPreMergeTS: 3, LoserPreMergeTS: 4}
	_, err = f.es.PutAutoMergeJournalEntry(patched)
	require.NoError(t, err)
	for _, id := range []string{"mine", ""} { // a blank identity must not match a row that has none
		closed, err = f.es.CloseAutoMergeJournalEntry(key, id, 888)
		require.NoError(t, err)
		require.False(t, closed, "identity %q", id)
	}
	got, err = f.es.GetAutoMergeJournalEntry(key)
	require.NoError(t, err)
	require.Zero(t, got.AbortedAt)
	require.Equal(t, int64(3), got.WinnerPreMergeTS)
}

// The failure memo ends early on a journal write, not only when its TTL runs out:
// a repaired row plus any journal write is read at once.
func TestSnapshotPins_AJournalWriteEndsTheFailureMemoBeforeItsTTL(t *testing.T) {
	f := newPinFixture(t)
	f.bookWithSnapshots(t, "Batman", 3)
	badKey := dedupAutoMergeJournalPfx + "ffffffffffffffff"
	require.NoError(t, f.s.SetRaw(badKey, []byte("{not json")))
	autoMergeJournalGen.Add(1)
	now := time.Now()
	pinClock = func() time.Time { return now }
	t.Cleanup(func() { pinClock = time.Now })

	var unreadable *MergeJournalRowUnreadableError
	require.ErrorAs(t, f.s.CheckSnapshotPins(), &unreadable)
	require.NoError(t, f.s.SetRaw(badKey, []byte(`{"winner_id":"a","loser_id":"b"}`)))
	require.ErrorAs(t, f.s.CheckSnapshotPins(), &unreadable, "inside the TTL and with no journal write: remembered")

	_, err := f.es.PutAutoMergeJournalEntry(AutoMergeJournalEntry{WinnerID: "x", LoserID: "y", MergedAt: 5, WinnerPreMergeTS: 1})
	require.NoError(t, err)
	require.NoError(t, f.s.CheckSnapshotPins(), "a journal write ends the memo at once, with the TTL not yet run out")
}

// A closed row (its merge was refused before it wrote) still holds its loser from
// the trash purge: only an undone row releases it. The row is provisional, and a
// provisional row's loser may have been soft-deleted.
func TestOpenAutoMergeLosers_AClosedRowStillHoldsItsLoser(t *testing.T) {
	f := newPinFixture(t)
	key, created, err := f.es.PutAutoMergeJournalEntryIfAbsent(AutoMergeJournalEntry{
		WinnerID: "batman", LoserID: "robin", MergedAt: 300, Provisional: true, RowID: "r",
	})
	require.NoError(t, err)
	require.True(t, created)
	closed, err := f.es.CloseAutoMergeJournalEntry(key, "r", 301)
	require.NoError(t, err)
	require.True(t, closed)

	open, err := f.es.OpenAutoMergeLosers()
	require.NoError(t, err)
	require.Equal(t, key, open.Losers["robin"], "a closed row still holds its loser")

	_, err = f.es.PutAutoMergeJournalEntry(AutoMergeJournalEntry{
		WinnerID: "batman", LoserID: "robin", MergedAt: 300, Provisional: true, RowID: "r", UndoneAt: 5,
	})
	require.NoError(t, err)
	open, err = f.es.OpenAutoMergeLosers()
	require.NoError(t, err)
	require.NotContains(t, open.Losers, "robin", "an undone row releases it")
}

// A live provisional row holds EVERY book it lists from the trash purge, not only
// its own loser. Its LoserID is only a prediction (the row's winner is the
// PREDICTED winner), so when the prediction was wrong and the row could not be
// finalized, the predicted winner, a real loser, is named by no row's LoserID and
// only the participant list reaches it.
func TestOpenAutoMergeLosers_ALiveProvisionalRowHoldsEveryParticipant(t *testing.T) {
	f := newPinFixture(t)
	key, created, err := f.es.PutAutoMergeJournalEntryIfAbsent(AutoMergeJournalEntry{
		WinnerID: "batman", LoserID: "nightwing", MergedAt: 400, Provisional: true, RowID: "r",
		BaselinesRecorded: true,
		Participants: []AutoMergeJournalParticipant{
			{BookID: "batman"}, {BookID: "nightwing"}, {BookID: "robin"},
		},
	})
	require.NoError(t, err)
	require.True(t, created)

	open, err := f.es.OpenAutoMergeLosers()
	require.NoError(t, err)
	for _, id := range []string{"batman", "nightwing", "robin"} {
		require.Equal(t, key, open.Losers[id], "%s is a participant of a live provisional row", id)
	}
	require.NotContains(t, open.Winners, "batman", "a provisional row still does not hold a winner as a winner")
	require.NotContains(t, open.Participants, "nightwing", "its own loser is a loser hold, not a participant hold")
	require.Equal(t, map[string]string{"batman": key, "robin": key}, open.Participants,
		"the books held only as participants are marked, so a hold reason can say so")
}

// Only a LIVE provisional row widens the hold: a closed one describes a merge that
// never wrote, so its other participants were never touched by it; an undone one
// releases everything; a finalized one names its two books exactly.
func TestOpenAutoMergeLosers_OnlyALiveProvisionalRowHoldsItsOtherParticipants(t *testing.T) {
	parts := []AutoMergeJournalParticipant{{BookID: "batman"}, {BookID: "robin"}, {BookID: "batgirl"}}
	for name, tc := range map[string]struct {
		entry AutoMergeJournalEntry
		holds bool
	}{
		"live provisional": {AutoMergeJournalEntry{Provisional: true, Participants: parts}, true},
		"closed":           {AutoMergeJournalEntry{Provisional: true, AbortedAt: 7, Participants: parts}, false},
		"undone":           {AutoMergeJournalEntry{Provisional: true, UndoneAt: 7, Participants: parts}, false},
		"finalized":        {AutoMergeJournalEntry{Participants: parts}, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPinFixture(t)
			e := tc.entry
			e.WinnerID, e.LoserID, e.MergedAt = "batman", "robin", 500
			_, err := f.es.PutAutoMergeJournalEntry(e)
			require.NoError(t, err)
			open, err := f.es.OpenAutoMergeLosers()
			require.NoError(t, err)
			if tc.holds {
				require.Contains(t, open.Losers, "batgirl")
			} else {
				require.NotContains(t, open.Losers, "batgirl")
			}
			if name != "undone" {
				require.Contains(t, open.Losers, "robin", "its own loser is held as it always was")
			}
		})
	}
}

// A row with no participant list (written before it existed) holds its loser and
// nothing else, as it always did.
func TestOpenAutoMergeLosers_AProvisionalRowWithoutParticipantsHoldsOnlyItsLoser(t *testing.T) {
	f := newPinFixture(t)
	old := `{"winner_id":"batman","loser_id":"robin","merged_at":1,"provisional":true,"baselines_recorded":true}`
	require.NoError(t, f.s.SetRaw(dedupAutoMergeJournalPfx+"0000000000000001", []byte(old)))

	open, err := f.es.OpenAutoMergeLosers()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"robin": dedupAutoMergeJournalPfx + "0000000000000001"}, open.Losers)
}

// When one book is both a row's own loser and another live provisional row's
// participant, the hold names the row that has it as its LOSER, whichever row the
// scan meets first.
func TestOpenAutoMergeLosers_ALoserKeyIsNeverReplacedByAParticipantKey(t *testing.T) {
	f := newPinFixture(t)
	parts := []AutoMergeJournalParticipant{{BookID: "batman"}, {BookID: "robin"}, {BookID: "batgirl"}}
	for _, e := range []AutoMergeJournalEntry{
		{WinnerID: "batman", LoserID: "robin", MergedAt: 600, Provisional: true, Participants: parts},
		{WinnerID: "batman", LoserID: "batgirl", MergedAt: 601, Provisional: true, Participants: parts},
	} {
		_, err := f.es.PutAutoMergeJournalEntry(e)
		require.NoError(t, err)
	}
	open, err := f.es.OpenAutoMergeLosers()
	require.NoError(t, err)
	require.Equal(t, string(autoMergeJournalKey(601)), open.Losers["batgirl"])
	require.Equal(t, string(autoMergeJournalKey(600)), open.Losers["robin"], "the later row lists robin only as a participant")
	require.Equal(t, map[string]string{"batman": string(autoMergeJournalKey(600))}, open.Participants,
		"a book that is some row's own loser is never marked as a participant hold")
}
