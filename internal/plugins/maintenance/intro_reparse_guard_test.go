// file: internal/plugins/maintenance/intro_reparse_guard_test.go
// version: 1.1.0
// guid: 8f2b6d41-7e05-4c39-b8a7-1d94e30c5f26
// last-edited: 2026-08-07

package maintenance

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// reparseStore is a minimal store satisfying the narrow interface
// reparseStoredIntros accepts, recording every write it receives.
type reparseStore struct {
	books   map[string]*database.Book
	order   []string
	updates map[string]*database.Book
}

func newReparseStore(books ...*database.Book) *reparseStore {
	s := &reparseStore{
		books:   map[string]*database.Book{},
		updates: map[string]*database.Book{},
	}
	for _, b := range books {
		s.books[b.ID] = b
		s.order = append(s.order, b.ID)
	}
	return s
}

func (s *reparseStore) ListBookIDs() ([]string, error) { return s.order, nil }

func (s *reparseStore) GetBookByID(id string) (*database.Book, error) {
	b, ok := s.books[id]
	if !ok {
		return nil, nil
	}
	cp := *b // hand back a copy, as the real store does
	return &cp, nil
}

func (s *reparseStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	cp := *b
	s.updates[id] = &cp
	s.books[id] = &cp
	return &cp, nil
}

func sp(s string) *string { return &s }

// TestReparseNeverClearsAnUnreproducibleParse is the data-loss regression test
// for the reparse guard.
//
// The hazard is real and was measured on production: applyOutcome overwrites
// IntroTranscription unconditionally but writes parsed fields only when a title
// was extracted, so a later WORSE transcription replaces good text while the
// good parse survives beside it. 1.4% of 987 sampled books (~644 library-wide)
// are in that state. Re-running the parser over them must not erase the parse.
func TestReparseNeverClearsAnUnreproducibleParse(t *testing.T) {
	// Verbatim production shape: transcript degraded to the Audible jingle, but
	// the parse from the earlier, better transcript is still stored.
	degraded := &database.Book{
		ID:                  "book-degraded",
		IntroTranscription:  sp("This is Audible."),
		TranscribedTitle:    sp("Wind and Truth"),
		TranscribedAuthor:   sp("Brandon Sanderson"),
		TranscribedNarrator: sp("Kate Reading and Michael Kramer"),
	}
	// Narrative prose that merely contains the word "by" — the old parser turned
	// this into a title/author pair; the new one must classify it prose and, on
	// a reparse, leave the stored fields untouched rather than clearing them.
	prose := &database.Book{
		ID:                 "book-prose",
		IntroTranscription: sp("The morning dragged on and he wasn't mildly amused by Memphis fortunes, nor the talk that followed him home."),
		TranscribedTitle:   sp("Some Earlier Title"),
		TranscribedAuthor:  sp("Some Earlier Author"),
	}

	store := newReparseStore(degraded, prose)
	p := &Plugin{}
	if err := p.reparseStoredIntros(context.Background(), store, &fakeReporter{}, store.order, false); err != nil {
		t.Fatalf("reparseStoredIntros: %v", err)
	}

	for _, id := range []string{"book-degraded", "book-prose"} {
		if _, written := store.updates[id]; written {
			got := store.books[id]
			t.Errorf("%s was WRITTEN on a non-credits verdict — reparse must only upgrade.\n"+
				"  title=%v author=%v", id, deref(got.TranscribedTitle), deref(got.TranscribedAuthor))
		}
	}
	// And the values must still be intact.
	if got := deref(store.books["book-degraded"].TranscribedAuthor); got != "Brandon Sanderson" {
		t.Errorf("degraded book lost its author: %q", got)
	}
	if got := deref(store.books["book-prose"].TranscribedTitle); got != "Some Earlier Title" {
		t.Errorf("prose book lost its title: %q", got)
	}
}

// TestReparseStillUpgradesACorrectableParse is the other half of the contract:
// the guard must not freeze genuinely improvable rows. This is the 24.8%
// library-wide leaked-verb defect being corrected.
func TestReparseStillUpgradesACorrectableParse(t *testing.T) {
	stale := &database.Book{
		ID:                 "book-leaked-verb",
		IntroTranscription: sp("Awakened Essence 1 Written by Jacob Poole Performed by Alex Perrone"),
		TranscribedTitle:   sp("Awakened Essence 1 Written"), // the leaked verb
		TranscribedAuthor:  sp("Jacob Poole"),
	}
	store := newReparseStore(stale)
	p := &Plugin{}
	if err := p.reparseStoredIntros(context.Background(), store, &fakeReporter{}, store.order, false); err != nil {
		t.Fatalf("reparseStoredIntros: %v", err)
	}

	got, written := store.updates["book-leaked-verb"]
	if !written {
		t.Fatal("a correctable parse was not upgraded — the guard is too broad")
	}
	if deref(got.TranscribedTitle) != "Awakened Essence 1" {
		t.Errorf("Title = %q, want %q", deref(got.TranscribedTitle), "Awakened Essence 1")
	}
	if deref(got.TranscribedNarrator) != "Alex Perrone" {
		t.Errorf("Narrator = %q, want %q", deref(got.TranscribedNarrator), "Alex Perrone")
	}
}

