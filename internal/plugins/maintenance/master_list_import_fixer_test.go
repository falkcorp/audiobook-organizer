// file: internal/plugins/maintenance/master_list_import_fixer_test.go
// version: 1.0.0
// guid: 91c3d7e5-0a48-4b6f-8e12-d5f7a3b9c640
// last-edited: 2026-10-23

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/masterlist"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Every title, author, identifier and user here is synthetic: the ISBNs are
// the standard valid-checksum examples, the ASINs are fake.

const (
	// 9780306406157 and 0306406152 are one book (the ISBN-10 converts to the ISBN-13).
	mlISBN13 = "9780306406157"
	mlISBN10 = "0306406152"
	// A second valid ISBN-13 (978-0-306-40615-7 is above; this one is 978-0-14-032872-1).
	mlISBN13b = "9780140328721"
)

type mlLab struct {
	t     *testing.T
	store *database.PebbleStore
	p     *Plugin
	fixer *masterListImportFixer
	user  string
	ids   map[string]string
	now   time.Time
}

func newMLLab(t *testing.T) *mlLab {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	u, err := st.CreateUser("listener", "listener@example.invalid", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	p := &Plugin{deps: fakeDeps{store: st}, standDownWait: noWait}
	f := newMasterListImportFixer(p)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	f.now = func() time.Time { return now }
	return &mlLab{t: t, store: st, p: p, fixer: f, user: u.ID, ids: map[string]string{}, now: now}
}

type mlBook struct {
	title, author, asin, isbn13, isbn10, pid, series string
	vg                                               string
	primary                                          *bool
	minutes                                          int
	trashed                                          bool
	mergedInto                                       string
}

func (l *mlLab) book(key string, b mlBook) string {
	l.t.Helper()
	nb := &database.Book{Title: b.title, Format: "m4b", FilePath: "/lib/" + key}
	for dst, v := range map[**string]string{&nb.ASIN: b.asin, &nb.ISBN13: b.isbn13, &nb.ISBN10: b.isbn10, &nb.ITunesPersistentID: b.pid} {
		if v != "" {
			v := v
			*dst = &v
		}
	}
	if b.author != "" {
		a, err := l.store.GetAuthorByName(b.author)
		require.NoError(l.t, err)
		if a == nil {
			a, err = l.store.CreateAuthor(b.author)
			require.NoError(l.t, err)
		}
		nb.AuthorID = &a.ID
	}
	if b.series != "" {
		s, err := l.store.GetSeriesByName(b.series, nb.AuthorID)
		require.NoError(l.t, err)
		if s == nil {
			s, err = l.store.CreateSeries(b.series, nb.AuthorID)
			require.NoError(l.t, err)
		}
		nb.SeriesID = &s.ID
	}
	if b.vg != "" {
		nb.VersionGroupID = &b.vg
		nb.IsPrimaryVersion = b.primary
	}
	if b.trashed {
		nb.MarkedForDeletion = new(true)
		now := l.now
		nb.MarkedForDeletionAt = &now
	}
	if b.mergedInto != "" {
		into := l.ids[b.mergedInto]
		require.NotEmpty(l.t, into, "survivor %q must be created first", b.mergedInto)
		nb.MergedIntoBookID = &into
	}
	created, err := l.store.CreateBook(nb)
	require.NoError(l.t, err)
	if b.minutes > 0 {
		require.NoError(l.t, l.store.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: "/lib/" + key + "/01.m4b",
			Format: "m4b", Duration: b.minutes * 60}))
	}
	l.ids[key] = created.ID
	return created.ID
}

const mlCSVHeader = "title,author,asin,isbn13,isbn10,source,source_id,runtime_min,status,finished_at,last_listened_at,series,series_position"

// csvLine is one list row, the cells in mlCSVHeader order (no commas inside).
func csvLine(cells ...string) string {
	for len(cells) < 13 {
		cells = append(cells, "")
	}
	return strings.Join(cells, ",")
}

