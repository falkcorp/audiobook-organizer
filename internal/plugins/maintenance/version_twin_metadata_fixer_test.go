// file: internal/plugins/maintenance/version_twin_metadata_fixer_test.go
// version: 1.0.0
// guid: 7a1e3c95-4d28-4b6f-a0c7-93e2d5b8f146
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const vtTestOpID = "op-version-twin-apply"

type vtLib struct {
	t     *testing.T
	st    *database.PebbleStore
	p     *Plugin
	fixer *versionTwinFixer
	ids   map[string]string
	// author is the shared author every fixture book credits.
	author *database.Author
}

func newVTLib(t *testing.T) *vtLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	p := &Plugin{deps: fakeDeps{store: st}, standDownWait: noWait}
	a, err := st.CreateAuthor("Frank Herbert")
	require.NoError(t, err)
	return &vtLib{t: t, st: st, p: p, fixer: newVersionTwinFixer(p), ids: map[string]string{}, author: a}
}

func vtPtr[T any](v T) *T { return &v }

// vtDune is the record the twins are applied from.
func vtDune() metafetch.MetadataCandidate { return vtDuneASIN("B002V1OF70") }

// vtDuneASIN is vtDune under another ASIN: each group's twin gets its own
// record, so groups never share one (which is itself a hold).
func vtDuneASIN(asin string) metafetch.MetadataCandidate {
	return metafetch.MetadataCandidate{Title: "Dune", Author: "Frank Herbert", Narrator: "Scott Brick",
		Publisher: "Macmillan Audio", ASIN: asin, Source: "audible", Score: 0.97}
}

// vtDWASIN is a Doctor Who record (owner-manual-only).
func vtDWASIN(asin string) metafetch.MetadataCandidate {
	c := vtDuneASIN(asin)
	c.Title = "Doctor Who: Spearhead"
	return c
}

// book creates a group member. primary decides IsPrimaryVersion; every book
// is organized and credits the shared author unless edit says otherwise.
func (l *vtLib) book(key, group string, primary bool, edit func(*database.Book)) string {
	l.t.Helper()
	path := "/lib/Frank Herbert/" + key
	b := &database.Book{Title: "Dune", Format: "m4b", FilePath: path, AuthorID: &l.author.ID,
		VersionGroupID: vtPtr(group), IsPrimaryVersion: vtPtr(primary), LibraryState: vtPtr("organized"),
		Duration: vtPtr(75600)}
	if edit != nil {
		edit(b)
	}
	created, err := l.st.CreateBook(b)
	require.NoError(l.t, err)
	require.NoError(l.t, l.st.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: b.FilePath + "/01.m4b", Format: "m4b"}))
	l.ids[key] = created.ID
	return created.ID
}

// appliedTwin creates an applied twin whose cache holds the record it was
// applied from (and a decoy candidate ranked above it).
func (l *vtLib) appliedTwin(key, group string, cand metafetch.MetadataCandidate, edit func(*database.Book)) string {
	l.t.Helper()
	hash := metafetch.CandidateSourceHash(cand)
	id := l.book(key, group, false, func(b *database.Book) {
		b.MetadataReviewStatus = vtPtr("matched")
		b.MetadataSource = vtPtr(cand.Source)
		b.MetadataSourceHash = vtPtr(hash)
		b.ASIN = vtPtr(cand.ASIN)
		b.Narrator = vtPtr(cand.Narrator)
		if edit != nil {
			edit(b)
		}
	})
	decoy := metafetch.MetadataCandidate{Title: "Dune Messiah", Author: "Frank Herbert", ASIN: "B00DECOY01", Source: "audible", Score: 0.99}
	l.putCache(id, []metafetch.MetadataCandidate{decoy, cand}, "")
	return id
}

func (l *vtLib) putCache(id string, cands []metafetch.MetadataCandidate, sourceHash string) {
	l.t.Helper()
	raw := make([]json.RawMessage, 0, len(cands))
	for _, c := range cands {
		b, err := json.Marshal(c)
		require.NoError(l.t, err)
		raw = append(raw, b)
	}
	require.NoError(l.t, l.st.PutMetadataCache(&database.MetadataCandidateCache{BookID: id, FetchedAt: time.Now().UTC(),
		Candidates: raw, SourceHash: sourceHash}))
}

