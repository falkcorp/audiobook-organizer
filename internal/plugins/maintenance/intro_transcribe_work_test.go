// file: internal/plugins/maintenance/intro_transcribe_work_test.go
// version: 1.1.0
// guid: 7a4c2e9d-1b6f-4d38-9e5a-c0f3b8d21a76
// last-edited: 2026-09-05

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// workFixture: seven listed ids with every transcription state the selector
// distinguishes. "f" is listed but the read fails; "g" is listed but has no
// row behind it — the production not-found shape is (nil, nil), not an error.
func workFixture() (ids []string, store *database.MockStore) {
	books := map[string]*database.Book{
		"a": {ID: "a", IntroTranscription: new("Chapter one. Written by someone.")},
		"b": {ID: "b"},
		"c": {ID: "c", IntroTranscription: new("")},
		"d": {ID: "d", IntroTranscription: new(silenceSentinel)},
		"e": {ID: "e", IntroTranscription: new("Another transcript.")},
	}
	ids = []string{"a", "b", "c", "d", "e", "f", "g"}
	store = &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) { return ids, nil },
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id == "f" {
				return nil, errors.New("row f: decode failed")
			}
			return books[id], nil
		},
	}
	return ids, store
}

func TestSelectTranscribeWork_PolicyAndOrder(t *testing.T) {
	ids, store := workFixture()
	cases := []struct {
		name      string
		sel       transcribeSelect
		wantWork  []string
		wantSkip  int
		wantUnrea int
	}{
		{"only_missing", transcribeSelect{onlyMissing: true}, []string{"b", "c"}, 3, 2},
		{"only_missing+retry_silence", transcribeSelect{onlyMissing: true, retrySilence: true}, []string{"b", "c", "d"}, 2, 2},
		{"everything", transcribeSelect{onlyMissing: false}, []string{"a", "b", "c", "d", "e"}, 0, 2},
		{"extract_only ignores only_missing", transcribeSelect{onlyMissing: true, extractOnly: true}, []string{"a", "b", "c", "d", "e"}, 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectTranscribeWork(context.Background(), store, ids, tc.sel)
			if err != nil {
				t.Fatalf("selectTranscribeWork: %v", err)
			}
			if strings.Join(got.work, ",") != strings.Join(tc.wantWork, ",") {
				t.Errorf("work = %v, want %v", got.work, tc.wantWork)
			}
			if got.skipped != tc.wantSkip || got.unreadable() != tc.wantUnrea {
				t.Errorf("skipped/unreadable = %d/%d, want %d/%d", got.skipped, got.unreadable(), tc.wantSkip, tc.wantUnrea)
			}
			if got.readErrors != 1 || got.notFound != 1 {
				t.Errorf("readErrors/notFound = %d/%d, want 1/1 (f errors, g is absent)", got.readErrors, got.notFound)
			}
			if len(got.samples) != 2 || !strings.HasPrefix(got.samples[0], "f: row f") || got.samples[1] != "g: not found" {
				t.Errorf("samples = %q", got.samples)
			}
		})
	}
}

// Order survives the worker pool: 1,000 ids, every one work, must come back in
// list order — the checkpoint (last id seen) and resume depend on it.
func TestSelectTranscribeWork_PreservesListOrderUnderConcurrency(t *testing.T) {
	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = fmt.Sprintf("book-%04d", i)
	}
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) { return &database.Book{ID: id}, nil },
	}
	got, err := selectTranscribeWork(context.Background(), store, ids, transcribeSelect{onlyMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.work) != len(ids) {
		t.Fatalf("work has %d ids, want %d", len(got.work), len(ids))
	}
	for i, id := range got.work {
		if id != ids[i] {
			t.Fatalf("work[%d] = %s, want %s", i, id, ids[i])
		}
	}
}

func TestSelectTranscribeWork_CancelledContextIsAnError(t *testing.T) {
	ids, store := workFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := selectTranscribeWork(ctx, store, ids, transcribeSelect{onlyMissing: true}); err == nil {
		t.Fatal("cancelled selection returned nil error")
	}
}

// denomReporter records every progress update's total.
type denomReporter struct {
	fakeReporter
	mu     sync.Mutex
	totals []int
	last   string
}

func (r *denomReporter) UpdateProgress(_, total int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totals = append(r.totals, total)
	r.last = msg
	return nil
}

// statsStore is a MockStore that also receives the stats:transcribe flushes.
type statsStore struct {
	*database.MockStore
	mu   sync.Mutex
	last *database.TranscribeStats
}

func (s *statsStore) PutTranscribeStats(st *database.TranscribeStats) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *st
	s.last = &c
	return nil
}

func (s *statsStore) GetTranscribeStats() (*database.TranscribeStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, nil
}

type rootDeps struct {
	fakeDeps
	root string
}

func (d rootDeps) RootDir() string { return d.root }

