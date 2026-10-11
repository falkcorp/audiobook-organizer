// file: internal/plugins/maintenance/relink_pre_fix_imports_fixer_test.go
// version: 1.0.0
// guid: c719ee18-e470-4f27-8a02-a0cf5b7074cd
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Synthetic torrent hashes; every title below is a placeholder.
const (
	pfrHashA     = "aa00000000000000000000000000000000000001"
	pfrHashHeld  = "aa00000000000000000000000000000000000002"
	pfrHashSame  = "aa00000000000000000000000000000000000003"
	pfrHashOther = "aa00000000000000000000000000000000000004"
	pfrHashMulti = "aa00000000000000000000000000000000000005"
)

// newPreFixFixture is a fragFixture whose temp root is a registered import
// path, so versions.CreateIngestVersion's allow-list accepts its files
// without touching the global config (the tests stay parallel).
func newPreFixFixture(t *testing.T) *fragFixture {
	t.Helper()
	f := newFragFixture(t)
	_, err := f.s.CreateImportPath(f.root, "relink-test")
	require.NoError(t, err)
	return f
}

// version creates a version of bookID carrying hash: active when the book has
// none, alt otherwise (the store allows one active version per book).
func (f *fragFixture) version(t *testing.T, bookID, hash string) *database.BookVersion {
	t.Helper()
	status := database.BookVersionStatusActive
	if a, _ := f.s.GetActiveVersionForBook(bookID); a != nil {
		status = database.BookVersionStatusAlt
	}
	source := "imported"
	if hash != "" {
		source = "deluge"
	}
	v, err := f.s.CreateBookVersion(&database.BookVersion{BookID: bookID, Status: status, Format: "m4b",
		Source: source, TorrentHash: hash})
	require.NoError(t, err)
	return v
}

// importRow adds a book_file row to bookID for a fresh file, carrying hash
// as its DelugeHash and naming versionID ("" for none), as role.
func (f *fragFixture) importRow(t *testing.T, role, bookID, hash, versionID string) string {
	t.Helper()
	p := f.file(t, "imports/"+role+".m4b", 300+len(role))
	bf := &database.BookFile{BookID: bookID, FilePath: p, OriginalFilename: role + ".m4b", Format: "m4b",
		FileSize: int64(300 + len(role)), DelugeHash: hash, VersionID: versionID, TrackNumber: 1}
	require.NoError(t, f.s.CreateBookFile(bf))
	f.rowIDs[role] = bf.ID
	return bf.ID
}

// preFixImport seeds what importer.ImportFile left before 2026-10-10: a new
// book, an active version carrying hash, and a row with the same DelugeHash,
// no VersionID and no FileHash.
func (f *fragFixture) preFixImport(t *testing.T, role, hash string) (bookID string, v *database.BookVersion) {
	t.Helper()
	bookID = f.book(t, role, "Placeholder Import "+role, f.path("imports/"+role+".m4b"), nil)
	v = f.version(t, bookID, hash)
	f.importRow(t, role, bookID, hash, "")
	return bookID, v
}

func (f *fragFixture) bookFile(t *testing.T, role string) database.BookFile {
	t.Helper()
	r, err := f.s.GetBookFileByID(f.ids[role], f.rowIDs[role])
	require.NoError(t, err)
	require.NotNil(t, r, role)
	return *r
}

// preFixState is every book_file row and version of the given books, for
// before/after comparison.
func (f *fragFixture) preFixState(t *testing.T, books []string) string {
	t.Helper()
	sort.Strings(books)
	var out []any
	for _, id := range books {
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		vers, err := f.s.GetBookVersionsByBookID(id)
		require.NoError(t, err)
		sort.Slice(vers, func(i, j int) bool { return vers[i].ID < vers[j].ID })
		for _, r := range rows {
			out = append(out, []string{r.ID, r.BookID, r.VersionID, r.DelugeHash, r.FileHash, r.FilePath})
		}
		for _, v := range vers {
			out = append(out, []string{v.ID, v.BookID, v.Status, v.TorrentHash, fmt.Sprint(v.Version)})
		}
	}
	b, err := json.Marshal(out)
	require.NoError(t, err)
	return string(b)
}