func (l *vtLib) get(key string) *database.Book {
	l.t.Helper()
	b, err := l.st.GetBookByID(l.ids[key])
	require.NoError(l.t, err)
	require.NotNil(l.t, b)
	return b
}

func (l *vtLib) plan() (*repairs.PlanResult, map[string]repairs.Row) {
	l.t.Helper()
	series, err := l.st.GetAllSeries()
	require.NoError(l.t, err)
	res, err := repairs.RunPlan(context.Background(), l.fixer, nil, l.p.repairsPlanDeps(l.st, series), &fakeReporter{})
	require.NoError(l.t, err)
	raw, err := json.Marshal(res)
	require.NoError(l.t, err)
	var stored repairs.PlanResult
	require.NoError(l.t, json.Unmarshal(raw, &stored))
	byGroup := map[string]repairs.Row{}
	for _, r := range stored.Rows {
		byGroup[r.RowID] = r
	}
	return &stored, byGroup
}

func (l *vtLib) apply(plan *repairs.PlanResult, dry bool, groups ...string) *repairs.ApplyResult {
	l.t.Helper()
	series, err := l.st.GetAllSeries()
	require.NoError(l.t, err)
	deps := repairs.ApplyDeps{Guard: l.st, Tags: l.p.repairsGuardTags(), Series: repairs.SeriesNamesFrom(series), OpID: vtTestOpID}
	if !dry {
		deps.Writer = repairs.NewWriter(l.st, l.st, l.fixer.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, vtTestOpID)
	}
	res, err := repairs.RunApply(context.Background(), l.fixer, plan, "op-version-twin-plan", groups, dry, deps, &fakeReporter{})
	require.NoError(l.t, err)
	return res
}

// primaryFlags snapshots every book's primary flag and merge target, which
// the fixer must never change.
func (l *vtLib) primaryFlags() map[string]string {
	l.t.Helper()
	out := map[string]string{}
	for k := range l.ids {
		b := l.get(k)
		out[k] = vtBoolStr(b.IsPrimaryVersion) + "|" + dcStr(b.MergedIntoBookID) + "|" + dcStr(b.VersionGroupID)
	}
	return out
}

func ptrCore(b *database.Book) *database.BookCore {
	c := b.Core()
	return &c
}

// An applied twin's record is applied to the primary through the normal
// apply (journaled history, match stamped, fill-only) and "undo last apply"
// reverts it. A dry run writes nothing; the primary flags never move.
func TestVersionTwinFixer_AppliedTwinCopyAndUndo(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g-applied", true, nil)
	l.appliedTwin("t", "g-applied", vtDune(), nil)
	flags := l.primaryFlags()

	plan, rows := l.plan()
	row, ok := rows["g-applied"]
	require.True(t, ok, "rows %v", rows)
	require.True(t, row.Applicable(), "skipped %s: %s", row.Skipped, row.SkipReason)
	require.Equal(t, vtClassApplied, row.Class)
	require.Equal(t, l.ids["p"], row.Current["primary_id"])
	require.Equal(t, l.ids["t"], row.Current["twin_id"])
	require.Equal(t, "matched", row.Current["twin_review_status"])
	require.Equal(t, "B002V1OF70", row.Current["twin_asin"])
	require.Equal(t, "75600", row.Current["primary_duration"])
	require.Contains(t, row.Reason, "B002V1OF70")

	dry := l.apply(plan, true, "g-applied")
	require.Equal(t, 1, dry.ByOutcome[repairs.OutcomeWouldApply])
	require.Nil(t, l.get("p").MetadataReviewStatus, "a dry run writes nothing")

	res := l.apply(plan, false, "g-applied")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	p := l.get("p")
	require.True(t, database.MetadataApplied(p.MetadataReviewStatus), "status %v", dcStr(p.MetadataReviewStatus))
	require.Equal(t, "Scott Brick", dcStr(p.Narrator), "the record's empty fields are filled")
	require.Equal(t, "Dune", p.Title)
	require.Equal(t, metafetch.CandidateSourceHash(vtDune()), dcStr(p.MetadataSourceHash))
	require.Equal(t, flags, l.primaryFlags(), "no book's primary flag, merge target or group changed")
	require.Equal(t, "matched", dcStr(l.get("t").MetadataReviewStatus), "the twin is not written")

	hist, err := l.st.GetBookChangeHistory(l.ids["p"], 50)
	require.NoError(t, err)
	require.NotEmpty(t, hist, "the apply journals metadata history")

	_, err = metafetch.NewService(l.st).UndoLastApply(l.ids["p"])
	require.NoError(t, err)
	p = l.get("p")
	require.Empty(t, dcStr(p.Narrator), "undo reverts the filled narrator")
	require.False(t, database.MetadataApplied(p.MetadataReviewStatus), "undo reverts the match stamp")
	require.Equal(t, flags, l.primaryFlags())
}

