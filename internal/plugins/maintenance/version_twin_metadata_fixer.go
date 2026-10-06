// file: internal/plugins/maintenance/version_twin_metadata_fixer.go
// version: 1.0.0
// guid: 2f6c8e14-7b3a-4d59-9e02-c4a1b7d36e85
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// versionTwinFixerID is the Repairs-lane id of the version twin fixer.
const versionTwinFixerID = "maintenance.version-twin-metadata"

// Classes of a version twin row.
const (
	// vtClassApplied: a twin had metadata applied; apply carries the same
	// record onto the primary through the normal apply path.
	vtClassApplied = "applied_twin"
	// vtClassCandidates: no twin is applied, but one holds fetched
	// candidates; apply copies them onto the primary (fetch state only).
	vtClassCandidates = "candidates_twin"
)

// Hold (skip) kinds of a version twin row.
const (
	vtHoldResolved         = "primary_resolved"
	vtHoldNoSource         = "no_twin_source"
	vtHoldPrimaryCands     = "primary_has_candidates"
	vtHoldPrimaryAmbiguous = "primary_ambiguous"
	vtHoldNotABSListed     = "primary_not_abs_listed"
	vtHoldITunes           = "primary_itunes"
	vtHoldITunesUnknown    = "primary_itunes_unknown"
	vtHoldLocked           = "primary_locked_fields"
	vtHoldTwinsDisagree    = "twins_disagree"
	vtHoldEdition          = "different_edition"
	vtHoldIdentity         = "identity_mismatch"
	vtHoldUnrecoverable    = "source_candidate_unrecoverable"
	vtHoldASINConflict     = "asin_conflict"
	vtHoldIdentityStale    = "identity_stale"
	vtHoldHashShared       = "hash_shared_outside_group"
	vtHoldGone             = "gone"
	vtHoldReadError        = "error"
)

// vtChangeTypeCacheCopy is the operation-journal row a candidate copy writes
// after the copy (ledger after write). The op revert does not reverse it (it
// is reported not restorable): it is fetch state, and a later candidate fetch
// for the book replaces it.
const vtChangeTypeCacheCopy = "metadata_cache_copy"

// vtDurationTolerance: twins whose known runtimes differ by more than this
// fraction are different editions.
const vtDurationTolerance = 0.05

// versionTwinFixer copies metadata to a version group's primary book from a
// twin in the same group. The 2026-10-05 census found ~859 primaries counted
// "metadata missing" whose version twin was already applied, or held fetched
// candidates: nothing propagated review status or candidates across a group.
//
// A row is one version group (RowID = group id) whose single primary is not
// applied and not marked "no match", and which has a twin that is applied
// (vtClassApplied) or holds cached candidates (vtClassCandidates).
//
//   - applied_twin: the twin's applied record is recovered from its candidate
//     cache (the candidate whose metafetch.CandidateSourceHash equals the
//     twin's MetadataSourceHash) and applied to the primary through
//     metafetch's ApplyMetadataCandidateWithOptions: fill-only, field locks
//     honoured, change history after the commit (so "undo last apply"
//     reverts it), the match stamped. Title and author are NOT in the
//     allowlist: the row requires the primary to hold the record's title and
//     author already (identity), so the apply never rewrites an ABS-listed
//     book's identity, and no file work (rename, tags, cover download) is
//     queued. A twin whose record cannot be recovered is held, never
//     rebuilt from the twin's row.
//   - candidates_twin: the twin's cached candidates are copied onto the
//     primary, re-keyed for it (metafetch.Service.CopyCandidateCache), only
//     when the primary's title and author match the twin's after
//     normalisation. Nothing is applied; the primary joins the review lane.
//
// The fixer NEVER changes which book is primary: it writes only the existing
// primary, and the apply's under-lock guard refuses a book that stopped being
// the primary. A record another version group's book also carries is held
// (hash_shared_outside_group): MATCH-4 would run a cross-group election on
// apply and could flag this group merged.
//
// ITunesDatabaseOnly: twins are often iTunes copies, which this fixer only
// reads; the framework's iTunes path guard would otherwise hold every such
// group. The primary itself is held when it is iTunes-linked
// (itunesCopyWhy), so no iTunes-linked book is written. Doctor Who / Big
// Finish / Torchwood stay guarded on every member.
type versionTwinFixer struct{ p *Plugin }