// upload stores a list of csv lines (after the header) the way the endpoint
// does: parse, one JSON document per row, PutList.
func (l *mlLab) upload(lines ...string) string {
	l.t.Helper()
	rows, err := masterlist.ParseCSV(strings.NewReader(mlCSVHeader + "\n" + strings.Join(lines, "\n") + "\n"))
	require.NoError(l.t, err)
	raw := make([][]byte, len(rows))
	for i := range rows {
		raw[i], err = json.Marshal(rows[i])
		require.NoError(l.t, err)
	}
	meta, err := database.NewMasterListStore(l.store.DB()).PutList(database.MasterListMeta{
		Filename: "synthetic.csv", Format: masterlist.FormatCSV, UploadedBy: l.user}, raw)
	require.NoError(l.t, err)
	return meta.ID
}

func (l *mlLab) params(listID string) json.RawMessage {
	b, err := json.Marshal(map[string]string{"list_id": listID, "target_user_id": l.user})
	require.NoError(l.t, err)
	return b
}

func (l *mlLab) plan(listID string) (*repairs.PlanResult, map[string]repairs.Row) {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	res, err := repairs.RunPlan(context.Background(), l.fixer, l.params(listID), l.p.repairsPlanDeps(l.store, series), &fakeReporter{})
	require.NoError(l.t, err)
	stored := roundTripPlan(l.t, res)
	byID := map[string]repairs.Row{}
	for _, r := range stored.Rows {
		byID[r.RowID] = r
	}
	require.Len(l.t, byID, len(stored.Rows), "row ids are unique")
	return &stored, byID
}

func memberOf(r repairs.Row, role string) []string {
	var out []string
	for _, m := range r.Members {
		if m.Role == role {
			out = append(out, m.BookID)
		}
	}
	return out
}

func rowState(t *testing.T, r repairs.Row) mlRowState {
	t.Helper()
	var st mlRowState
	require.NoError(t, json.Unmarshal(r.State, &st))
	return st
}