func applicableIDs(res *repairs.PlanResult) []string {
	var ids []string
	for _, r := range res.Rows {
		if r.Applicable() {
			ids = append(ids, r.RowID)
		}
	}
	return ids
}

// pfrSeed seeds one book per outcome class and returns the books.
func pfrSeed(t *testing.T, f *fragFixture) map[string]string {
	t.Helper()
	books := map[string]string{}
	// would_link: a torrent import and a manual import.
	books["torrent"], _ = f.preFixImport(t, "torrent", pfrHashA)
	books["manual"], _ = f.preFixImport(t, "manual", "")

	// already_linked: a post-fix import.
	linked := f.book(t, "linked", "Placeholder Linked", f.path("imports/linked.m4b"), nil)
	lv := f.version(t, linked, "")
	f.importRow(t, "linked", linked, "", lv.ID)
	books["linked"] = linked

	// held_by_other_book: this book's stranded version carries the torrent,
	// but another book's linked version took the torrent index since.
	held, _ := f.preFixImport(t, "held", pfrHashHeld)
	thief := f.book(t, "thief", "Placeholder Holder", f.path("imports/thief.m4b"), nil)
	tv := f.version(t, thief, pfrHashHeld)
	f.importRow(t, "thief", thief, pfrHashHeld, tv.ID)
	books["held"], books["thief"] = held, thief

	// same_book_version: row 1 linked to the version holding the torrent,
	// row 2 carrying the same torrent and no version.
	same := f.book(t, "same1", "Placeholder Two Files", f.path("imports/same1.m4b"), nil)
	sv := f.version(t, same, pfrHashSame)
	f.importRow(t, "same1", same, pfrHashSame, sv.ID)
	f.importRow(t, "same2", same, pfrHashSame, "")
	f.ids["same2"] = same
	books["same"] = same

	// foreign_version_id: a merged row still naming the absorbed book's
	// version.
	absorbed := f.book(t, "absorbed", "Placeholder Absorbed", f.path("imports/absorbed.m4b"), nil)
	av := f.version(t, absorbed, "")
	keeper := f.book(t, "foreign", "Placeholder Keeper", f.path("imports/foreign.m4b"), nil)
	f.importRow(t, "foreign", keeper, "", av.ID)
	books["foreign"], books["absorbed"] = keeper, absorbed

	// dangling_version_id.
	dangling := f.book(t, "dangling", "Placeholder Dangling", f.path("imports/dangling.m4b"), nil)
	f.importRow(t, "dangling", dangling, "", "no-such-version")
	books["dangling"] = dangling

	// no_candidate: the book's only version is linked to its other row.
	nc := f.book(t, "nc1", "Placeholder No Candidate", f.path("imports/nc1.m4b"), nil)
	ncv := f.version(t, nc, "")
	f.importRow(t, "nc1", nc, "", ncv.ID)
	f.importRow(t, "nc2", nc, "", "")
	f.ids["nc2"] = nc
	books["nc"] = nc

	// hash_mismatch: the unlinked version carries a torrent, the row none.
	hm := f.book(t, "mismatch", "Placeholder Mismatch", f.path("imports/mismatch.m4b"), nil)
	f.version(t, hm, pfrHashOther)
	f.importRow(t, "mismatch", hm, "", "")
	books["mismatch"] = hm

	// ambiguous: two unlinked hashless versions, one hashless row.
	amb := f.book(t, "ambiguous", "Placeholder Ambiguous", f.path("imports/ambiguous.m4b"), nil)
	f.version(t, amb, "")
	f.version(t, amb, "")
	f.importRow(t, "ambiguous", amb, "", "")
	books["ambiguous"] = amb

	// ambiguous: one hashless version, two hashless rows.
	peers := f.book(t, "peers1", "Placeholder Peers", f.path("imports/peers1.m4b"), nil)
	f.version(t, peers, "")
	f.importRow(t, "peers1", peers, "", "")
	f.importRow(t, "peers2", peers, "", "")
	f.ids["peers2"] = peers
	books["peers"] = peers

	// path_not_allowed: the row's path is outside every allowed prefix.
	outside := f.book(t, "outside", "Placeholder Outside", "/nonexistent-relink-root/outside.m4b", nil)
	f.version(t, outside, "")
	bf := &database.BookFile{BookID: outside, FilePath: "/nonexistent-relink-root/outside.m4b", Format: "m4b", FileSize: 9}
	require.NoError(t, f.s.CreateBookFile(bf))
	f.rowIDs["outside"] = bf.ID
	books["outside"] = outside

	// book_deleted.
	gone, _ := f.preFixImport(t, "deleted", "")
	yes := true
	_, err := f.s.ModifyBook(gone, func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil })
	require.NoError(t, err)
	books["deleted"] = gone

	// A scanned book with no version at all: never listed.
	plain := f.book(t, "plain", "Placeholder Scanned", f.path("imports/plain.m4b"), nil)
	f.importRow(t, "plain", plain, "", "")
	books["plain"] = plain
	return books
}

