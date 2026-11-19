// file: internal/plugins/maintenance/kinds_f5_18b_gate_test.go
// version: 1.0.0
// guid: 91c3e5a7-2b04-4d68-a1f9-6e7d0c4b8325
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/metafetchgate"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// f518bSource is the one metadata source of these tests: a fixed answer.
type f518bSource struct{ results []metadata.BookMetadata }

func (*f518bSource) Name() string { return "f518b-fake" }
func (s *f518bSource) SearchByTitle(context.Context, string) ([]metadata.BookMetadata, error) {
	return s.results, nil
}
func (s *f518bSource) SearchByTitleAndAuthor(context.Context, string, string) ([]metadata.BookMetadata, error) {
	return s.results, nil
}

// f518bGatedService is a metafetch.Service over inner whose every store write
// goes through the run's writer w (F5-18b): the shape F5-18 gives the server's
// fetch service. Nothing else writes: file work is not wired (see the seam).
func f518bGatedService(t *testing.T, inner metafetch.Store, w tasks.Writer, results []metadata.BookMetadata) *metafetch.Service {
	t.Helper()
	rows, err := metafetchgate.Rows()
	require.NoError(t, err)
	svc := metafetch.NewService(metafetchgate.Wrap(t.Context(), inner, writer.New(w, rows)))
	svc.SetOverrideSources([]metadata.MetadataSource{&f518bSource{results: results}})
	return svc
}

// f518bFetcher is what server.FetchMetadataByTitle does over a service: the
// fetch the resolve kind runs per book.
type f518bFetcher struct{ svc *metafetch.Service }

func (f518bFetcher) HasService() bool { return true }
func (f f518bFetcher) FetchByTitle(ctx context.Context, bookID string) (*int, error) {
	resp, err := f.svc.FetchMetadataForBookByTitle(ctx, bookID, metafetch.Unattended)
	if err != nil || resp == nil || resp.Book == nil {
		return nil, err
	}
	return resp.Book.AuthorID, nil
}

// f518bResources strips the module from stamped "books/x" refs.
func f518bResources(stamped map[string]bool) map[string]bool {
	out := map[string]bool{}
	for s := range stamped {
		out[strings.TrimPrefix(s, "books/")] = true
	}
	return out
}

// TestF518b_ResolveProductionAuthors_RunsThroughTheEffectsEnforcingWriter
// (O9, proves 3): the real metafetch apply of resolve-production-authors, over
// a real store, with every store write fenced against the kind's declared
// Effects. The run is not refused, every resource the store diff shows changed
// is a stamped one, and every stamped resource is declared.
func TestF518b_ResolveProductionAuthors_RunsThroughTheEffectsEnforcingWriter(t *testing.T) {
	f518bRealRun(t, f518bResolveRun, nil)
}

// f518bRun is one real run of a kind over a fresh store: it returns the
// kind's definition, the enforcing writer it ran under, the store and its
// snapshots around the run. declare, when set, replaces the declared write set
// the writer fences against.
type f518bRun struct {
	def           tasks.Definition
	w             *writertest.Writer
	before, after map[string]string
}

func f518bResolveRun(t *testing.T, declare func([]entity.Ref) []entity.Ref) f518bRun {
	t.Helper()
	s := newRepairPebble(t)
	prod, err := s.CreateAuthor("Gotham Playhouse Theater")
	require.NoError(t, err)
	repairBook(t, s, "b1", &prod.ID, prod.ID)
	for _, id := range []string{"b1"} {
		_, err := s.UpdateBook(id, &database.Book{ID: id, Title: "Year One", FilePath: "/lib/" + id + ".m4b", AuthorID: &prod.ID})
		require.NoError(t, err)
	}

	p := &t6g1LiveProbe{}
	deps := p.deps(s)
	var enforcing *writertest.Writer
	deps.NewMetadataFetcher = func(w tasks.Writer) t6g1MetadataFetcher {
		enforcing = w.(*writertest.Writer)
		return f518bFetcher{f518bGatedService(t, s, w, []metadata.BookMetadata{{
			Title: "Year One", Author: "Bruce Wayne", Publisher: "Wayne Audio", Series: "Gotham Chronicles", SeriesPosition: "1",
			Description: "A masked vigilante's first year.", PublishYear: 1987, Language: "English",
		}})}
	}
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), resolveProductionAuthorsKind)
	w := f518bWriter(def, declare)

	before := t6g5Snapshot(t, s)
	_, _, err = runT6G1Full(t, def, tasks.ModeLive, w, "", nil)
	require.NoError(t, err)
	require.Same(t, w, enforcing, "the fetcher was built over the run's writer")
	return f518bRun{def, w, before, t6g5Snapshot(t, s)}
}