// TestReparseKeepsExistingNarratorWhenNewParseHasNone guards the one field a
// credits verdict does not guarantee.
func TestReparseKeepsExistingNarratorWhenNewParseHasNone(t *testing.T) {
	b := &database.Book{
		ID:                  "book-no-narrator",
		IntroTranscription:  sp("On Writing by Stephen King No one writes a long novel alone"),
		TranscribedTitle:    sp("On Writing Old"),
		TranscribedAuthor:   sp("Stephen King"),
		TranscribedNarrator: sp("Christopher Hurt"), // from a richer earlier transcript
	}
	store := newReparseStore(b)
	p := &Plugin{}
	if err := p.reparseStoredIntros(context.Background(), store, &fakeReporter{}, store.order, false); err != nil {
		t.Fatalf("reparseStoredIntros: %v", err)
	}
	if got := deref(store.books["book-no-narrator"].TranscribedNarrator); got != "Christopher Hurt" {
		t.Errorf("narrator cleared by a parse that had none: %q", got)
	}
}

// TestSilenceSentinelIsSharedWithClassifier pins the constant alias: two
// packages disagreeing about this literal would turn "known silent" into
// "unparsed prose" library-wide.
func TestSilenceSentinelIsSharedWithClassifier(t *testing.T) {
	if silenceSentinel != transcribe.SilenceSentinel {
		t.Fatalf("sentinel drift: maintenance=%q transcribe=%q", silenceSentinel, transcribe.SilenceSentinel)
	}
	if c := transcribe.ClassifyIntro(silenceSentinel, transcribe.UnknownPosition); c.Kind != transcribe.IntroKindUnknown {
		t.Errorf("sentinel classified %q, want unknown", c.Kind)
	}
}

// progressReporter captures the last progress message so dry-run reporting can
// be asserted on.
type progressReporter struct {
	fakeReporter
	lastMsg string
}

func (r *progressReporter) UpdateProgress(_, _ int, msg string) error {
	r.lastMsg = msg
	return nil
}

// TestReparseDryRunWritesNothingButReportsWouldUpdate pins the dry_run
// contract: identical classification+comparison logic runs, the would-update
// count is reported, and ZERO UpdateBook calls are made. A second pass with
// dryRun=false over the same fixture confirms the write path still writes.
func TestReparseDryRunWritesNothingButReportsWouldUpdate(t *testing.T) {
	// A correctable stored parse (the leaked-verb defect): a fresh parse of the
	// stored transcript differs from the stored fields, so a real run would
	// rewrite them.
	fixture := func() *database.Book {
		return &database.Book{
			ID:                 "book-dry-run",
			IntroTranscription: sp("Awakened Essence 1 Written by Jacob Poole Performed by Alex Perrone"),
			TranscribedTitle:   sp("Awakened Essence 1 Written"),
			TranscribedAuthor:  sp("Jacob Poole"),
		}
	}

	// Dry run: nonzero would-update reported, nothing written.
	store := newReparseStore(fixture())
	rep := &progressReporter{}
	p := &Plugin{}
	if err := p.reparseStoredIntros(context.Background(), store, rep, store.order, true); err != nil {
		t.Fatalf("reparseStoredIntros(dryRun=true): %v", err)
	}
	if n := len(store.updates); n != 0 {
		t.Fatalf("dry run made %d UpdateBook calls, want 0", n)
	}
	if got := deref(store.books["book-dry-run"].TranscribedTitle); got != "Awakened Essence 1 Written" {
		t.Errorf("dry run mutated stored title: %q", got)
	}
	if !strings.Contains(rep.lastMsg, "would update 1 of 1") {
		t.Errorf("final progress = %q, want it to contain %q", rep.lastMsg, "would update 1 of 1")
	}

	// Same fixture, dryRun=false: the write happens and is reported as applied.
	store2 := newReparseStore(fixture())
	rep2 := &progressReporter{}
	if err := p.reparseStoredIntros(context.Background(), store2, rep2, store2.order, false); err != nil {
		t.Fatalf("reparseStoredIntros(dryRun=false): %v", err)
	}
	if n := len(store2.updates); n != 1 {
		t.Fatalf("real run made %d UpdateBook calls, want 1", n)
	}
	if got := deref(store2.books["book-dry-run"].TranscribedTitle); got != "Awakened Essence 1" {
		t.Errorf("real run title = %q, want %q", got, "Awakened Essence 1")
	}
	if !strings.Contains(rep2.lastMsg, "updated 1 of 1") {
		t.Errorf("final progress = %q, want it to contain %q", rep2.lastMsg, "updated 1 of 1")
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