func TestRelinkPreFixImports_Classes(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	pfrSeed(t, f)
	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)

	want := map[string]string{
		"torrent": pfrClassLink, "manual": pfrClassLink,
		"linked": pfrClassLinked, "thief": pfrClassLinked, "same1": pfrClassLinked, "nc1": pfrClassLinked,
		"held":      pfrClassHeldOther,
		"same2":     pfrClassSameBook,
		"foreign":   pfrClassForeign,
		"dangling":  pfrClassDangling,
		"nc2":       pfrClassNoCandidate,
		"mismatch":  pfrClassHashMismatch,
		"ambiguous": pfrClassAmbiguous, "peers1": pfrClassAmbiguous, "peers2": pfrClassAmbiguous,
		"outside": pfrClassPathNotAllowed,
		"deleted": pfrClassBookDeleted,
	}
	for role, class := range want {
		r := findRow(t, res, f.rowIDs[role])
		require.Equal(t, class, r.Class, "%s: %s", role, r.Reason)
		if class == pfrClassLink || class == pfrClassSameBook {
			require.True(t, r.Applicable(), "%s: %s %s", role, r.Skipped, r.SkipReason)
			require.NotEmpty(t, r.Proposed["version_id"], role)
			require.Equal(t, "missing", r.Current["file_hash"], role)
		} else {
			require.Equal(t, class, r.Skipped, role)
			require.NotEmpty(t, r.SkipReason, role)
		}
		require.Equal(t, []string{f.bookFile(t, role).BookID}, r.BookIDs, role)
	}
	for _, r := range res.Rows {
		require.NotEqual(t, f.rowIDs["plain"], r.RowID, "a book without versions must not be listed")
	}
	require.Equal(t, len(want), res.Total)
	require.Equal(t, 3, res.Applicable)
	require.Equal(t, 2, res.ByClass[pfrClassLink])
	require.Equal(t, 3, res.ByClass[pfrClassAmbiguous])
	require.Equal(t, 1, res.SkippedByKind[pfrClassForeign])

	// The proposed version is the book's own stranded one.
	torrent := findRow(t, res, f.rowIDs["torrent"])
	v, err := f.s.GetBookVersion(torrent.Proposed["version_id"])
	require.NoError(t, err)
	require.Equal(t, f.ids["torrent"], v.BookID)
	require.Equal(t, pfrHashA, v.TorrentHash)
	held := findRow(t, res, f.rowIDs["held"])
	require.Contains(t, held.SkipReason, f.ids["thief"])
}

// The plan and a dry-run apply write nothing: every row and version of every
// seeded book is byte-for-byte what it was.
func TestRelinkPreFixImports_DryRunChangesNothing(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	books := pfrSeed(t, f)
	var ids []string
	for _, id := range books {
		ids = append(ids, id)
	}
	before := f.preFixState(t, ids)

	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	f.applyOp("op-dry", relinkPreFixFixerID)
	params, err := json.Marshal(repairs.ApplyParams{FixerID: relinkPreFixFixerID, PlanOpID: "op-plan", RowIDs: applicableIDs(res)})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: "op-dry"}
	require.NoError(t, f.p.runRepairsApply(context.Background(), params, rep))
	dry, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	require.True(t, dry.DryRun)
	require.Equal(t, 3, dry.ByOutcome[repairs.OutcomeWouldApply], "%+v", dry.Rows)

	require.Equal(t, before, f.preFixState(t, ids))
	changes, err := f.s.GetOperationChanges("op-dry")
	require.NoError(t, err)
	require.Empty(t, changes)
}