func TestMasterListImport_EveryClassAndTier(t *testing.T) {
	l := newMLLab(t)
	l.book("alpha", mlBook{title: "Alpha Voyage", author: "Ann Author", asin: "B000000001", minutes: 600})
	l.book("beta", mlBook{title: "Beta Harbor", author: "Bob Writer", isbn13: mlISBN13, minutes: 300})
	l.book("gamma", mlBook{title: "Gamma Ridge", author: "Cy Maker", pid: "ABCDEF0123456789"})
	l.book("delta", mlBook{title: "Delta Fields", author: "Dee Penn", minutes: 500})
	l.book("echo1", mlBook{title: "Echo Park", author: "Eve Quill", minutes: 100})
	l.book("echo2", mlBook{title: "Echo Park", author: "Eve Quill", minutes: 100})
	l.book("foxtrot", mlBook{title: "Foxtrot Run", author: "Fay Stone", asin: "B000000009", minutes: 600})
	l.book("golf", mlBook{title: "Wrongly Titled", author: "Golf Course", minutes: 200})
	l.book("hotel", mlBook{title: "Hotel Nights", author: "Hal Roe", asin: "B000000004", minutes: 400})
	l.book("india", mlBook{title: "India Saga 2", author: "Ira Vale", asin: "B000000005", minutes: 400})

	listID := l.upload(
		csvLine("Alpha Voyage", "Ann Author", "B000000001", "", "", "", "", "600", "finished", "2026-09-01T10:00:00Z"),           // line 2: matched_high (ASIN)
		csvLine("Beta Harbor", "Bob Writer", "", mlISBN13, "", "", "", "300", "finished", "2026-09-02T10:00:00Z"),                // 3: matched_high (ISBN)
		csvLine("Beta Harbor", "Bob Writer", "", "", mlISBN10, "", "", "", "in_progress"),                                        // 4: duplicate_in_list of 3 (ISBN-10 converts)
		csvLine("Gamma Ridge", "Cy Maker", "", "", "", "itunes", "abcdef0123456789", "", "unstarted"),                            // 5: matched_high (source id), runtime unknown
		csvLine("Delta Fields", "Dee Penn", "", "", "", "", "", "500", "finished", "2026-09-03T10:00:00Z"),                       // 6: matched_review (title+author)
		csvLine("Echo Park", "Eve Quill", "", "", "", "", "", "", "finished", "2026-09-04T10:00:00Z"),                            // 7: ambiguous
		csvLine("Foxtrot Run", "Fay Stone", "B000000009", "", "", "", "", "100", "finished", "2026-09-05T10:00:00Z"),             // 8: matched_review runtime_mismatch
		csvLine("Nobody Home", "Xan Yu", "", "", "", "", "", "", "finished"),                                                     // 9: unmatched
		csvLine("Delta Fields", "Zed Other", "", "", "", "", "", "", "finished"),                                                 // 10: unmatched author_mismatch
		csvLine("", "", "", "", "", "", "", "", "finished"),                                                                      // 11: invalid
		csvLine("Golf Course", "Gus Yarn", "", "", "", "", "", "200", "finished"),                                                // 12: matched_review swapped_title
		csvLine("Alpha Voyage", "Ann Author", "B000000001", "", "", "", "", "", "in_progress"),                                   // 13: duplicate_in_list of 2
		csvLine("Hotel Nights", "Hal Roe", "B000000004", "9780306406158", "", "", "", "400", "finished", "2026-09-06T10:00:00Z"), // 14: matched_review bad_identifier
		csvLine("India Saga 3", "Ira Vale", "B000000005", "", "", "", "", "400", "finished", "2026-09-07T10:00:00Z"),             // 15: matched_review series_mismatch
	)
	plan, _ := l.plan(listID)
	require.Len(t, plan.Rows, 14, "one plan row per input row")

	byLine := func(line int) repairs.Row {
		t.Helper()
		for _, r := range plan.Rows {
			st := rowState(t, r)
			if len(st.Lines) > 0 && st.Lines[0] == line {
				return r
			}
		}
		t.Fatalf("no row for line %d", line)
		return repairs.Row{}
	}

	// ASIN, runtime agrees.
	r := byLine(2)
	assert.Equal(t, mlMatchedHigh, r.Class)
	assert.True(t, r.Applicable(), "the only applicable class is matched_high")
	assert.Equal(t, "asin:B000000001", r.RowID)
	assert.Equal(t, []string{l.ids["alpha"]}, memberOf(r, "target"))
	assert.Equal(t, repairs.RiskLow, r.Risk)
	st := rowState(t, r)
	assert.Equal(t, []int{2, 13}, st.Lines, "the group's lines are kept, in order")
	assert.Equal(t, masterlist.StatusFinished, st.Row.Status, "finished beats in_progress in the combined row")
	assert.Equal(t, mlTierASIN, st.Tier)
	assert.Equal(t, "finished", r.Proposed["read_status"])
	assert.Equal(t, l.user, r.Proposed["user"])

	// ISBN-13 match; its ISBN-10 twin is a duplicate that names it.
	r = byLine(3)
	assert.Equal(t, mlMatchedHigh, r.Class)
	assert.Equal(t, "isbn13:"+mlISBN13, r.RowID)
	assert.Equal(t, mlTierISBN, rowState(t, r).Tier)
	dup := byLine(4)
	assert.Equal(t, mlDuplicate, dup.Class)
	assert.Equal(t, "isbn13:"+mlISBN13+"#2", dup.RowID)
	assert.False(t, dup.Applicable())
	assert.Equal(t, mlDuplicate, dup.Skipped)
	assert.Equal(t, "isbn13:"+mlISBN13, rowState(t, dup).Representative)
	assert.Empty(t, dup.BookIDs, "a duplicate row plans no book")

	// iTunes persistent id (source id, case-insensitive); runtime unknown on the list side.
	r = byLine(5)
	assert.Equal(t, mlMatchedHigh, r.Class)
	assert.Equal(t, mlTierSource, rowState(t, r).Tier)
	assert.Equal(t, repairs.RiskReview, r.Risk, "a match whose length could not be checked is planned at review risk")

	// Title and author: never high.
	r = byLine(6)
	assert.Equal(t, mlMatchedReview, r.Class)
	assert.False(t, r.Applicable())
	assert.Equal(t, mlTierTitle, rowState(t, r).Tier)
	assert.Equal(t, []string{l.ids["delta"]}, memberOf(r, "target"))

	// Two books fit.
	r = byLine(7)
	assert.Equal(t, mlAmbiguous, r.Class)
	assert.ElementsMatch(t, []string{l.ids["echo1"], l.ids["echo2"]}, r.BookIDs)
	assert.Empty(t, memberOf(r, "target"))

	// Identifier match, wrong length.
	r = byLine(8)
	assert.Equal(t, mlMatchedReview, r.Class)
	assert.Contains(t, r.Flags, mlFlagRuntime)

	assert.Equal(t, mlUnmatched, byLine(9).Class)

	r = byLine(10)
	assert.Equal(t, mlUnmatched, r.Class)
	assert.Contains(t, r.Flags, mlFlagAuthorMismatch)
	assert.Equal(t, []string{l.ids["delta"]}, r.BookIDs, "the title hit is listed as a candidate")

	r = byLine(11)
	assert.Equal(t, mlInvalid, r.Class)
	assert.Equal(t, "line:11", r.RowID)
	assert.False(t, r.Applicable())

	r = byLine(12)
	assert.Equal(t, mlMatchedReview, r.Class)
	assert.Contains(t, r.Flags, mlFlagSwapped)
	assert.Equal(t, mlTierSwapped, rowState(t, r).Tier)

	dup = byLine(13)
	assert.Equal(t, mlDuplicate, dup.Class)
	assert.Equal(t, "asin:B000000001#2", dup.RowID)

	r = byLine(14)
	assert.Equal(t, mlMatchedReview, r.Class, "a list-side finding downgrades an identifier match")
	assert.Contains(t, r.Flags, masterlist.IssueBadIdentifier)
	assert.Equal(t, []string{l.ids["hotel"]}, memberOf(r, "target"))

	r = byLine(15)
	assert.Equal(t, mlMatchedReview, r.Class)
	assert.Contains(t, r.Flags, mlFlagSeries)

	// Counts: every class count is a filter of the rows.
	assert.Equal(t, map[string]int{
		mlMatchedHigh: 3, mlMatchedReview: 5, mlAmbiguous: 1, mlUnmatched: 2, mlDuplicate: 2, mlInvalid: 1,
	}, plan.ByClass)
	assert.Equal(t, 3, plan.Applicable)
	assert.Equal(t, 14, plan.Total)
	assert.Equal(t, 1, plan.ByFlag[mlFlagRuntime])
}