// f518bWriter is the enforcing writer for def, over its own write set or over
// declare's replacement of it.
func f518bWriter(def tasks.Definition, declare func([]entity.Ref) []entity.Ref) *writertest.Writer {
	if declare == nil {
		return writertest.New(def, tasks.ModeLive)
	}
	return writertest.ForSet(def.Kind(), declare(tasks.CommonOf(def).Effects.WriteSet()), tasks.ModeLive)
}

// f518bRealRun runs a kind unmodified and requires its real apply to be
// admitted: no refusal, every stamp declared, every changed resource stamped.
func f518bRealRun(t *testing.T, run func(*testing.T, func([]entity.Ref) []entity.Ref) f518bRun, declare func([]entity.Ref) []entity.Ref) {
	t.Helper()
	r := run(t, declare)
	w := r.w
	for _, wr := range w.Writes() {
		t.Logf("write %s stamps %v", wr.Name, wr.Stamps())
	}
	require.NoError(t, w.RequireClean(), "the real apply stamped a resource the kind does not declare (O9)")
	require.NotEmpty(t, w.Writes(), "the apply wrote nothing through the gate")
	stamped := f518bResources(w.Stamped())
	declared := map[string]bool{}
	for _, d := range tasks.CommonOf(r.def).Effects.WriteSet() {
		declared[d.Kind] = true
	}
	for res := range stamped {
		require.True(t, declared[res], "stamped %s is not in the kind's Effects", res)
	}
	f518bRequireDiffStamped(t, r.before, r.after, stamped)
}

// TestF518b_EveryStampedResourceIsLoadBearing: for each resource the two real
// applies stamp, a kind that declared everything BUT that resource is refused
// at the write. The fence is what catches the under-declared Effects: the
// store diff alone saw only what changed, never what was about to.
func TestF518b_EveryStampedResourceIsLoadBearing(t *testing.T) {
	for name, run := range map[string]func(*testing.T, func([]entity.Ref) []entity.Ref) f518bRun{
		"resolve-production-authors": f518bResolveRun,
		"auto-match-transcribed":     f518bAutoMatchRun,
	} {
		t.Run(name, func(t *testing.T) {
			stamped := f518bResources(run(t, nil).w.Stamped())
			require.NotEmpty(t, stamped)
			for res := range stamped {
				t.Run("without "+res, func(t *testing.T) {
					r := run(t, func(set []entity.Ref) []entity.Ref {
						var out []entity.Ref
						for _, d := range set {
							if d.Kind != res {
								out = append(out, d)
							}
						}
						return out
					})
					refused := r.w.Refusals()
					require.NotEmpty(t, refused, "declaring no %s changed nothing: the fence did not see the stamp", res)
					require.ErrorIs(t, refused[0].Err, tasks.ErrOutsideEffects)
					var named bool
					for _, f := range refused {
						named = named || strings.Contains(f.Err.Error(), "books/"+res)
					}
					require.True(t, named, "no refusal names books/%s: %v", res, refused[0].Err)
				})
			}
		})
	}
}

// f518bExtraFamilies are key families the apply writes that the store-diff
// helper has no resource for (kinds_t6_g5_storediff_test.go is another row's
// file): the history rows of RecordMetadataChange (operations, as F5-06 filed
// them), the series rows CreateSeries writes and the global preference rows
// SetUserPreference writes (user_state, as the shared table has
// SetUserPreferenceForUser).
var f518bExtraFamilies = map[string]string{"metadata_change:": "operations", "series:": "series", "preference:": "user_state"}

