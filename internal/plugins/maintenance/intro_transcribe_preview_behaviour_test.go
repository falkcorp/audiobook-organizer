// file: internal/plugins/maintenance/intro_transcribe_preview_behaviour_test.go
// version: 1.0.0
// guid: f479366b-0525-4f2f-a944-65aa99b39977
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
)

// liveTranscribeParams adds dry_run=false to a params object (empty means {}):
// the op previews unless a run states otherwise, and the tests that drive the
// transcribe run itself want the run.
func liveTranscribeParams(params string) json.RawMessage {
	m := map[string]any{}
	if params != "" {
		if err := json.Unmarshal([]byte(params), &m); err != nil {
			panic(err)
		}
	}
	m["dry_run"] = false
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// lastProgressReporter keeps the last progress message, which is where the op
// puts its summary.
type lastProgressReporter struct {
	fakeReporter
	pmu  sync.Mutex
	last string
}

func (r *lastProgressReporter) UpdateProgress(_, _ int, msg string) error {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	r.last = msg
	return nil
}

func (r *lastProgressReporter) lastMessage() string {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	return r.last
}

// transcribeWriteStore is a MockStore that counts the writes the transcribe op
// makes: book rows (UpdateBook, which ModifyBook's mock fallback ends in) and
// the stats:transcribe key (PutTranscribeStats).
type transcribeWriteStore struct {
	*database.MockStore
	bookWrites  atomic.Int64
	statsWrites atomic.Int64
}

func (s *transcribeWriteStore) PutTranscribeStats(*database.TranscribeStats) error {
	s.statsWrites.Add(1)
	return nil
}

func (s *transcribeWriteStore) GetTranscribeStats() (*database.TranscribeStats, error) {
	return nil, nil
}

func newTranscribeWriteStore(books map[string]*database.Book) *transcribeWriteStore {
	ts := &transcribeWriteStore{}
	ts.MockStore = &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) {
			ids := make([]string, 0, len(books))
			for id := range books {
				ids = append(ids, id)
			}
			return ids, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if b, ok := books[id]; ok {
				c := *b
				return &c, nil
			}
			return nil, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) { return nil, nil },
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			ts.bookWrites.Add(1)
			return b, nil
		},
	}
	return ts
}

// TestPreviewBehaviour_TranscribeBookIntros runs the registered op with the
// params the /api/v3/ops start path would queue for each mode, for the full
// transcribe path and for extract_only. The book's source file is absent, so a
// live run reaches its first writes (clip cache directory, book status row,
// stats:transcribe) without ffmpeg or Whisper. A preview must make none of
// them, must not be refused by the server-decode switch, and must say what it
// would do; a live run must write.
func TestPreviewBehaviour_TranscribeBookIntros(t *testing.T) {
	origCache := config.AppConfig.WhisperClipCacheDir
	config.AppConfig.WhisperClipCacheDir = ""
	t.Cleanup(func() { config.AppConfig.WhisperClipCacheDir = origCache })

	for _, extra := range []string{``, `,"extract_only":true`} {
		for _, tc := range []struct {
			mode      string
			wantWrite bool
		}{
			{"preview", false},
			{"live", true},
		} {
			t.Run(tc.mode+extra, func(t *testing.T) {
				root := t.TempDir()
				store := newTranscribeWriteStore(map[string]*database.Book{
					"book-1": {ID: "book-1", Title: "Example Book", FilePath: filepath.Join(root, "missing", "example.mp3")},
					"book-2": {ID: "book-2", Title: "Another Book", FilePath: filepath.Join(root, "missing", "another.mp3")},
				})
				p := New(fakeDeps{store: store, root: root})
				def := p.introTranscribeDef()

				// Decoding is off: only a live run may be refused for it.
				t.Setenv(serverdecode.EnvVar, "")
				if tc.wantWrite {
					t.Setenv(serverdecode.EnvVar, "1")
				}

				params, err := def.Preview.Resolve(json.RawMessage("{"+strings.TrimPrefix(extra, ",")+"}"), tc.mode)
				if err != nil {
					t.Fatalf("Resolve(%s): %v", tc.mode, err)
				}
				rep := &lastProgressReporter{}
				if err := def.Run(context.Background(), params, rep); err != nil {
					t.Fatalf("Run(%s) with %s: %v", tc.mode, params, err)
				}

				cacheDir := wavCacheDir(root)
				_, statErr := os.Stat(cacheDir)
				cacheMade := statErr == nil
				if !tc.wantWrite {
					if n := store.bookWrites.Load(); n != 0 {
						t.Fatalf("preview wrote %d book rows", n)
					}
					if n := store.statsWrites.Load(); n != 0 {
						t.Fatalf("preview wrote the stats:transcribe key %d times", n)
					}
					if cacheMade {
						t.Fatalf("preview created the clip cache directory %s", cacheDir)
					}
					if last := rep.lastMessage(); !strings.Contains(last, "PREVIEW") || !strings.Contains(last, "would") || !strings.Contains(last, " 2 books") {
						t.Fatalf("preview did not report its selection: %q", last)
					}
					return
				}
				if store.bookWrites.Load() < 1 {
					t.Fatalf("live run wrote no book rows (params %s)", params)
				}
				if store.statsWrites.Load() < 1 {
					t.Fatalf("live run never wrote the stats:transcribe key (params %s)", params)
				}
				if !cacheMade {
					t.Fatalf("live run never created the clip cache directory %s", cacheDir)
				}
			})
		}
	}
}

// A preview is not refused when decoding is off, and a live run still is: the
// switch guards the ffmpeg path, which a preview never takes.
func TestIntroTranscribe_PreviewSkipsServerDecodeCheck(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	store := newTranscribeWriteStore(map[string]*database.Book{"book-1": {ID: "book-1"}})
	p := New(fakeDeps{store: store, root: t.TempDir()})
	if err := p.runIntroTranscribe(context.Background(), json.RawMessage(`{}`), &fakeReporter{}); err != nil {
		t.Fatalf("a preview must not be refused by the decode switch: %v", err)
	}
	err := p.runIntroTranscribe(context.Background(), json.RawMessage(`{"dry_run":false}`), &fakeReporter{})
	if !errors.Is(err, serverdecode.ErrRefused) {
		t.Fatalf("a live run must still be refused with decoding off, got %v", err)
	}
}

// The checkpoint a run writes must not carry dry_run: ResumeRestart merges the
// checkpoint OVER the run's original params, so a dry_run key in it would
// override the live run's dry_run=false on resume. Left out (nil, omitempty),
// the original dry_run=false survives and a resumed live run stays live.
func TestIntroTranscribe_CheckpointLeavesDryRunAlone(t *testing.T) {
	only := true
	raw, err := json.Marshal(introTranscribeParams{LastBookID: "book-1", OnlyMissing: &only})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["dry_run"]; has {
		t.Fatalf("checkpoint carries dry_run and would override the resumed run's mode: %s", raw)
	}
}
