// file: internal/plugins/maintenance/relink_pre_fix_imports_classes_test.go
// version: 1.0.0
// guid: 5b8f3a1e-6c47-4d92-9e0b-2f7a41c8d6e3
// last-edited: 2026-10-11

package maintenance

// The relink trial lists a link row (would_link, same_book_version) under the
// first evidence check it fails (pfrRowClass), so the Repairs lane's class
// chips count and open the rows of each check. These tests pin the class of
// each disagreement, the primary order for a row failing several, the
// click-through (PlanResult.Page by class lists exactly the counted rows) and
// that a row whose class moves after planning is refused at apply.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// pfrSeedFailing seeds one pre-fix import, role, whose row fails exactly the
// named evidence checks, and returns its row id. With sameBook the row is the
// second file of a torrent whose first file is already linked (so it classes
// as same_book_version, not would_link). n makes the torrent hash distinct.
func pfrSeedFailing(t *testing.T, f *fragFixture, role string, n int, sameBook bool, fails ...string) string {
	t.Helper()
	failing := map[string]bool{}
	for _, c := range fails {
		failing[c] = true
	}
	rel, format, hash := "imports/"+role+".m4b", "m4b", ""
	if failing[pfrCheckLocation] {
		rel = "library/" + role + ".m4b"
	}
	if failing[pfrCheckFormat] {
		format = "mp3"
	}
	if sameBook {
		hash = fmt.Sprintf("%040x", n+1)
	}
	book := f.book(t, role, "Placeholder "+role, f.path(rel), nil)
	ver := &database.BookVersion{BookID: book, Status: database.BookVersionStatusActive, Source: "imported", TorrentHash: hash}
	if failing[pfrCheckDates] {
		ver.IngestDate = time.Now().Add(-30 * 24 * time.Hour)
	}
	v, err := f.s.CreateBookVersion(ver)
	require.NoError(t, err)
	if sameBook {
		f.importRow(t, role+"-sibling", book, hash, v.ID)
	}
	bf := &database.BookFile{BookID: book, FilePath: f.file(t, rel, 333), Format: format, FileSize: 333,
		DelugeHash: hash, TrackNumber: 2}
	require.NoError(t, f.s.CreateBookFile(bf))
	f.rowIDs[role] = bf.ID
	if failing[pfrCheckMissing] {
		_, err := f.s.ModifyBookFile(book, bf.ID, func(r *database.BookFile) error { r.Missing = true; return nil })
		require.NoError(t, err)
	}
	return bf.ID
}

// Each disagreement alone lists the row under its own review class; a row
// that agrees keeps the plain class.
func TestRelinkPreFixImports_ClassPerDisagreement(t *testing.T) {
	t.Parallel()
	for _, base := range []string{pfrClassLink, pfrClassSameBook} {
		t.Run(base, func(t *testing.T) {
			t.Parallel()
			f := newPreFixFixture(t)
			sameBook := base == pfrClassSameBook
			want := map[string]string{"agree": base}
			pfrSeedFailing(t, f, "agree", 0, sameBook)
			for i, c := range []string{pfrCheckDates, pfrCheckFormat, pfrCheckLocation, pfrCheckMissing} {
				pfrSeedFailing(t, f, c, i+1, sameBook, c)
				want[c] = base + "_review_" + c
			}
			res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
			for role, class := range want {
				r := findRow(t, res, f.rowIDs[role])
				require.Equal(t, class, r.Class, "%s: %s", role, r.Reason)
				require.True(t, r.Applicable(), role)
				wantRisk := repairs.RiskReview
				if class == pfrClassLink {
					wantRisk = repairs.RiskLow
				}
				require.Equal(t, wantRisk, r.Risk, role)
			}
		})
	}
}

// A row failing several checks is listed under the first in the priority
// order missing, location, format, dates, and its evidence names the rest.
func TestRelinkPreFixImports_MultiFailurePrimaryOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fails   []string
		primary string
		others  string // "" when the row fails one check
	}{
		{[]string{pfrCheckDates, pfrCheckFormat}, pfrCheckFormat, pfrCheckDates},
		{[]string{pfrCheckDates, pfrCheckLocation}, pfrCheckLocation, pfrCheckDates},
		{[]string{pfrCheckFormat, pfrCheckLocation}, pfrCheckLocation, pfrCheckFormat},
		{[]string{pfrCheckDates, pfrCheckMissing}, pfrCheckMissing, pfrCheckDates},
		{[]string{pfrCheckLocation, pfrCheckMissing}, pfrCheckMissing, pfrCheckLocation},
		{[]string{pfrCheckFormat, pfrCheckMissing}, pfrCheckMissing, pfrCheckFormat},
		{[]string{pfrCheckDates, pfrCheckFormat, pfrCheckLocation, pfrCheckMissing}, pfrCheckMissing,
			pfrCheckLocation + ", " + pfrCheckFormat + ", " + pfrCheckDates},
	}
	for _, base := range []string{pfrClassLink, pfrClassSameBook} {
		for i, tc := range cases {
			t.Run(fmt.Sprintf("%s/%s", base, strings.Join(tc.fails, "+")), func(t *testing.T) {
				t.Parallel()
				f := newPreFixFixture(t)
				id := pfrSeedFailing(t, f, "multi", i, base == pfrClassSameBook, tc.fails...)
				res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
				r := findRow(t, res, id)
				require.Equal(t, base+"_review_"+tc.primary, r.Class, "%v", r.Evidence)
				require.True(t, r.Applicable())
				require.Equal(t, repairs.RiskReview, r.Risk)
				ev := strings.Join(r.Evidence, "\n")
				require.Contains(t, ev, "listed under "+r.Class)
				require.Equal(t, len(tc.fails), strings.Count(ev, "REVIEW ("), "every failing check keeps its evidence line: %s", ev)
				require.Contains(t, ev, "also fails: "+tc.others)
			})
		}
	}
	t.Run("a single failure names no other check", func(t *testing.T) {
		t.Parallel()
		f := newPreFixFixture(t)
		id := pfrSeedFailing(t, f, "single", 0, false, pfrCheckDates)
		res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
		ev := strings.Join(findRow(t, res, id).Evidence, "\n")
		require.Contains(t, ev, "listed under would_link_review_dates")
		require.NotContains(t, ev, "also fails")
	})
}