func newVersionTwinFixer(p *Plugin) *versionTwinFixer { return &versionTwinFixer{p: p} }

var _ repairs.Fixer = (*versionTwinFixer)(nil)

func (f *versionTwinFixer) ID() string    { return versionTwinFixerID }
func (f *versionTwinFixer) Title() string { return "Copy metadata from a version twin to the primary" }
func (f *versionTwinFixer) Description() string {
	return "Version groups whose primary book has no metadata applied while another version of the same book does, or " +
		"holds fetched candidates. Apply carries an applied twin's record onto the primary through the normal metadata " +
		"apply (fill-only, journaled, undoable with \"undo last apply\"), or copies a twin's candidates onto the primary " +
		"for review. The primary is never changed or re-elected. Held: twins that disagree, a different edition " +
		"(runtime, abridgement or narrator), locked fields, iTunes-linked or not-ABS-listed primaries, a different " +
		"title/author, an ASIN conflict, and Doctor Who / Big Finish / Torchwood."
}

// ITunesDatabaseOnly: see the type comment. The fixer writes no file and no
// iTunes id; the primary's own iTunes link is a hold.
func (f *versionTwinFixer) ITunesDatabaseOnly() bool { return true }

// vtDetail is what Apply needs from Replan.
type vtDetail struct {
	class     string
	groupID   string
	primaryID string
	twinID    string
	cand      *metafetch.MetadataCandidate
	// title is the primary's normalised title at plan time; the under-lock
	// guard refuses a primary retitled since.
	title string
	asin  string
}

// vtMember is one live member of a group with what the row reads of it.
type vtMember struct {
	core  database.BookCore
	entry *database.MetadataCandidateCache
}

func (m *vtMember) candidates() int {
	if m.entry == nil {
		return 0
	}
	return len(m.entry.Candidates)
}

// vtReaders is everything one row reads.
type vtReaders struct {
	store OpsStore
	cache database.MetadataCacheStore
	svc   VersionTwinMetadataService
	hash  func(string) ([]database.Book, error)
	res   *repairs.PathResolver
}

func (f *versionTwinFixer) readers(res *repairs.PathResolver) (vtReaders, error) {
	r := vtReaders{store: f.p.deps.OpsStore(), cache: f.p.deps.MetadataCacheStore(),
		svc: f.p.deps.VersionTwinMetadataService(), hash: f.p.deps.BooksWithMetadataSourceHash, res: res}
	if r.store == nil || r.cache == nil {
		return r, fmt.Errorf("database not initialized")
	}
	if r.svc == nil {
		return r, fmt.Errorf("metadata fetch service not initialized")
	}
	return r, nil
}

// vtLive reports whether b is a live member: not trashed, not merged away.
func vtLive(b *database.BookCore) bool {
	return !b.IsSoftDeleted() && dcStr(b.MergedIntoBookID) == ""
}

func vtIsPrimary(b *database.BookCore) bool {
	return b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
}

func vtNoMatch(b *database.BookCore) bool { return metafetch.IsMarkedNoMatch(b.MetadataReviewStatus) }