// A twin holding candidates (none applied) has them copied onto the primary,
// re-keyed so the primary's identity check accepts them; nothing is applied
// and the copy is journaled.
func TestVersionTwinFixer_CandidatesTwinCopy(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g-cands", true, nil)
	tid := l.book("t", "g-cands", false, nil)
	l.putCache(tid, []metafetch.MetadataCandidate{vtDune()}, metafetch.BatchSourceHash(tid, "Dune", "Frank Herbert"))
	flags := l.primaryFlags()

	plan, rows := l.plan()
	row := rows["g-cands"]
	require.True(t, row.Applicable(), "skipped %s: %s", row.Skipped, row.SkipReason)
	require.Equal(t, vtClassCandidates, row.Class)

	res := l.apply(plan, false, "g-cands")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	entry, err := l.st.GetMetadataCache(l.ids["p"])
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Len(t, entry.Candidates, 1)
	p := l.get("p")
	require.NoError(t, metafetch.NewService(l.st).ValidateCachedIdentityForBook(entry, p, []string{"Frank Herbert"}),
		"the copy passes the primary's own identity check")
	require.Nil(t, p.MetadataReviewStatus, "nothing is applied")
	require.Equal(t, flags, l.primaryFlags())
	changes, err := l.st.GetOperationChanges(vtTestOpID)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, vtChangeTypeCacheCopy, changes[0].ChangeType)

	// A re-plan: the primary holds candidates now, so the group is no row.
	_, rows = l.plan()
	_, still := rows["g-cands"]
	require.False(t, still)
}

