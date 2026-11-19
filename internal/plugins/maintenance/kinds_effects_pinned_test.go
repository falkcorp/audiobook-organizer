// file: internal/plugins/maintenance/kinds_effects_pinned_test.go
// version: 1.1.4
// guid: bbc0869b-3caf-4c34-96f7-b5383fa30423
// last-edited: 2026-11-19

package maintenance

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"
)

// This file is the one place the write sets of the converted kinds are NOT
// derived from t6WriterResources (kinds_resources.go). Every other Effects test
// builds its expectation from the table, by method name, so a resource dropped
// from a row shrinks the declaration and the expectation together and nothing
// fails: the F5-11 mutation run dropped sync_identity from the purge row and no
// test noticed. The literal below is typed by hand from what the kinds
// declared when they were reviewed, and the test compares the live values to it,
// so a resource lost from any kind's Effects fails here by name; the same pin of
// every row of the shared table lives in internal/maintenance/resources.
//
// Changing a kind's Effects on purpose means editing the literal in the same
// change, and changing a row of the table means editing rowsPinned in
// internal/maintenance/resources. That edit is the point: a narrowing shows up
// in review as a removed word, and has to be justified there (THE RULE in
// internal/maintenance/resources: a row errs wide). Widening is the same edit
// the other way.

// t6EffectsPin is one kind's declared write, read and delete sets: space
// separated pkg/ops resource names in module "books", in any order (the tests
// sort both sides before comparing, so a pinned entry never has to mirror the
// order the code happens to build a set in).
type t6EffectsPin struct{ writes, reads, deletes string }