// Plan lists every version group whose one primary needs metadata and has a
// twin to take it from.
func (f *versionTwinFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	rd, err := f.readers(repairs.NewPathResolver())
	if err != nil {
		return nil, err
	}
	all, err := rd.store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	groups := map[string][]database.BookCore{}
	for i := range all {
		b := all[i]
		gid := dcStr(b.VersionGroupID)
		if gid == "" || !vtLive(&b) {
			continue
		}
		groups[gid] = append(groups[gid], b)
	}
	// The cheap filter: a group of two or more whose primary (or, with no
	// single primary, some member) leaves room for this fixer.
	var gids []string
	for gid, ms := range groups {
		if len(ms) < 2 {
			continue
		}
		var primaries []*database.BookCore
		applied := false
		for i := range ms {
			if vtIsPrimary(&ms[i]) {
				primaries = append(primaries, &ms[i])
			}
			applied = applied || database.MetadataApplied(ms[i].MetadataReviewStatus)
		}
		if len(primaries) != 1 {
			if applied {
				gids = append(gids, gid)
			}
			continue
		}
		if p := primaries[0]; database.MetadataApplied(p.MetadataReviewStatus) || vtNoMatch(p) {
			continue
		}
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	rows := make([]repairs.Row, len(gids))
	keep := make([]bool, len(gids))
	var done atomic.Int64
	// Each worker writes only rows[i] and keep[i] for its own i, and reads
	// only its own group: groups are disjoint, so no two workers share a book.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(gids)), func(_ context.Context, i int) error {
		defer done.Add(1)
		r, k, rerr := f.row(rd, gids[i], groups[gids[i]])
		if rerr != nil {
			r = vtErrorRow(gids[i], groups[gids[i]], rerr)
			k = true
		}
		rows[i], keep[i] = r, k
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Version twins %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([]repairs.Row, 0, len(rows))
	for i := range rows {
		if keep[i] {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// Replan re-reads the group. A group whose primary was applied, re-elected
// or retitled since, or whose twin changed, comes back with a different
// fingerprint (changed_since_plan).
func (f *versionTwinFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	rd, err := f.readers(repairs.NewPathResolver())
	if err != nil {
		return repairs.Row{}, err
	}
	books, err := rd.store.GetBooksByVersionGroup(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read version group %s: %w", planned.RowID, err)
	}
	var ms []database.BookCore
	for i := range books {
		c := books[i].Core()
		if vtLive(&c) && dcStr(c.VersionGroupID) == planned.RowID {
			ms = append(ms, c)
		}
	}
	if len(ms) < 2 {
		r := repairs.Row{RowID: planned.RowID, BookIDs: planned.BookIDs, Title: planned.Title, Risk: repairs.RiskLow}
		r.Skipped, r.SkipReason = vtHoldGone, "the version group no longer has two live members"
		r.Reason = r.SkipReason
		r.Fingerprint = vtFingerprint(r, "gone")
		return r, nil
	}
	r, _, err := f.row(rd, planned.RowID, ms)
	return r, err
}

func vtErrorRow(gid string, ms []database.BookCore, err error) repairs.Row {
	ids := make([]string, 0, len(ms))
	title := ""
	for i := range ms {
		ids = append(ids, ms[i].ID)
		if vtIsPrimary(&ms[i]) {
			title = ms[i].Title
		}
	}
	sort.Strings(ids)
	r := repairs.Row{RowID: gid, BookIDs: ids, Title: title, Risk: repairs.RiskReview,
		Skipped: vtHoldReadError, SkipReason: err.Error(), Reason: err.Error()}
	r.Fingerprint = vtFingerprint(r, "read-error")
	return r
}

// vtFingerprint hashes the row's decision inputs.
func vtFingerprint(r repairs.Row, extra string) string {
	sum := sha256.Sum256([]byte(r.RowID + "\n" + r.Class + "\n" + r.Skipped + "\n" + extra))
	return hex.EncodeToString(sum[:])[:32]
}

// vtBuild accumulates one row: what it shows and what its fingerprint hashes.
type vtBuild struct {
	r  repairs.Row
	fp []string
}

func (b *vtBuild) note(parts ...string) { b.fp = append(b.fp, parts...) }

func (b *vtBuild) hold(kind, why string) repairs.Row {
	b.r.Skipped, b.r.SkipReason, b.r.Reason = kind, why, why
	b.r.Fingerprint = vtFingerprint(b.r, strings.Join(b.fp, "\x00"))
	return b.r
}

// row decides one group. keep is false for a group that is not a row (the
// primary needs nothing, or no twin has anything to give); the returned row
// is then the skip a Replan reports.
func (f *versionTwinFixer) row(rd vtReaders, gid string, members []database.BookCore) (repairs.Row, bool, error) {
	ms := make([]vtMember, len(members))
	for i := range members {
		ms[i] = vtMember{core: members[i]}
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].core.ID < ms[j].core.ID })
	b := &vtBuild{r: repairs.Row{RowID: gid, Risk: repairs.RiskReview}}
	var primaries []int
	for i := range ms {
		c := &ms[i].core
		b.r.BookIDs = append(b.r.BookIDs, c.ID)
		b.note(c.ID, strconv.FormatBool(vtIsPrimary(c)), dcStr(c.MetadataReviewStatus), c.Title, dcStr(c.ASIN),
			dcStr(c.MetadataSourceHash), dcStr(c.LibraryState), vtIntStr(c.Duration), dcStr(c.Narrator), vtBoolStr(c.Abridged))
		if vtIsPrimary(c) {
			primaries = append(primaries, i)
		}
	}
	if len(primaries) != 1 {
		b.r.Title = ms[0].core.Title
		for i := range ms {
			b.r.Members = append(b.r.Members, repairs.RowMember{BookID: ms[i].core.ID, Title: ms[i].core.Title, Role: "member"})
		}
		applied := false
		for i := range ms {
			applied = applied || database.MetadataApplied(ms[i].core.MetadataReviewStatus)
		}
		r := b.hold(vtHoldPrimaryAmbiguous, fmt.Sprintf("the group has %d primary books, not one; "+
			"the version-group-primary repair settles that, this fixer never elects one", len(primaries)))
		return r, applied, nil
	}
	pi := primaries[0]
	p := &ms[pi]
	b.r.Title = p.core.Title
	if database.MetadataApplied(p.core.MetadataReviewStatus) || vtNoMatch(&p.core) {
		return b.hold(vtHoldResolved, "the primary's metadata is applied, or it is marked \"no match\""), false, nil
	}
	for i := range ms {
		entry, err := rd.cache.GetMetadataCache(ms[i].core.ID)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("read the candidate cache of %s: %w", ms[i].core.ID, err)
		}
		ms[i].entry = entry
		fetched := ""
		if entry != nil {
			fetched = entry.FetchedAt.UTC().Format(time.RFC3339Nano)
		}
		b.note(strconv.Itoa(ms[i].candidates()), fetched)
	}

	var applied, cands []int
	for i := range ms {
		if i == pi || vtNoMatch(&ms[i].core) {
			continue
		}
		if database.MetadataApplied(ms[i].core.MetadataReviewStatus) {
			applied = append(applied, i)
		} else if ms[i].candidates() > 0 {
			cands = append(cands, i)
		}
	}
	switch {
	case len(applied) > 0:
		b.r.Class = vtClassApplied
	case len(cands) > 0:
		b.r.Class = vtClassCandidates
		if p.candidates() > 0 {
			return b.hold(vtHoldPrimaryCands, "the primary already holds fetched candidates of its own"), false, nil
		}
	default:
		return b.hold(vtHoldNoSource, "no twin is applied or holds candidates"), false, nil
	}

	authorsOf := map[int][]string{}
	readAuthors := func(i int) ([]string, error) {
		if a, ok := authorsOf[i]; ok {
			return a, nil
		}
		book := ms[i].core.ToBook()
		a, err := repairs.BookAuthorNames(rd.store, &book)
		if err != nil {
			return nil, fmt.Errorf("read authors of %s: %w", ms[i].core.ID, err)
		}
		authorsOf[i] = a
		return a, nil
	}
	pAuthors, err := readAuthors(pi)
	if err != nil {
		return repairs.Row{}, false, err
	}

	// Choose the twin.
	twin := -1
	var cand *metafetch.MetadataCandidate
	if b.r.Class == vtClassApplied {
		for _, i := range applied {
			if c := vtAppliedCandidate(&ms[i]); c != nil {
				twin, cand = i, c
				break
			}
		}
		if twin < 0 {
			twin = applied[0]
		}
	} else {
		twin = cands[0]
		for _, i := range cands[1:] {
			if ms[i].entry.FetchedAt.After(ms[twin].entry.FetchedAt) {
				twin = i
			}
		}
	}
	t := &ms[twin]
	tAuthors, err := readAuthors(twin)
	if err != nil {
		return repairs.Row{}, false, err
	}
	b.note("twin", t.core.ID)
	f.display(b, ms, pi, twin, pAuthors, tAuthors, cand)

	// Holds on the primary itself.
	if !database.ABSLibraryFilter().MatchesCore(&p.core) {
		return b.hold(vtHoldNotABSListed, "the primary is not listed by ABS (it must be primary, organized and not quarantined)"), true, nil
	}
	why, doubt, err := vtITunesWhy(rd, &p.core)
	if err != nil {
		return repairs.Row{}, false, err
	}
	b.note(why, strconv.FormatBool(doubt))
	switch {
	case why != "":
		return b.hold(vtHoldITunes, "the primary is iTunes-linked ("+why+"); iTunes-linked books are never written"), true, nil
	case doubt:
		return b.hold(vtHoldITunesUnknown, "could not tell whether the primary is iTunes-linked"), true, nil
	}
	locks, err := database.LoadFieldLocks(rd.store, p.core.ID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read field locks of %s: %w", p.core.ID, err)
	}
	if locks.Any() {
		keys := make([]string, 0)
		for k := range locks.Set() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.note("locks", strings.Join(keys, ","))
		return b.hold(vtHoldLocked, "the primary has locked fields ("+strings.Join(keys, ", ")+
			"); a person or a repair chose those values"), true, nil
	}

	// The twins must agree with each other.
	disagree := applied
	if b.r.Class == vtClassCandidates {
		disagree = cands
	}
	if why, err := vtTwinsDisagree(ms, disagree, readAuthors); err != nil {
		return repairs.Row{}, false, err
	} else if why != "" {
		return b.hold(vtHoldTwinsDisagree, why), true, nil
	}

	// Same edition.
	if why := vtEditionDiffers(&p.core, &t.core); why != "" {
		return b.hold(vtHoldEdition, why), true, nil
	}

	pBook := p.core.ToBook()
	tBook := t.core.ToBook()
	if b.r.Class == vtClassApplied {
		if cand == nil {
			return b.hold(vtHoldUnrecoverable, "no cached candidate of twin "+t.core.ID+" carries its applied record "+
				"(metadata_source_hash); its candidates were deleted by an older apply. Refetch the twin, or apply by hand"), true, nil
		}
		hash := metafetch.CandidateSourceHash(*cand)
		b.note("cand", hash)
		if why := vtIdentity(pBook.Title, pAuthors, cand.Title, tAuthors); why != "" {
			return b.hold(vtHoldIdentity, why), true, nil
		}
		if res := applygate.CheckASIN(&pBook, cand); res.Outcome == applygate.OutcomeBlock {
			return b.hold(vtHoldASINConflict, "the primary's ASIN conflicts with the twin's record: "+res.Detail), true, nil
		}
		if serr := metafetch.CandidateASINStale(t.entry, &tBook, cand); serr != nil {
			return b.hold(vtHoldIdentityStale, serr.Error()), true, nil
		}
		outside, err := vtOutsideGroup(rd, gid, hash)
		if err != nil {
			return repairs.Row{}, false, err
		}
		b.note("outside", strings.Join(outside, ","))
		if len(outside) > 0 {
			return b.hold(vtHoldHashShared, "the twin's record is also carried by "+strings.Join(outside, ", ")+
				" outside this version group; applying it would run a cross-group duplicate election"), true, nil
		}
		b.r.Proposed["review_status"] = "matched"
		b.r.Proposed["source"] = cand.Source
		b.r.Reason = fmt.Sprintf("twin %s had %s metadata applied (record %s); the primary is the same book (same "+
			"title and author) and has none", t.core.ID, cand.Source, vtRecord(cand))
	} else {
		if why := vtIdentity(pBook.Title, pAuthors, tBook.Title, tAuthors); why != "" {
			return b.hold(vtHoldIdentity, why), true, nil
		}
		pASIN, tASIN := dcStr(p.core.ASIN), dcStr(t.core.ASIN)
		if tASIN == "" {
			tASIN = strings.TrimSpace(t.entry.FetchedForASIN)
		}
		if pASIN != "" && tASIN != "" && !strings.EqualFold(pASIN, tASIN) {
			return b.hold(vtHoldASINConflict, fmt.Sprintf("the primary carries ASIN %s, the twin's candidates were fetched for %s", pASIN, tASIN)), true, nil
		}
		if verr := rd.svc.ValidateCachedIdentityForBook(t.entry, &tBook, tAuthors); verr != nil {
			return b.hold(vtHoldIdentityStale, "the twin's candidates were fetched for a title or author it no longer has: "+verr.Error()), true, nil
		}
		b.r.Proposed["candidates"] = strconv.Itoa(t.candidates())
		b.r.Reason = fmt.Sprintf("twin %s holds %d fetched candidates for the same title and author; the primary has "+
			"none. Copying them puts the primary in the review lane (nothing is applied)", t.core.ID, t.candidates())
	}
	b.r.Fingerprint = vtFingerprint(b.r, strings.Join(b.fp, "\x00"))
	b.r.Detail = &vtDetail{class: b.r.Class, groupID: gid, primaryID: p.core.ID, twinID: t.core.ID, cand: cand,
		title: util.NormalizeTitle(p.core.Title), asin: dcStr(p.core.ASIN)}
	return b.r, true, nil
}

// display fills the row's Members, Current and Evidence: primary and twin,
// each with id, title, author, duration, narrator, ASIN and review status.
func (f *versionTwinFixer) display(b *vtBuild, ms []vtMember, pi, ti int, pAuthors, tAuthors []string, cand *metafetch.MetadataCandidate) {
	for i := range ms {
		role := "version"
		switch i {
		case pi:
			role = "primary"
		case ti:
			role = "twin"
		}
		b.r.Members = append(b.r.Members, repairs.RowMember{BookID: ms[i].core.ID, Title: ms[i].core.Title, Role: role})
	}
	b.r.Author = strings.Join(pAuthors, ", ")
	b.r.Current = map[string]string{}
	b.r.Proposed = map[string]string{}
	put := func(prefix string, c *database.BookCore, authors []string, cands int) {
		b.r.Current[prefix+"_id"] = c.ID
		b.r.Current[prefix+"_title"] = c.Title
		b.r.Current[prefix+"_author"] = strings.Join(authors, ", ")
		b.r.Current[prefix+"_duration"] = vtIntStr(c.Duration)
		b.r.Current[prefix+"_narrator"] = dcStr(c.Narrator)
		b.r.Current[prefix+"_asin"] = dcStr(c.ASIN)
		b.r.Current[prefix+"_review_status"] = dcStr(c.MetadataReviewStatus)
		b.r.Current[prefix+"_candidates"] = strconv.Itoa(cands)
	}
	put("primary", &ms[pi].core, pAuthors, ms[pi].candidates())
	put("twin", &ms[ti].core, tAuthors, ms[ti].candidates())
	b.r.Evidence = []string{"same version group", "twin review status: " + dcStr(ms[ti].core.MetadataReviewStatus)}
	if cand != nil {
		b.r.Evidence = append(b.r.Evidence, "twin's applied record recovered from its candidate cache: "+vtRecord(cand))
	}
}

// vtRecord names a candidate's record for the owner.
func vtRecord(c *metafetch.MetadataCandidate) string {
	id := c.ASIN
	if id == "" {
		id = c.ISBN
	}
	return fmt.Sprintf("%s %q by %s [%s]", c.Source, c.Title, c.Author, id)
}

// vtAppliedCandidate is the twin's applied candidate: the cached candidate
// whose source hash is the twin's MetadataSourceHash. nil when none is.
func vtAppliedCandidate(m *vtMember) *metafetch.MetadataCandidate {
	want := dcStr(m.core.MetadataSourceHash)
	if want == "" || m.entry == nil {
		return nil
	}
	for _, raw := range m.entry.Candidates {
		var c metafetch.MetadataCandidate
		if err := json.Unmarshal(raw, &c); err != nil {
			continue
		}
		if metafetch.CandidateSourceHash(c) == want {
			return &c
		}
	}
	return nil
}

// vtITunesWhy runs the shared iTunes predicate on the primary.
func vtITunesWhy(rd vtReaders, c *database.BookCore) (string, bool, error) {
	files, err := rd.store.GetBookFiles(c.ID)
	if err != nil {
		return "", false, fmt.Errorf("read files of %s: %w", c.ID, err)
	}
	exts, err := rd.store.GetExternalIDsForBook(c.ID)
	if err != nil {
		return "", false, fmt.Errorf("read external ids of %s: %w", c.ID, err)
	}
	paths := []string{c.FilePath}
	ff := make([]fragFile, 0, len(files))
	for i := range files {
		paths = append(paths, files[i].FilePath)
		ff = append(ff, fragFile{ID: files[i].ID, ITunesPID: files[i].ITunesPersistentID, ITunesPath: files[i].ITunesPath})
	}
	why, doubt := itunesCopyWhy(rd.res, c.ID, dcStr(c.ITunesPersistentID), paths, ff, exts)
	return why, doubt, nil
}

// vtTwinsDisagree names why the given twins are not one record: two
// different ASINs, or titles or authors that differ after normalisation.
func vtTwinsDisagree(ms []vtMember, idx []int, authors func(int) ([]string, error)) (string, error) {
	if len(idx) < 2 {
		return "", nil
	}
	first := idx[0]
	fa, err := authors(first)
	if err != nil {
		return "", err
	}
	for _, i := range idx[1:] {
		a, b := &ms[first].core, &ms[i].core
		if x, y := dcStr(a.ASIN), dcStr(b.ASIN); x != "" && y != "" && !strings.EqualFold(x, y) {
			return fmt.Sprintf("twins %s and %s carry different ASINs (%s, %s)", a.ID, b.ID, x, y), nil
		}
		ia, err := authors(i)
		if err != nil {
			return "", err
		}
		if why := vtIdentity(a.Title, fa, b.Title, ia); why != "" {
			return fmt.Sprintf("twins %s and %s disagree: %s", a.ID, b.ID, why), nil
		}
	}
	return "", nil
}

// vtEditionDiffers names why the twin is a different edition of the
// primary: runtimes more than 5% apart (both known), abridged against
// unabridged, or different narrators (both known).
func vtEditionDiffers(p, t *database.BookCore) string {
	if p.Duration != nil && t.Duration != nil && *p.Duration > 0 && *t.Duration > 0 {
		a, b := float64(*p.Duration), float64(*t.Duration)
		if math.Abs(a-b)/math.Max(a, b) > vtDurationTolerance {
			return fmt.Sprintf("runtimes differ by more than 5%% (primary %d, twin %d)", *p.Duration, *t.Duration)
		}
	}
	if p.Abridged != nil && t.Abridged != nil && *p.Abridged != *t.Abridged {
		return fmt.Sprintf("abridged %v on the primary, %v on the twin", *p.Abridged, *t.Abridged)
	}
	pn, tn := fbNorm(dcStr(p.Narrator)), fbNorm(dcStr(t.Narrator))
	if pn != "" && tn != "" && !strings.Contains(pn, tn) && !strings.Contains(tn, pn) {
		return fmt.Sprintf("different narrators (primary %q, twin %q)", dcStr(p.Narrator), dcStr(t.Narrator))
	}
	return ""
}

// vtIdentity names why two books are not the same search identity: titles
// that differ after util.NormalizeTitle (the normalisation the apply's match
// stamp compares by), or author lists with no name in common after
// normalisation (an empty list matches nothing). "" when they match.
func vtIdentity(aTitle string, aAuthors []string, bTitle string, bAuthors []string) string {
	if util.NormalizeTitle(aTitle) != util.NormalizeTitle(bTitle) || util.NormalizeTitle(aTitle) == "" {
		return fmt.Sprintf("titles differ (%q, %q)", aTitle, bTitle)
	}
	seen := map[string]bool{}
	for _, a := range aAuthors {
		if n := fbNorm(a); n != "" {
			seen[n] = true
		}
	}
	for _, a := range bAuthors {
		if seen[fbNorm(a)] {
			return ""
		}
	}
	return fmt.Sprintf("authors differ (%q, %q)", strings.Join(aAuthors, ", "), strings.Join(bAuthors, ", "))
}

// vtOutsideGroup lists the live books outside group gid carrying hash.
func vtOutsideGroup(rd vtReaders, gid, hash string) ([]string, error) {
	books, err := rd.hash(hash)
	if err != nil {
		return nil, fmt.Errorf("read books carrying record hash %s: %w", hash, err)
	}
	var out []string
	for i := range books {
		c := books[i].Core()
		if vtLive(&c) && dcStr(c.VersionGroupID) != gid {
			out = append(out, c.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func vtIntStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func vtBoolStr(p *bool) string {
	if p == nil {
		return ""
	}
	return strconv.FormatBool(*p)
}

// vtApplyFields is every apply field except title and author: the row
// requires the primary to hold the record's title and author already, so the
// apply never rewrites them.
func vtApplyFields() []string {
	var out []string
	for _, k := range metafetch.ApplyFieldKeys() {
		if k != "title" && k != "author" {
			out = append(out, k)
		}
	}
	return out
}

// errVTChanged wraps repairs.ErrChangedSincePlan for the under-lock guard.
func errVTChanged(format string, args ...any) error {
	return fmt.Errorf("%w: %s", repairs.ErrChangedSincePlan, fmt.Sprintf(format, args...))
}

// vtStillNeeds is the under-lock check on the primary's row: still this
// group's primary, still unapplied and not "no match", same title and ASIN as
// planned.
func vtStillNeeds(b *database.Book, d *vtDetail) error {
	switch {
	case b == nil || b.IsSoftDeleted():
		return errVTChanged("primary %s is gone", d.primaryID)
	case b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion:
		return errVTChanged("book %s is no longer its group's primary", d.primaryID)
	case dcStr(b.VersionGroupID) != d.groupID:
		return errVTChanged("book %s left version group %s", d.primaryID, d.groupID)
	case database.MetadataApplied(b.MetadataReviewStatus) || metafetch.IsMarkedNoMatch(b.MetadataReviewStatus):
		return errVTChanged("book %s was applied or marked no match", d.primaryID)
	case util.NormalizeTitle(b.Title) != d.title:
		return errVTChanged("book %s was retitled", d.primaryID)
	case !strings.EqualFold(dcStr(b.ASIN), d.asin):
		return errVTChanged("book %s's ASIN changed", d.primaryID)
	}
	return nil
}

// errVTMatchNotRecorded: the apply wrote but did not stamp the match.
var errVTMatchNotRecorded = errors.New("the apply did not record the match")

// Apply writes one fresh row: the twin's record applied to the primary, or
// the twin's candidates copied onto it. Neither goes through w's book
// primitives (the apply records its own change history after the commit;
// the candidate cache is not a book row), so each is preceded by w.Beat to
// renew the scan stand-down lease, and the copy is journaled after it lands.
func (f *versionTwinFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*vtDetail)
	if !ok || d == nil {
		return fmt.Errorf("row %s: no apply detail from the re-plan", fresh.RowID)
	}
	svc := f.p.deps.VersionTwinMetadataService()
	if svc == nil {
		return fmt.Errorf("metadata fetch service not initialized")
	}
	guard := func(b *database.Book) error { return vtStillNeeds(b, d) }
	switch d.class {
	case vtClassApplied:
		if err := w.Beat("metadata apply on book " + d.primaryID); err != nil {
			return err
		}
		// FillOnly: an automatic apply (nobody picked this candidate for this
		// book): filled descriptive fields are kept, a "no match" is
		// refused, and the match is stamped because the primary holds the
		// record's title. No file work is queued after it.
		resp, err := svc.ApplyMetadataCandidateWithOptions(d.primaryID, *d.cand, vtApplyFields(),
			metafetch.ApplyOptions{FillOnly: true, Guard: guard})
		if err != nil {
			return fmt.Errorf("apply twin %s's record to %s: %w", d.twinID, d.primaryID, err)
		}
		if resp == nil || resp.Book == nil || !database.MetadataApplied(resp.Book.MetadataReviewStatus) {
			return fmt.Errorf("%w on %s", errVTMatchNotRecorded, d.primaryID)
		}
		return nil
	case vtClassCandidates:
		if err := w.Beat("candidate copy onto book " + d.primaryID); err != nil {
			return err
		}
		cp, err := svc.CopyCandidateCache(d.twinID, d.primaryID, func(b *database.Book, cur *database.MetadataCandidateCache) error {
			if err := guard(b); err != nil {
				return err
			}
			if cur != nil && len(cur.Candidates) > 0 {
				return errVTChanged("book %s gained candidates of its own", d.primaryID)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("copy twin %s's candidates to %s: %w", d.twinID, d.primaryID, err)
		}
		if jerr := w.Journal(d.primaryID, vtChangeTypeCacheCopy, "candidates", "0",
			fmt.Sprintf("%d candidates copied from %s", len(cp.Candidates), d.twinID)); jerr != nil {
			return fmt.Errorf("candidates copied to %s, but the journal row was not recorded: %w", d.primaryID, jerr)
		}
		return nil
	}
	return fmt.Errorf("row %s: unknown class %q", fresh.RowID, d.class)
}