func TestMasterListImport_TrashedBooksAreFollowedNeverTargets(t *testing.T) {
	l := newMLLab(t)
	l.book("survivor", mlBook{title: "Survivor Book", author: "Sue Vivor", minutes: 300})
	l.book("loser", mlBook{title: "Survivor Book", author: "Sue Vivor", asin: "B000000020", trashed: true, mergedInto: "survivor"})
	l.book("lost", mlBook{title: "Lost Book", author: "Lou Gone", asin: "B000000021", trashed: true})
	l.book("trashed-title", mlBook{title: "Only In Trash", author: "Tom Bin", trashed: true})

	listID := l.upload(
		csvLine("Survivor Book", "Sue Vivor", "B000000020", "", "", "", "", "300", "finished", "2026-09-01T10:00:00Z"),
		csvLine("Lost Book", "Lou Gone", "B000000021", "", "", "", "", "", "finished"),
		csvLine("Only In Trash", "Tom Bin", "", "", "", "", "", "", "finished"),
	)
	_, rows := l.plan(listID)

	via := rows["asin:B000000020"]
	assert.Equal(t, mlMatchedReview, via.Class, "an identifier found only on a trashed copy is never high")
	assert.Contains(t, via.Flags, mlFlagViaTrashed)
	assert.Equal(t, []string{l.ids["survivor"]}, memberOf(via, "target"), "the live survivor, not the loser")
	assert.Equal(t, []string{l.ids["loser"]}, memberOf(via, "trashed"))
	assert.NotContains(t, via.BookIDs, l.ids["loser"], "a trashed book is evidence, never a book the row may write")

	lost := rows["asin:B000000021"]
	assert.Equal(t, mlUnmatched, lost.Class)
	assert.Contains(t, lost.Flags, mlFlagTrashedOnly)
	assert.Empty(t, memberOf(lost, "target"))
	assert.Empty(t, lost.BookIDs)
	assert.Equal(t, []string{l.ids["lost"]}, memberOf(lost, "trashed"))

	// A title that only a trashed book carries matches nothing.
	title := ""
	for id, r := range rows {
		if strings.HasPrefix(id, "noid:") {
			title = id
			assert.Equal(t, mlUnmatched, r.Class)
			assert.Empty(t, r.BookIDs)
		}
	}
	assert.NotEmpty(t, title)
}