// Every chip count opens exactly the rows it counts: PlanResult.ByClass is the
// number of rows PlanResult.Page lists for that class, the link classes sum to
// the link rows, and the per-check totals the op log reports are unchanged
// (they overlap where a row fails several checks).
func TestRelinkPreFixImports_ClassCountsClickThrough(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	pfrSeedFailing(t, f, "agree", 0, false)
	pfrSeedFailing(t, f, "d1", 1, false, pfrCheckDates)
	pfrSeedFailing(t, f, "d2", 2, false, pfrCheckDates)
	pfrSeedFailing(t, f, "dm", 3, false, pfrCheckDates, pfrCheckMissing)
	pfrSeedFailing(t, f, "fl", 4, false, pfrCheckFormat, pfrCheckLocation)
	pfrSeedFailing(t, f, "sb", 5, true)
	pfrSeedFailing(t, f, "sbd", 6, true, pfrCheckDates)

	rep := &fakeReporter{}
	rows, err := newRelinkPreFixFixer(f.p).Plan(t.Context(), nil, rep)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	rep.mu.Lock()
	logs := strings.Join(rep.logs, "\n")
	rep.mu.Unlock()
	// 5 would_link + 2 same_book_version; dates fails on d1, d2, dm and sbd.
	require.Contains(t, logs, "evidence over 7 link rows (5 would_link, 2 same_book_version): low risk 1, review 6;")
	require.Contains(t, logs, "dates 4, format 1, location 1, missing 1")

	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	wantCounts := map[string]int{
		pfrClassLink:                       1,
		pfrClassLink + "_review_dates":     2,
		pfrClassLink + "_review_missing":   1,
		pfrClassLink + "_review_location":  1,
		pfrClassSameBook:                   1,
		pfrClassSameBook + "_review_dates": 1,
	}
	for class, n := range wantCounts {
		require.Equal(t, n, res.ByClass[class], class)
		page, err := res.Page("op-plan", repairs.FilterAll, class, 0, 100)
		require.NoError(t, err)
		require.Equal(t, n, page.Total, class)
		require.Len(t, page.Rows, n, class)
		for _, r := range page.Rows {
			require.Equal(t, class, r.Class)
		}
	}
	linkRows := 0
	for class, n := range res.ByClass {
		if strings.HasPrefix(class, pfrClassLink) || strings.HasPrefix(class, pfrClassSameBook) {
			linkRows += n
		}
	}
	require.Equal(t, 7, linkRows)
	require.Len(t, wantCounts, 6)
	require.Equal(t, 7, res.Applicable)
}

// The class is a function of the fingerprinted checks: a row that moves from
// one review class to another between the plan and the apply (still Review
// risk, so the risk compare alone would not catch it) is refused, and so is a
// low-risk row that gains a disagreement.
func TestRelinkPreFixImports_ClassChangeSincePlanIsRefused(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	idDates := pfrSeedFailing(t, f, "moves", 0, false, pfrCheckDates)
	idAgree := pfrSeedFailing(t, f, "gains", 1, false)
	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	require.Equal(t, pfrClassLink+"_review_dates", findRow(t, res, idDates).Class)
	require.Equal(t, pfrClassLink, findRow(t, res, idAgree).Class)

	for role, id := range map[string]string{"moves": idDates, "gains": idAgree} {
		_, err := f.s.ModifyBookFile(f.ids[role], id, func(r *database.BookFile) error { r.Missing = true; return nil })
		require.NoError(t, err)
	}
	out := f.fixerApply(t, relinkPreFixFixerID, "op-plan", "op-apply", applicableIDs(res))
	require.Zero(t, out.Applied)
	require.Equal(t, 2, out.ChangedSincePlan, "%+v", out.Rows)
	require.Empty(t, f.bookFile(t, "moves").VersionID)
	require.Empty(t, f.bookFile(t, "gains").VersionID)

	// Re-planned, both are listed under the new class.
	again := f.fixerPlan(t, relinkPreFixFixerID, "op-plan2", nil)
	require.Equal(t, pfrClassLink+"_review_missing", findRow(t, again, idDates).Class)
	require.Equal(t, pfrClassLink+"_review_missing", findRow(t, again, idAgree).Class)
}

// Applying a review class links its rows as it does any link row.
func TestRelinkPreFixImports_ReviewClassRowsApply(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	id := pfrSeedFailing(t, f, "rv", 0, false, pfrCheckDates)
	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	require.Equal(t, pfrClassLink+"_review_dates", findRow(t, res, id).Class)
	out := f.fixerApply(t, relinkPreFixFixerID, "op-plan", "op-apply", []string{id})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.NotEmpty(t, f.bookFile(t, "rv").VersionID)
}
