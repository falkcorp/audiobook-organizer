// file: internal/plugins/maintenance/intro_transcribe_work_test.go
// version: 1.0.0
// guid: 7a4c2e9d-1b6f-4d38-9e5a-c0f3b8d21a76
// last-edited: 2026-09-05

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// workFixture: six listed ids with every transcription state the selector
// distinguishes. "f" is listed but the store cannot return it.
func workFixture() (ids []string, store *database.MockStore) {
	books := map[string]*database.Book{
		"a": {ID: "a", IntroTranscription: new("Chapter one. Written by someone.")},
		"b": {ID: "b"},
		"c": {ID: "c", IntroTranscription: new("")},
		"d": {ID: "d", IntroTranscription: new(silenceSentinel)},
		"e": {ID: "e", IntroTranscription: new("Another transcript.")},
	}
	ids = []string{"a", "b", "c", "d", "e", "f"}
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
		{"only_missing", transcribeSelect{onlyMissing: true}, []string{"b", "c"}, 3, 1},
		{"only_missing+retry_silence", transcribeSelect{onlyMissing: true, retrySilence: true}, []string{"b", "c", "d"}, 2, 1},
		{"everything", transcribeSelect{onlyMissing: false}, []string{"a", "b", "c", "d", "e"}, 0, 1},
		{"extract_only ignores only_missing", transcribeSelect{onlyMissing: true, extractOnly: true}, []string{"a", "b", "c", "d", "e"}, 0, 1},
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
			if got.skipped != tc.wantSkip || got.unreadable != tc.wantUnrea {
				t.Errorf("skipped/unreadable = %d/%d, want %d/%d", got.skipped, got.unreadable, tc.wantSkip, tc.wantUnrea)
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

	// Page-level progress: 2 pages (250 work / 200 per page), never 3 (550 / 200).
	if len(rep.totals) < 2 {
		t.Fatalf("only %d progress updates recorded: %v", len(rep.totals), rep.totals)
	}
	for _, tot := range rep.totals[:len(rep.totals)-1] {
		if tot != 2 {
			t.Errorf("progress total %d, want 2 pages of work; all: %v", tot, rep.totals)
		}
	}
	if !strings.Contains(rep.last, "(of 250 total)") {
		t.Errorf("final message %q does not report 250 total", rep.last)
	}
	st := store.last
	if st == nil {
		t.Fatal("stats:transcribe was never flushed")
	}
	if st.TotalBooks != missing || st.SkippedExisting != transcribed || !st.Done {
		t.Errorf("stats total/skipped/done = %d/%d/%v, want %d/%d/true", st.TotalBooks, st.SkippedExisting, st.Done, missing, transcribed)
	}
	if st.Attempted != st.TotalBooks {
		t.Errorf("attempted %d != total %d at the end of a complete run", st.Attempted, st.TotalBooks)
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