func TestMasterListImport_TwoEntriesOneBookAreBothHeldForReview(t *testing.T) {
	l := newMLLab(t)
	l.book("one", mlBook{title: "Kilo Part One", author: "Kim Lee", asin: "B000000030", minutes: 300})
	listID := l.upload(
		// Row 2 matches by ASIN (the wrong volume's identifier on that book);
		// row 3 matches the same book by title and author.
		csvLine("Kilo Part One", "Kim Lee", "B000000030", "", "", "", "", "300", "finished", "2026-09-01T10:00:00Z"),
		csvLine("Kilo Part One", "Kim Lee", "", "", "", "", "", "300", "in_progress"),
	)
	plan, rows := l.plan(listID)
	assert.Zero(t, plan.Applicable, "two entries claiming one book: neither is applicable")
	for _, r := range rows {
		assert.Contains(t, r.Flags, mlFlagDuplicateTgt)
		assert.Contains(t, r.Flags, mlFlagPossibleDup, "same title and author, no shared identifier")
		assert.Equal(t, mlMatchedReview, r.Class)
	}
}

func TestMasterListImport_FutureTimestampIsReviewed(t *testing.T) {
	l := newMLLab(t)
	l.book("lima", mlBook{title: "Lima Road", author: "Lin May", asin: "B000000040", minutes: 300})
	l.book("mike", mlBook{title: "Mike Lane", author: "Moe Ray", asin: "B000000041", minutes: 300})
	listID := l.upload(
		csvLine("Lima Road", "Lin May", "B000000040", "", "", "", "", "300", "finished", "2027-01-01T00:00:00Z"),
		csvLine("Mike Lane", "Moe Ray", "B000000041", "", "", "", "", "300", "finished", "2026-10-23T12:05:00Z"), // within the slack
	)
	_, rows := l.plan(listID)
	assert.Equal(t, mlMatchedReview, rows["asin:B000000040"].Class)
	assert.Contains(t, rows["asin:B000000040"].Flags, masterlist.IssueFutureTimestamp)
	assert.Equal(t, mlMatchedHigh, rows["asin:B000000041"].Class)
}

func TestMasterListImport_PlanWritesNothing(t *testing.T) {
	l := newMLLab(t)
	l.book("alpha", mlBook{title: "Alpha Voyage", author: "Ann Author", asin: "B000000001", minutes: 600})
	listID := l.upload(csvLine("Alpha Voyage", "Ann Author", "B000000001", "", "", "", "", "600", "finished", "2026-09-01T10:00:00Z"))

	dump := func() map[string]string {
		kvs, err := l.store.ScanPrefix("")
		require.NoError(t, err)
		out := map[string]string{}
		for _, kv := range kvs {
			out[kv.Key] = string(kv.Value)
		}
		return out
	}
	before := dump()
	plan, _ := l.plan(listID)
	require.Equal(t, 1, plan.Applicable)
	assert.Equal(t, before, dump(), "planning a list changes no key: not the list, not a book, not user state")
}

func TestMasterListImport_PlanIsDeterministicAndFingerprintsTrackInputs(t *testing.T) {
	l := newMLLab(t)
	l.book("alpha", mlBook{title: "Alpha Voyage", author: "Ann Author", asin: "B000000001", minutes: 600})
	listID := l.upload(csvLine("Alpha Voyage", "Ann Author", "B000000001", "", "", "", "", "600", "finished", "2026-09-01T10:00:00Z"))
	_, a := l.plan(listID)
	_, b := l.plan(listID)
	require.NotEmpty(t, a["asin:B000000001"].Fingerprint)
	assert.Equal(t, a["asin:B000000001"].Fingerprint, b["asin:B000000001"].Fingerprint)

	// A different list row (another status) is a different fingerprint.
	other := l.upload(csvLine("Alpha Voyage", "Ann Author", "B000000001", "", "", "", "", "600", "in_progress"))
	_, c := l.plan(other)
	assert.NotEqual(t, a["asin:B000000001"].Fingerprint, c["asin:B000000001"].Fingerprint)
}