// Every hold is a skipped row with its own kind, and apply never writes it.
func TestVersionTwinFixer_Holds(t *testing.T) {
	l := newVTLib(t)
	cases := map[string]string{}
	hold := func(group, kind string) { cases[group] = kind }

	// Two applied twins carrying different records.
	l.book("dis-p", "g-disagree", true, nil)
	l.appliedTwin("dis-t1", "g-disagree", vtDune(), nil)
	other := vtDune()
	other.ASIN = "B00OTHER01"
	l.appliedTwin("dis-t2", "g-disagree", other, nil)
	hold("g-disagree", vtHoldTwinsDisagree)

	// A locked field on the primary.
	l.book("lock-p", "g-locked", true, nil)
	l.appliedTwin("lock-t", "g-locked", vtDuneASIN("B0TWIN0001"), nil)
	require.NoError(t, l.st.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: l.ids["lock-p"],
		Field: database.FieldKeyTitle, OverrideValue: vtPtr(`"Dune"`), OverrideLocked: true, UpdatedAt: time.Now()}))
	hold("g-locked", vtHoldLocked)

	// Different editions: runtime, abridgement, narrator.
	l.book("dur-p", "g-duration", true, func(b *database.Book) { b.Duration = vtPtr(60000) })
	l.appliedTwin("dur-t", "g-duration", vtDuneASIN("B0TWIN0002"), nil)
	hold("g-duration", vtHoldEdition)
	l.book("abr-p", "g-abridged", true, func(b *database.Book) { b.Abridged = vtPtr(true) })
	l.appliedTwin("abr-t", "g-abridged", vtDuneASIN("B0TWIN0003"), func(b *database.Book) { b.Abridged = vtPtr(false) })
	hold("g-abridged", vtHoldEdition)
	l.book("nar-p", "g-narrator", true, func(b *database.Book) { b.Narrator = vtPtr("Simon Vance") })
	l.appliedTwin("nar-t", "g-narrator", vtDuneASIN("B0TWIN0004"), nil)
	hold("g-narrator", vtHoldEdition)

	// Doctor Who: the framework's owner-manual guard.
	l.book("dw-p", "g-dw", true, func(b *database.Book) { b.Title = "Doctor Who: Spearhead" })
	l.appliedTwin("dw-t", "g-dw", vtDWASIN("B0TWIN0005"), func(b *database.Book) { b.Title = "Doctor Who: Spearhead" })
	hold("g-dw", repairs.SkipOwnerManual)

	// An iTunes-linked primary.
	l.book("it-p", "g-itunes", true, func(b *database.Book) { b.ITunesPersistentID = vtPtr("ABCDEF0123456789") })
	l.appliedTwin("it-t", "g-itunes", vtDuneASIN("B0TWIN0006"), nil)
	hold("g-itunes", vtHoldITunes)

	// A primary ABS does not list.
	l.book("abs-p", "g-abs", true, func(b *database.Book) { b.LibraryState = vtPtr("imported") })
	l.appliedTwin("abs-t", "g-abs", vtDuneASIN("B0TWIN0007"), nil)
	hold("g-abs", vtHoldNotABSListed)

	// A different title.
	l.book("id-p", "g-identity", true, func(b *database.Book) { b.Title = "Children of Dune" })
	l.appliedTwin("id-t", "g-identity", vtDuneASIN("B0TWIN0008"), nil)
	hold("g-identity", vtHoldIdentity)

	// An applied twin whose record is no longer in its cache.
	l.book("lost-p", "g-lost", true, nil)
	lt := l.appliedTwin("lost-t", "g-lost", vtDuneASIN("B0TWIN0009"), nil)
	require.NoError(t, l.st.DeleteMetadataCache(lt))
	hold("g-lost", vtHoldUnrecoverable)

	// The primary carries another ASIN.
	l.book("asin-p", "g-asin", true, func(b *database.Book) { b.ASIN = vtPtr("B00ELSEWH1") })
	l.appliedTwin("asin-t", "g-asin", vtDuneASIN("B0TWIN0010"), nil)
	hold("g-asin", vtHoldASINConflict)

	// The record is also on a book in another group.
	l.book("hash-p", "g-hash", true, nil)
	l.appliedTwin("hash-t", "g-hash", vtDune(), nil)
	l.book("hash-out", "g-elsewhere", true, func(b *database.Book) {
		b.MetadataReviewStatus = vtPtr("matched")
		b.MetadataSourceHash = vtPtr(metafetch.CandidateSourceHash(vtDune()))
	})
	hold("g-hash", vtHoldHashShared)

	// Two primaries in one group.
	l.book("amb-p1", "g-ambiguous", true, nil)
	l.book("amb-p2", "g-ambiguous", true, nil)
	l.appliedTwin("amb-t", "g-ambiguous", vtDuneASIN("B0TWIN0011"), nil)
	hold("g-ambiguous", vtHoldPrimaryAmbiguous)

	// Candidates twin with a different author is held too.
	other2, err := l.st.CreateAuthor("Brian Herbert")
	require.NoError(t, err)
	l.book("ca-p", "g-cand-author", true, func(b *database.Book) { b.AuthorID = &other2.ID })
	cat := l.book("ca-t", "g-cand-author", false, nil)
	l.putCache(cat, []metafetch.MetadataCandidate{vtDune()}, metafetch.BatchSourceHash(cat, "Dune", "Frank Herbert"))
	hold("g-cand-author", vtHoldIdentity)

	flags := l.primaryFlags()
	plan, rows := l.plan()
	var groups []string
	for g, kind := range cases {
		row, ok := rows[g]
		require.True(t, ok, "group %s is a row", g)
		require.Equal(t, kind, row.Skipped, "group %s: %s", g, row.SkipReason)
		require.NotEmpty(t, row.SkipReason, g)
		groups = append(groups, g)
	}
	res := l.apply(plan, false, groups...)
	require.Zero(t, res.Applied, "outcomes %v", res.ByOutcome)
	for k := range l.ids {
		b := l.get(k)
		if k == "hash-out" || !vtIsPrimary(ptrCore(b)) {
			continue
		}
		require.Nil(t, b.MetadataReviewStatus, "held primary %s was applied", k)
		require.NotEqual(t, "Scott Brick", dcStr(b.Narrator), "held primary %s was filled", k)
		entry, err := l.st.GetMetadataCache(b.ID)
		require.NoError(t, err)
		require.Nil(t, entry, "held primary %s got candidates", k)
	}
	require.Equal(t, flags, l.primaryFlags())
}