// Apply links every would_link and same_book_version row to its planned
// version, fills the identity hash, creates and changes no version, journals
// each link, and a re-plan finds nothing left to link.
func TestRelinkPreFixImports_ApplyLinksAndIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	books := pfrSeed(t, f)
	// A two-file book from one torrent: both rows link to its one version,
	// the second through the same-book path once the first adopted it.
	multi, mv := f.preFixImport(t, "multi1", pfrHashMulti)
	f.importRow(t, "multi2", multi, pfrHashMulti, "")
	f.ids["multi2"] = multi
	books["multi"] = multi

	versionsBefore := map[string]int{}
	for _, id := range books {
		vers, err := f.s.GetBookVersionsByBookID(id)
		require.NoError(t, err)
		versionsBefore[id] = len(vers)
	}
	var otherIDs []string
	for role, id := range books {
		if role != "torrent" && role != "manual" && role != "same" && role != "multi" {
			otherIDs = append(otherIDs, id)
		}
	}
	untouched := f.preFixState(t, otherIDs)

	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	require.Equal(t, pfrClassLink, findRow(t, res, f.rowIDs["multi1"]).Class)
	require.Equal(t, pfrClassLink, findRow(t, res, f.rowIDs["multi2"]).Class)
	ids := applicableIDs(res)
	require.Len(t, ids, 5)

	out := f.fixerApply(t, relinkPreFixFixerID, "op-plan", "op-apply", ids)
	require.Equal(t, 5, out.Applied, "%+v", out.Rows)

	torrentV, err := f.s.GetActiveVersionForBook(f.ids["torrent"])
	require.NoError(t, err)
	manualV, err := f.s.GetActiveVersionForBook(f.ids["manual"])
	require.NoError(t, err)
	sameV, err := f.s.GetActiveVersionForBook(f.ids["same1"])
	require.NoError(t, err)
	for role, want := range map[string]*database.BookVersion{
		"torrent": torrentV, "manual": manualV, "same2": sameV, "multi1": mv, "multi2": mv,
	} {
		r := f.bookFile(t, role)
		require.Equal(t, want.ID, r.VersionID, role)
		require.Equal(t, want.TorrentHash, r.DelugeHash, role)
		h, herr := filehash.BookFileHash(r.FilePath)
		require.NoError(t, herr)
		require.Equal(t, h, r.FileHash, role)
	}
	for _, id := range books {
		vers, verr := f.s.GetBookVersionsByBookID(id)
		require.NoError(t, verr)
		require.Len(t, vers, versionsBefore[id], "book %s gained or lost a version", id)
	}
	idx, err := f.s.GetBookVersionByTorrentHash(pfrHashA)
	require.NoError(t, err)
	require.Equal(t, torrentV.ID, idx.ID)
	require.Equal(t, untouched, f.preFixState(t, otherIDs), "a skipped row was written")

	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	links := 0
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeBookFileVersionLink && !c.Voided {
			links++
			require.Empty(t, c.OldValue)
			require.False(t, undo.IsRestorable(c))
		}
	}
	require.Equal(t, 5, links)

	again := f.fixerPlan(t, relinkPreFixFixerID, "op-plan-2", nil)
	require.Zero(t, again.Applicable, "a re-plan after apply must find nothing to link")
	for _, role := range []string{"torrent", "manual", "same2", "multi1", "multi2"} {
		require.Equal(t, pfrClassLinked, findRow(t, again, f.rowIDs[role]).Class, role)
	}
	// Applying the first plan again writes nothing more.
	second := f.fixerApply(t, relinkPreFixFixerID, "op-plan", "op-apply-2", ids)
	require.Zero(t, second.Applied, "%+v", second.Rows)
}