// The run's denominator is the books it will attempt, not the library. 550
// listed books, 300 already transcribed: the page count (RunItems' progress
// total) is 2 pages of 200 for the 250 work books rather than 3 for the
// library, the stats:transcribe aggregate says 250 with 300 skipped, and only
// the 250 work books are written.
func TestRunIntroTranscribe_DenominatorIsBooksNeedingWork(t *testing.T) {
	const transcribed, missing = 300, 250
	ids := make([]string, 0, transcribed+missing)
	books := map[string]*database.Book{}
	for i := range transcribed + missing {
		id := fmt.Sprintf("book-%03d", i)
		ids = append(ids, id)
		b := &database.Book{ID: id}
		if i%2 == 0 && len(books)-i/2 < transcribed { // interleave: evens transcribed until the quota
			b.IntroTranscription = new("Chapter one.")
		}
		books[id] = b
	}
	// Top up so exactly `transcribed` books carry a transcript.
	have := 0
	for _, id := range ids {
		if books[id].IntroTranscription != nil {
			have++
		}
	}
	for _, id := range ids {
		if have >= transcribed {
			break
		}
		if books[id].IntroTranscription == nil {
			books[id].IntroTranscription = new("Chapter one.")
			have++
		}
	}
	if have != transcribed {
		t.Fatalf("fixture has %d transcribed books, want %d", have, transcribed)
	}
	var writes []string
	var wmu sync.Mutex
	mock := &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) { return ids, nil },
		GetBookByIDFunc: func(id string) (*database.Book, error) { return books[id], nil },
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			wmu.Lock()
			defer wmu.Unlock()
			writes = append(writes, id)
			return b, nil
		},
	}
	store := &statsStore{MockStore: mock}
	p := &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}}
	rep := &denomReporter{}

	if err := p.runIntroTranscribe(context.Background(), []byte(`{}`), rep); err != nil {
		t.Fatalf("runIntroTranscribe: %v", err)
	}

	// Page-level progress reports 2 pages of work and never the 3 the library
	// would make. (The per-book lines report 250; none fire here because the
	// fixture has no audio, so the assertion is on presence, not on every line.)
	if !slices.Contains(rep.totals, 2) || slices.Contains(rep.totals, 3) || slices.Contains(rep.totals, transcribed+missing) {
		t.Errorf("progress totals %v: want 2 present, never 3 or %d", rep.totals, transcribed+missing)
	}
	if !strings.Contains(rep.last, "(of 250 total;") {
		t.Errorf("final message %q does not report 250 total", rep.last)
	}
	st := store.last
	if st == nil {
		t.Fatal("stats:transcribe was never flushed")
	}
	if st.TotalBooks != missing || st.SkippedExisting != transcribed || !st.Done {
		t.Errorf("stats total/skipped/done = %d/%d/%v, want %d/%d/true", st.TotalBooks, st.SkippedExisting, st.Done, missing, transcribed)
	}
	if st.Attempted+st.Deferred != st.TotalBooks || st.Deferred != 0 {
		t.Errorf("attempted %d + deferred %d != total %d at the end of a complete run", st.Attempted, st.Deferred, st.TotalBooks)
	}
	wmu.Lock()
	defer wmu.Unlock()
	if len(writes) != missing {
		t.Errorf("%d books written, want only the %d work books", len(writes), missing)
	}
	for _, id := range writes {
		if books[id].IntroTranscription != nil {
			t.Errorf("already-transcribed %s was attempted", id)
		}
	}
}

// A run with nothing to do still publishes its skip count and marks itself
// done, so a monitor reading stats:transcribe never sees a run that vanished.
func TestRunIntroTranscribe_NothingToDoStillPublishesStats(t *testing.T) {
	mock := &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) { return []string{"a"}, nil },
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, IntroTranscription: new("done")}, nil
		},
	}
	store := &statsStore{MockStore: mock}
	p := &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}}
	rep := &denomReporter{}
	if err := p.runIntroTranscribe(context.Background(), nil, rep); err != nil {
		t.Fatal(err)
	}
	if store.last == nil || !store.last.Done || store.last.SkippedExisting != 1 || store.last.TotalBooks != 0 {
		t.Fatalf("stats after empty run = %+v, want done, skipped 1, total 0", store.last)
	}
	if !strings.Contains(rep.last, "nothing to transcribe") {
		t.Errorf("final message %q", rep.last)
	}
}

// A store that fails EVERY read must not turn into a successful "nothing to
// transcribe" run: the run errors and the aggregate is never marked done.
func TestRunIntroTranscribe_TotalStoreFailureIsAnError(t *testing.T) {
	mock := &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) { return []string{"a", "b", "c"}, nil },
		GetBookByIDFunc: func(string) (*database.Book, error) { return nil, errors.New("pebble: closed") },
	}
	store := &statsStore{MockStore: mock}
	p := &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}}
	err := p.runIntroTranscribe(context.Background(), nil, &denomReporter{})
	if err == nil || !strings.Contains(err.Error(), "pebble: closed") {
		t.Fatalf("err = %v, want the store failure", err)
	}
	if store.last != nil && store.last.Done {
		t.Fatalf("aggregate marked done after a total store failure: %+v", store.last)
	}
}