// t6KindEffectsPinned is every native kind of the six converted groups
// (t6AllNativeKinds), by kind.
var t6KindEffectsPinned = map[tasks.Kind]t6EffectsPin{
	"books.itunes.clone-into-library":                      {writes: "authors book_files books embeddings field_states files fingerprint_index indexes metadata_cache operations sync_identity user_state", reads: "", deletes: ""},
	"books.itunes.heal":                                    {writes: "ai_journal authors books external_ids files indexes metadata_cache operations sync_identity user_state", reads: "", deletes: ""},
	"books.itunes.playlist-import":                         {writes: "user_state", reads: "", deletes: ""},
	"books.itunes.regroup":                                 {writes: "authors book_files books embeddings external_ids field_states indexes metadata_cache sync_identity user_state", reads: "", deletes: ""},
	"books.maintenance.activity-filter-index-backfill":     {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.ai-dedup-batch":                     {writes: "review_items", reads: "", deletes: ""},
	"books.maintenance.author-conjunction-repair":          {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-duplicate-merge":             {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-id-repair":                   {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-path-link":                   {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-split-scan":                  {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.author-strip-merge":                 {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-title-fragment-scan":         {writes: "", reads: "authors", deletes: ""},
	"books.maintenance.author-whitespace-collision-report": {writes: "", reads: "authors", deletes: ""},
	"books.maintenance.authority-build":                    {writes: "authority_lists", reads: "", deletes: ""},
	"books.maintenance.auto-match-transcribed":             {writes: "authors book_files books field_states indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.book-atpath-index-backfill":         {writes: "books", reads: "", deletes: ""},
	"books.maintenance.book-atpath-index-verify":           {writes: "", reads: "", deletes: ""},
	"books.maintenance.book-hash-index-repair":             {writes: "books", reads: "books", deletes: ""},
	"books.maintenance.book-shape-report":                  {writes: "", reads: "", deletes: ""},
	"books.maintenance.bookfile-acoustid-index-repair":     {writes: "book_files", reads: "book_files", deletes: ""},
	"books.maintenance.booksig-recovery-audit":             {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.booksig-sidecar-migrate":            {writes: "books", reads: "", deletes: ""},
	"books.maintenance.build-folder-book-files":            {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.chapters-backfill":                  {writes: "books", reads: "", deletes: ""},
	"books.maintenance.cleanup-activity-log":               {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.cleanup-old-backups":                {writes: "files", reads: "", deletes: ""},
	"books.maintenance.clear-apply-rename-failures":        {writes: "user_state", reads: "", deletes: ""},
	"books.maintenance.combine-journal-check":              {writes: "", reads: "", deletes: ""},
	"books.maintenance.compact-activity-log":               {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.cover-text-read":                    {writes: "cover_text", reads: "", deletes: ""},
	"books.maintenance.credit-census":                      {writes: "", reads: "", deletes: ""},
	"books.maintenance.credits-backfill":                   {writes: "authors books indexes metadata_cache operations", reads: "authors books series", deletes: ""},
	"books.maintenance.dedup-exact-triage":                 {writes: "embeddings", reads: "", deletes: ""},
	"books.maintenance.dedupe-book-file-rows":              {writes: "authors book_files books files fingerprint_index indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.duration-backfill":                  {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.external-id-backfill":               {writes: "external_ids settings", reads: "", deletes: ""},
	"books.maintenance.extract-wav-clips":                  {writes: "authors book_files books files indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.file-integrity-check":               {writes: "", reads: "", deletes: ""},
	"books.maintenance.file-provenance-capture":            {writes: "file_provenance", reads: "", deletes: ""},
	"books.maintenance.file-provenance-export":             {writes: "file_provenance files", reads: "", deletes: ""},
	"books.maintenance.filepath-collision-report":          {writes: "", reads: "", deletes: ""},
	"books.maintenance.folder-cover-backfill":              {writes: "authors books files indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.fs-regroup-xml":                     {writes: "authors book_files books external_ids indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.ident-index-rebuild":                {writes: "books indexes", reads: "", deletes: ""},
	"books.maintenance.intro-migrate-single-file":          {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.library-optimize":                   {writes: "", reads: "", deletes: ""},
	"books.maintenance.malformed-m4b-remux":                {writes: "authors book_files books files indexes metadata_cache settings", reads: "", deletes: ""},
	"books.maintenance.malformed-m4b-transcode":            {writes: "authors book_files books files indexes metadata_cache settings", reads: "", deletes: ""},
	"books.maintenance.mark-missing-files":                 {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.merge-same-path-dupes":              {writes: "authors book_files books external_ids indexes metadata_cache operations sync_identity user_state", reads: "", deletes: ""},
	"books.maintenance.metadata-cache-reap":                {writes: "indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.metadata-refresh":                   {writes: "", reads: "", deletes: ""},
	"books.maintenance.missing-file-audit":                 {writes: "", reads: "", deletes: ""},
	"books.maintenance.missing-file-repair":                {writes: "", reads: "", deletes: ""},
	"books.maintenance.missing-file-repoint":               {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.move-book-file-rows":                {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.movement-atom-cleanup":              {writes: "authors book_files books file_provenance files indexes metadata_cache settings", reads: "", deletes: ""},
	"books.maintenance.nightly-compact-activity-log":       {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.opchange-book-index-rebuild":        {writes: "operations", reads: "", deletes: ""},
	"books.maintenance.orphan-book-files-cleanup":          {writes: "", reads: "", deletes: ""},
	"books.maintenance.orphan-book-files-repoint-plan":     {writes: "", reads: "", deletes: ""},
	"books.maintenance.probe-directory-books":              {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.prune-ai-journal":                   {writes: "ai_journal", reads: "", deletes: ""},
	"books.maintenance.purge-deleted":                      {writes: "activity authors books embeddings external_ids field_states files indexes metadata_cache sync_identity user_state", reads: "", deletes: ""},
	"books.maintenance.purge-empty-authors":                {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.purge-empty-narrators":              {writes: "books operations", reads: "", deletes: ""},
	"books.maintenance.recompact-activity-digests":         {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.recover-missing-files":              {writes: "authors book_files books files indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.regroup-shattered-ai":               {writes: "review_items", reads: "", deletes: ""},
	"books.maintenance.relink-unlinked-books":              {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.repair-library-state":               {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.repair-merged-user-state":           {writes: "authors books indexes metadata_cache sync_identity user_state", reads: "", deletes: ""},
	"books.maintenance.repair-transcribe-status":           {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.repoint-book-file-rows":             {writes: "authors book_files books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.resolve-production-authors":         {writes: "authors book_files books field_states indexes metadata_cache operations series", reads: "", deletes: ""},
	"books.maintenance.review-status-index-repair":         {writes: "review_items", reads: "", deletes: ""},
	"books.maintenance.split-joined-narrators":             {writes: "books operations", reads: "", deletes: ""},
	"books.maintenance.temp-file-cleanup":                  {writes: "activity files", reads: "", deletes: ""},
	"books.maintenance.tombstone-cleanup":                  {writes: "authors", reads: "", deletes: ""},
	"books.maintenance.transcode-temp-cleanup":             {writes: "files", reads: "", deletes: ""},
	"books.maintenance.transcribe-book-intros":             {writes: "ai_journal authors book_files books files indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.transcribe-file-intros":             {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.trash-cleanup":                      {writes: "books files settings", reads: "", deletes: ""},
	"books.maintenance.unknown-author-audit":               {writes: "", reads: "", deletes: ""},
	"books.maintenance.version-group-primary-report":       {writes: "", reads: "", deletes: ""},
	"books.maintenance.reports-prune":                      {writes: "files", reads: "", deletes: ""},
	"books.maintenance.title-backfill":                     {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.title-repair":                       {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.bulk-write-back":                    {writes: "activity authors book_files books file_provenance files indexes metadata_cache operations", reads: "", deletes: ""},
}

// t6PinnedNames is the sorted resource names of refs, which must be in module
// "books".
func t6PinnedNames(t *testing.T, kind tasks.Kind, refs []entity.Ref) string {
	t.Helper()
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		require.Equal(t, "books", r.Module, "%s", kind)
		names = append(names, r.Kind)
	}
	slices.Sort(names)
	return strings.Join(names, " ")
}

// t6PinSorted is the resource names of s in sorted order, duplicates kept: two
// sides are the same list only if they hold the same names the same number of
// times, so a resource listed twice on either side fails the comparison.
func t6PinSorted(s string) string {
	names := strings.Fields(s)
	slices.Sort(names)
	return strings.Join(names, " ")
}

// t6PinDiff names what got lacks of want and what it has beyond it. When both are
// empty the two hold the same names with different counts (a duplicate).
func t6PinDiff(want, got string) string {
	w, g := strings.Fields(want), strings.Fields(got)
	var missing, extra []string
	for _, x := range w {
		if !slices.Contains(g, x) {
			missing = append(missing, x)
		}
	}
	for _, x := range g {
		if !slices.Contains(w, x) {
			extra = append(extra, x)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return "the same names, listed a different number of times"
	}
	return fmt.Sprintf("lost %v, gained %v", missing, extra)
}

// Every native kind declares exactly the write, read and delete sets pinned
// above, and the pinned list has no kind the groups no longer register.
func TestT6Kinds_EffectsEqualThePinnedSets(t *testing.T) {
	seen := map[tasks.Kind]bool{}
	for _, d := range t6AllNativeKinds(t) {
		kind := d.Kind()
		seen[kind] = true
		want, ok := t6KindEffectsPinned[kind]
		if !ok {
			t.Errorf("%s is a native kind with no entry in t6KindEffectsPinned: add one (its declared sets, typed by hand)", kind)
			continue
		}
		e := tasks.CommonOf(d).Effects
		want = t6EffectsPin{writes: t6PinSorted(want.writes), reads: t6PinSorted(want.reads), deletes: t6PinSorted(want.deletes)}
		got := t6EffectsPin{
			writes:  t6PinnedNames(t, kind, e.WriteSet()),
			reads:   t6PinnedNames(t, kind, e.ReadSet()),
			deletes: t6PinnedNames(t, kind, e.DeleteSet()),
		}
		if got.writes != want.writes {
			t.Errorf("%s writes %q, pinned %q (%s)", kind, got.writes, want.writes, t6PinDiff(want.writes, got.writes))
		}
		if got.reads != want.reads {
			t.Errorf("%s reads %q, pinned %q (%s)", kind, got.reads, want.reads, t6PinDiff(want.reads, got.reads))
		}
		if got.deletes != want.deletes {
			t.Errorf("%s deletes %q, pinned %q (%s)", kind, got.deletes, want.deletes, t6PinDiff(want.deletes, got.deletes))
		}
	}
	for kind := range t6KindEffectsPinned {
		if !seen[kind] {
			t.Errorf("t6KindEffectsPinned names %s, which no group registers any more: remove it", kind)
		}
	}
}

// The shared method-to-resource table lives in internal/maintenance/resources,
// and so does the hand-typed pin of every row (resources.TestTable_EqualsThePinnedRows),
// so a change to a row fails there, in the package a jobs-only change also runs.
// This package only has to read that table and not a copy of it.
func TestT6WriterResources_IsTheSharedTable(t *testing.T) {
	require.Equal(t, resources.Table(), t6WriterResources)
}