// f518bChangedResources are the resources the keys changed between two
// snapshots belong to, each with one changed key as evidence.
func f518bChangedResources(t *testing.T, before, after map[string]string) map[string]string {
	t.Helper()
	skip := make([]string, 0, len(f518bExtraFamilies))
	for prefix := range f518bExtraFamilies {
		skip = append(skip, prefix)
	}
	diff := t6g5DiffStore(before, after, skip...)
	require.Empty(t, diff.problems)
	resources := map[string]string{}
	for _, r := range diff.resources {
		resources[r] = diff.sample[r]
	}
	changed := func(k string) bool { old, ok := before[k]; return !ok || old != after[k] }
	for _, snap := range []map[string]string{before, after} {
		for k := range snap {
			for prefix, res := range f518bExtraFamilies {
				if strings.HasPrefix(k, prefix) && changed(k) {
					resources[res] = k
				}
			}
		}
	}
	return resources
}

// f518bRequireDiffStamped requires every resource the keys changed between two
// snapshots belong to to be one a gated write was stamped with.
func f518bRequireDiffStamped(t *testing.T, before, after map[string]string, stamped map[string]bool) {
	t.Helper()
	resources := f518bChangedResources(t, before, after)
	require.NotEmpty(t, resources, "the run changed nothing in the store")
	for r, key := range resources {
		require.True(t, stamped[r], "the store diff shows %s changed but no gated write was stamped with it (sample key %s)", r, key)
	}
}

// f518bRunners is the transcription search and apply of auto-match-transcribed
// over a service: the search answers a fixed candidate and the apply is the
// server's last step, ApplyMetadataCandidateWithOptions with the two-field
// fill-only allowlist (server.ApplyTranscriptionCandidate), without that
// method's read-side guards, which write nothing.
type f518bRunners struct {
	svc  *metafetch.Service
	cand metafetch.MetadataCandidate
}

func (r f518bRunners) SearchTranscriptionCandidate(context.Context, string, string, string) (TranscriptionCandidate, bool, error) {
	return TranscriptionCandidate{Title: r.cand.Title, Author: r.cand.Author, Series: r.cand.Series, Score: 1.8}, true, nil
}

func (r f518bRunners) ApplyTranscriptionCandidate(_ context.Context, bookID, _, _ string) error {
	_, err := r.svc.ApplyMetadataCandidateWithOptions(bookID, r.cand, []string{"title", "author"},
		metafetch.ApplyOptions{FillOnly: true, RefuseEmptyWrite: true})
	return err
}

// TestF518b_AutoMatchTranscribed_RunsThroughTheEffectsEnforcingWriter (O9,
// proves 3): the real metafetch apply of auto-match-transcribed through the
// Effects-enforcing writer, as the resolve kind's test above.
func TestF518b_AutoMatchTranscribed_RunsThroughTheEffectsEnforcingWriter(t *testing.T) {
	f518bRealRun(t, f518bAutoMatchRun, nil)
}

func f518bAutoMatchRun(t *testing.T, declare func([]entity.Ref) []entity.Ref) f518bRun {
	t.Helper()
	s := newRepairPebble(t)
	trans, author := "Year One", "Bruce Wayne"
	_, err := s.CreateBook(&database.Book{ID: "b1", Title: "", FilePath: "/lib/b1.m4b", TranscribedTitle: &trans, TranscribedAuthor: &author})
	require.NoError(t, err)

	p := &t6g1LiveProbe{}
	deps := p.deps(s)
	deps.NewTranscriptionRunners = func(w tasks.Writer) TranscriptionRunners {
		svc := f518bGatedService(t, s, w, nil)
		return f518bRunners{svc: svc, cand: metafetch.MetadataCandidate{
			Title: trans, Author: author, Series: "Gotham Chronicles", Publisher: "Wayne Audio", Source: "f518b-fake",
		}}
	}
	def := t6g1ByKind(t, (&Plugin{}).t6g1Kinds(deps, nil), autoMatchTranscribedKind)
	w := f518bWriter(def, declare)

	before := t6g5Snapshot(t, s)
	_, _, err = runT6G1Full(t, def, tasks.ModeLive, w, `{"min_score":0.75}`, nil)
	require.NoError(t, err)
	got, err := s.GetBookByID("b1")
	require.NoError(t, err)
	if declare == nil {
		require.Equal(t, trans, got.Title, "the apply filled the empty title")
	}
	return f518bRun{def, w, before, t6g5Snapshot(t, s)}
}