func TestMasterListImport_ApplyAndReplanRefuseAndLaneHidesApply(t *testing.T) {
	l := newMLLab(t)
	assert.True(t, repairs.IsPlanOnly(l.fixer))
	row := repairs.Row{RowID: "asin:B000000001"}
	err := l.fixer.Apply(context.Background(), nil, row)
	require.ErrorIs(t, err, errMasterListApplyNotBuilt)
	assert.Contains(t, err.Error(), "PR 2")
	_, err = l.fixer.Replan(context.Background(), nil, row, &fakeReporter{})
	require.ErrorIs(t, err, errMasterListApplyNotBuilt)
}

func TestMasterListImport_ParamsAreChecked(t *testing.T) {
	l := newMLLab(t)
	listID := l.upload(csvLine("Alpha Voyage", "Ann Author", "B000000001"))
	plan := func(params string) error {
		series, err := l.store.GetAllSeries()
		require.NoError(t, err)
		_, err = repairs.RunPlan(context.Background(), l.fixer, json.RawMessage(params), l.p.repairsPlanDeps(l.store, series), &fakeReporter{})
		return err
	}
	assert.ErrorContains(t, plan(`null`), "params are required")
	assert.ErrorContains(t, plan(fmt.Sprintf(`{"target_user_id":%q}`, l.user)), "list_id is required")
	assert.ErrorContains(t, plan(fmt.Sprintf(`{"list_id":%q}`, listID)), "target_user_id is required")
	assert.ErrorContains(t, plan(fmt.Sprintf(`{"list_id":%q,"target_user_id":"_local"}`, listID)), "iTunes sync user")
	assert.ErrorContains(t, plan(fmt.Sprintf(`{"list_id":%q,"target_user_id":"nobody"}`, listID)), "is not a user")
	assert.ErrorContains(t, plan(fmt.Sprintf(`{"list_id":"missing","target_user_id":%q}`, l.user)), "does not exist")
	require.NoError(t, plan(fmt.Sprintf(`{"list_id":%q,"target_user_id":%q}`, listID, l.user)))
}

func TestMasterListImport_ManyRowsPlanConcurrently(t *testing.T) {
	l := newMLLab(t)
	var csvRows []string
	const n = 100
	for i := 0; i < n; i++ {
		asin := fmt.Sprintf("B0%08d", i)
		l.book(fmt.Sprintf("b%d", i), mlBook{title: fmt.Sprintf("Synthetic Title %d", i), author: "Pat Many", asin: asin, minutes: 300})
		csvRows = append(csvRows, csvLine(fmt.Sprintf("Synthetic Title %d", i), "Pat Many", asin, "", "", "", "", "300", "finished", "2026-09-01T10:00:00Z"))
	}
	plan, _ := l.plan(l.upload(csvRows...))
	assert.Equal(t, n, plan.Total)
	assert.Equal(t, n, plan.Applicable)
	assert.Equal(t, n, plan.ByClass[mlMatchedHigh])
}

func TestMasterListImport_DisagreeingIdentifiersAreHeldForReview(t *testing.T) {
	l := newMLLab(t)
	l.book("a", mlBook{title: "November Rain", author: "Nia Fox", asin: "B000000050", minutes: 300})
	l.book("b", mlBook{title: "Oscar Hill", author: "Oli Dane", isbn13: mlISBN13b, minutes: 300})
	l.book("c", mlBook{title: "Papa Sound", author: "Pia Cole", asin: "B000000051", isbn13: mlISBN13, minutes: 300})
	listID := l.upload(
		// The ASIN is book a's, the ISBN is book b's.
		csvLine("November Rain", "Nia Fox", "B000000050", mlISBN13b, "", "", "", "300", "finished", "2026-09-01T10:00:00Z"),
		// ASIN and ISBN both name book c: nothing disagrees.
		csvLine("Papa Sound", "Pia Cole", "B000000051", mlISBN13, "", "", "", "300", "finished", "2026-09-01T10:00:00Z"),
	)
	_, rows := l.plan(listID)
	r := rows["asin:B000000050"]
	assert.Equal(t, mlMatchedReview, r.Class)
	assert.Contains(t, r.Flags, mlFlagIDConflict)
	assert.Equal(t, []string{l.ids["a"]}, memberOf(r, "target"), "the first tier's book stays the target; a person decides")
	assert.Equal(t, mlMatchedHigh, rows["asin:B000000051"].Class)
	assert.NotContains(t, rows["asin:B000000051"].Flags, mlFlagIDConflict)
}