// A row whose primary changed after the plan is refused changed_since_plan:
// demoted (re-elected elsewhere), applied by someone else, or retitled.
func TestVersionTwinFixer_ChangedSincePlan(t *testing.T) {
	l := newVTLib(t)
	l.book("demote-p", "g-demote", true, nil)
	l.appliedTwin("demote-t", "g-demote", vtDuneASIN("B0TWIN0012"), nil)
	l.book("applied-p", "g-applied-later", true, nil)
	l.appliedTwin("applied-t", "g-applied-later", vtDuneASIN("B0TWIN0013"), nil)
	l.book("retitle-p", "g-retitle", true, nil)
	l.appliedTwin("retitle-t", "g-retitle", vtDuneASIN("B0TWIN0014"), nil)

	plan, rows := l.plan()
	for _, g := range []string{"g-demote", "g-applied-later", "g-retitle"} {
		require.True(t, rows[g].Applicable(), "%s: %s", g, rows[g].SkipReason)
	}
	_, err := l.st.ModifyBook(l.ids["demote-p"], func(b *database.Book) error { b.IsPrimaryVersion = vtPtr(false); return nil })
	require.NoError(t, err)
	_, err = l.st.ModifyBook(l.ids["applied-p"], func(b *database.Book) error { b.MetadataReviewStatus = vtPtr("matched"); return nil })
	require.NoError(t, err)
	_, err = l.st.ModifyBook(l.ids["retitle-p"], func(b *database.Book) error { b.Title = "Dune (Unabridged)"; return nil })
	require.NoError(t, err)

	res := l.apply(plan, false, "g-demote", "g-applied-later", "g-retitle")
	require.Equal(t, 3, res.ChangedSincePlan, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Empty(t, dcStr(l.get("demote-p").Narrator))
	require.Empty(t, dcStr(l.get("retitle-p").Narrator))
	require.False(t, *l.get("demote-p").IsPrimaryVersion, "the fixer never re-elects")
}

// The apply's under-lock guard: a primary demoted between the re-plan and
// the write is refused inside the commit, and nothing is written.
func TestVersionTwinFixer_UnderLockGuardRefusesDemotedPrimary(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g-race", true, nil)
	l.appliedTwin("t", "g-race", vtDune(), nil)
	_, rows := l.plan()
	fresh, err := l.fixer.Replan(context.Background(), nil, rows["g-race"], &fakeReporter{})
	require.NoError(t, err)
	require.True(t, fresh.Applicable(), fresh.SkipReason)

	_, err = l.st.ModifyBook(l.ids["p"], func(b *database.Book) error { b.IsPrimaryVersion = vtPtr(false); return nil })
	require.NoError(t, err)
	w := repairs.NewWriter(l.st, l.st, l.fixer.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, vtTestOpID)
	err = l.fixer.Apply(context.Background(), w, fresh)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
	p := l.get("p")
	require.Nil(t, p.MetadataReviewStatus)
	require.Empty(t, dcStr(p.Narrator))
	require.False(t, *p.IsPrimaryVersion)
}

// The fixer is registered in the Repairs lane, cleared for iTunes twins'
// database rows, and holds the scan stand-down for its writes.
func TestVersionTwinFixer_Registered(t *testing.T) {
	p := &Plugin{deps: fakeDeps{}}
	f, ok := p.Repairs().Get(versionTwinFixerID)
	require.True(t, ok)
	require.True(t, repairs.AllowsITunesDatabaseOnly(f))
	require.False(t, repairs.SkipsScanStandDown(f))
}