// Some reads failing is not a store outage: the run proceeds over what it
// could read and the aggregate carries the unreadable count so the gap
// between library size and total+skipped is explained where the monitor looks.
func TestRunIntroTranscribe_PartialUnreadableIsPersisted(t *testing.T) {
	ids, mock := workFixture()
	mock.UpdateBookFunc = func(_ string, b *database.Book) (*database.Book, error) { return b, nil }
	store := &statsStore{MockStore: mock}
	p := &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}}
	if err := p.runIntroTranscribe(context.Background(), nil, &denomReporter{}); err != nil {
		t.Fatal(err)
	}
	st := store.last
	if st == nil || st.Unreadable != 2 || st.TotalBooks != 2 || st.SkippedExisting != 3 {
		t.Fatalf("stats = %+v, want unreadable 2, total 2, skipped 3 over %d listed", st, len(ids))
	}
}

// Resume: the checkpoint is found in the FULL list, and the work is selected
// from the books after it. Seven books t1 w1 t2 w2 w3 t3 w4 (t = transcribed,
// w = missing), checkpoint t2: exactly w2 w3 w4 are attempted in that order,
// total 3, skipped 1 (t3 — books before the checkpoint are not counted).
func TestRunIntroTranscribe_ResumeSelectsAfterCheckpointInFullList(t *testing.T) {
	ids := []string{"t1", "w1", "t2", "w2", "w3", "t3", "w4"}
	newStore := func() (*statsStore, *[]string) {
		writes := &[]string{}
		var mu sync.Mutex
		mock := &database.MockStore{
			ListBookIDsFunc: func() ([]string, error) { return ids, nil },
			GetBookByIDFunc: func(id string) (*database.Book, error) {
				b := &database.Book{ID: id}
				if strings.HasPrefix(id, "t") {
					b.IntroTranscription = new("done")
				}
				return b, nil
			},
			UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
				mu.Lock()
				defer mu.Unlock()
				*writes = append(*writes, id)
				return b, nil
			},
		}
		return &statsStore{MockStore: mock}, writes
	}
	run := func(t *testing.T, params string) (*statsStore, []string, string) {
		t.Helper()
		store, writes := newStore()
		p := &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}}
		rep := &denomReporter{}
		if err := p.runIntroTranscribe(context.Background(), []byte(params), rep); err != nil {
			t.Fatal(err)
		}
		return store, *writes, rep.last
	}

	store, writes, _ := run(t, `{"last_book_id":"t2"}`)
	if strings.Join(writes, ",") != "w2,w3,w4" {
		t.Errorf("attempted %v, want w2,w3,w4", writes)
	}
	if store.last.TotalBooks != 3 || store.last.SkippedExisting != 1 {
		t.Errorf("total/skipped = %d/%d, want 3/1", store.last.TotalBooks, store.last.SkippedExisting)
	}

	store, writes, last := run(t, `{"last_book_id":"w4"}`)
	if len(writes) != 0 || store.last.TotalBooks != 0 || !strings.Contains(last, "nothing to transcribe") {
		t.Errorf("checkpoint at the final book: writes %v, total %d, msg %q", writes, store.last.TotalBooks, last)
	}

	store, writes, _ = run(t, `{"last_book_id":"gone"}`)
	if strings.Join(writes, ",") != "w1,w2,w3,w4" || store.last.TotalBooks != 4 {
		t.Errorf("unknown checkpoint: writes %v total %d, want a full run of 4", writes, store.last.TotalBooks)
	}
}

// Between selection and page time a book can change hands: one becomes
// unreadable (deferred, so attempted+deferred still equals total) and one
// gains a transcript (skipped, not attempted).
func TestRunIntroTranscribe_PageTimeChangesAreCounted(t *testing.T) {
	ids := []string{"stale", "done-meanwhile", "fine"}
	reads := map[string]int{}
	var mu sync.Mutex
	mock := &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) { return ids, nil },
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			mu.Lock()
			reads[id]++
			n := reads[id]
			mu.Unlock()
			if n == 1 { // selection: everything is readable and missing a transcript
				return &database.Book{ID: id}, nil
			}
			switch id {
			case "stale":
				return nil, errors.New("row vanished")
			case "done-meanwhile":
				return &database.Book{ID: id, IntroTranscription: new("someone else did it")}, nil
			}
			return &database.Book{ID: id}, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) { return b, nil },
	}
	store := &statsStore{MockStore: mock}
	p := &Plugin{deps: rootDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}}
	if err := p.runIntroTranscribe(context.Background(), nil, &denomReporter{}); err != nil {
		t.Fatal(err)
	}
	st := store.last
	if st.TotalBooks != 3 || st.Deferred != 1 || st.SkippedExisting != 1 || st.Attempted != 1 {
		t.Fatalf("stats = total %d deferred %d skipped %d attempted %d, want 3/1/1/1", st.TotalBooks, st.Deferred, st.SkippedExisting, st.Attempted)
	}
}