// Two rows of one book (one torrent) and rows of other books applied at once,
// straight through the fixer's Apply rather than the engine's partitioning:
// the merge and torrent locks alone must keep the result one version per
// book, every row linked to it, and nothing created. Run under -race.
func TestRelinkPreFixImports_ConcurrentApplySameBookAndTorrent(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	multi, mv := f.preFixImport(t, "c1", pfrHashMulti)
	for _, role := range []string{"c2", "c3"} {
		f.importRow(t, role, multi, pfrHashMulti, "")
		f.ids[role] = multi
	}
	manual := map[string]*database.BookVersion{}
	for i := range 4 {
		role := fmt.Sprintf("m%d", i)
		_, v := f.preFixImport(t, role, "")
		manual[role] = v
	}
	fx := newRelinkPreFixFixer(f.p)
	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	ids := applicableIDs(res)
	require.Len(t, ids, 7)

	// Every row re-planned BEFORE any apply, so the later rows of the book
	// arrive at Apply still believing they adopt the stranded version.
	fresh := make([]repairs.Row, 0, len(ids))
	for _, id := range ids {
		r, err := fx.Replan(context.Background(), nil, findRow(t, res, id), nil)
		require.NoError(t, err)
		fresh = append(fresh, r)
	}
	w := repairs.NewWriter(f.s, f.s, relinkPreFixFixerID, "bulk_update", "repairs-").WithJournal(f.s, f.s, "op-apply")
	f.applyOp("op-apply", relinkPreFixFixerID)
	var wg sync.WaitGroup
	errs := make([]error, len(fresh))
	for i := range fresh {
		wg.Go(func() { errs[i] = fx.Apply(context.Background(), w, fresh[i]) })
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, fresh[i].RowID)
	}
	for _, role := range []string{"c1", "c2", "c3"} {
		require.Equal(t, mv.ID, f.bookFile(t, role).VersionID, role)
	}
	vers, err := f.s.GetBookVersionsByBookID(multi)
	require.NoError(t, err)
	require.Len(t, vers, 1)
	for role, v := range manual {
		require.Equal(t, v.ID, f.bookFile(t, role).VersionID, role)
		vs, verr := f.s.GetBookVersionsByBookID(f.ids[role])
		require.NoError(t, verr)
		require.Len(t, vs, 1, role)
	}
}

// A row whose decision moved between the plan and the apply (another writer
// linked it elsewhere) is refused, not overwritten.
func TestRelinkPreFixImports_ChangedSincePlanIsRefused(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	book, _ := f.preFixImport(t, "moved", "")
	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	require.Equal(t, 1, res.Applicable)
	// Something else gives the book a second unlinked version: the row is
	// now ambiguous, and must not be linked to either.
	f.version(t, book, "")
	out := f.fixerApply(t, relinkPreFixFixerID, "op-plan", "op-apply", applicableIDs(res))
	require.Zero(t, out.Applied)
	require.Equal(t, 1, out.ChangedSincePlan, "%+v", out.Rows)
	require.Empty(t, f.bookFile(t, "moved").VersionID)
}

// A pre-fix import merged into another book: the merge moved the row to the
// keeper but left the version on the absorbed, now deleted, book. The keeper
// has no version of its own, yet the row is listed (by its torrent hash) and
// reported, never linked.
func TestRelinkPreFixImports_MergedImportIsReported(t *testing.T) {
	t.Parallel()
	f := newPreFixFixture(t)
	absorbed, v := f.preFixImport(t, "merged", pfrHashMulti)
	keeper := f.book(t, "keeper", "Placeholder Keeper", f.path("imports/keeper"), nil)
	require.NoError(t, f.s.MoveBookFilesToBook([]string{f.rowIDs["merged"]}, absorbed, keeper))
	f.ids["merged"] = keeper
	yes := true
	_, err := f.s.ModifyBook(absorbed, func(b *database.Book) error {
		b.MarkedForDeletion, b.MergedIntoBookID = &yes, &keeper
		return nil
	})
	require.NoError(t, err)

	res := f.fixerPlan(t, relinkPreFixFixerID, "op-plan", nil)
	r := findRow(t, res, f.rowIDs["merged"])
	require.Equal(t, pfrClassDeletedHolder, r.Class, r.Reason)
	require.Equal(t, pfrClassDeletedHolder, r.Skipped)
	require.Contains(t, r.SkipReason, v.ID)
	require.Contains(t, strings.Join(r.Evidence, "\n"), "merged into "+keeper)
	require.Zero(t, res.Applicable)
}
