// file: internal/plugins/deluge/kinds_env_test.go
// version: 1.0.1
// guid: c27a6e90-4b18-4d53-a6f1-0e9d83b52c47
// last-edited: 2026-11-19

package deluge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// itemsRun runs the same n-item loop on both pools and returns what each
// reported: the error, the items that ran, and the label arguments seen.
type itemsRun struct {
	err    string
	ran    []int
	labels map[[2]int]bool
	// bars is every (done, total) the pool reported (HEAD's UpdateProgress, the
	// native ItemsProgress), sorted.
	bars [][2]int
}

func runItemsOn(t *testing.T, pool string, n int, o kindItemsOptions, fail map[int]bool) itemsRun {
	t.Helper()
	var mu sync.Mutex
	out := itemsRun{labels: map[[2]int]bool{}}
	userLabel := o.PureLabel
	o.PureLabel = func(i, total int) string {
		mu.Lock()
		out.labels[[2]int{i, total}] = true
		mu.Unlock()
		if userLabel != nil {
			return userLabel(i, total)
		}
		return fmt.Sprintf("item %d/%d", i, total)
	}
	fn := func(_ context.Context, i int) error {
		mu.Lock()
		out.ran = append(out.ran, i)
		mu.Unlock()
		if fail[i] {
			return fmt.Errorf("item %d failed", i)
		}
		return nil
	}
	var err error
	switch pool {
	case "head":
		rep := &parityReporter{}
		err = headItems(rep)(context.Background(), tasks.Sequential("test"), n, o, fn)
		for _, f := range rep.frameList() {
			out.bars = append(out.bars, [2]int{f.Current, f.Total})
		}
	case "native":
		def := byKind(t, (&Plugin{}).Kinds(KindDeps{}, nil), protectedPathsSyncKind)
		_ = def
		h := newKindHost(context.Background(), def, tasks.ModeLive, nil)
		err = nativeItems(tasks.NewRC(h))(context.Background(), tasks.Sequential("test"), n, o, fn)
		for _, e := range h.FakeRun.Progress() {
			out.bars = append(out.bars, [2]int{int(e.Done), int(e.Total)})
		}
	default:
		t.Fatalf("unknown pool %s", pool)
	}
	out.err = errText(err)
	sort.Ints(out.ran)
	sort.Slice(out.bars, func(i, j int) bool {
		if out.bars[i][0] != out.bars[j][0] {
			return out.bars[i][0] < out.bars[j][0]
		}
		return out.bars[i][1] < out.bars[j][1]
	})
	return out
}

// Both pools hand the core the item's own error (fail-fast: the first one; collect:
// all of them joined), run every item in collect mode, and label with the same
// total (the absolute ProgressTotal when set, else n).
func TestDelugeEnv_ItemsPoolsAgree(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		o    kindItemsOptions
		fail map[int]bool
	}{
		{name: "all succeed", n: 4},
		{name: "collect runs everything and joins the errors", n: 4, o: kindItemsOptions{Collect: true}, fail: map[int]bool{1: true, 3: true}},
		{name: "fail-fast stops at the first error", n: 4, fail: map[int]bool{1: true}},
		{name: "an absolute progress total labels with it", n: 3, o: kindItemsOptions{ProgressTotal: 5}},
		{name: "a slice of a larger set reports against the set", n: 3, o: kindItemsOptions{ProgressBase: 10, ProgressTotal: 20}},
		{name: "no items", n: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := runItemsOn(t, "head", tc.n, tc.o, tc.fail)
			nat := runItemsOn(t, "native", tc.n, tc.o, tc.fail)
			require.Equal(t, head.err, nat.err, "error")
			require.Equal(t, head.ran, nat.ran, "items run")
			require.Equal(t, head.labels, nat.labels, "label arguments")
			require.Equal(t, head.bars, nat.bars, "bar frames (done, total)")
			if tc.o.ProgressTotal > 0 && tc.n > 0 {
				last := nat.bars[len(nat.bars)-1]
				require.Equal(t, tc.o.ProgressTotal, last[1], "the bar's denominator is the absolute total")
				require.Equal(t, tc.o.ProgressBase+tc.n, last[0], "the bar's last position is the base plus the items done")
			}
			wantTotal := tc.n
			if tc.o.ProgressTotal > 0 {
				wantTotal = tc.o.ProgressTotal
			}
			for k := range nat.labels {
				require.Equal(t, wantTotal, k[1], "label total")
			}
			if tc.o.Collect {
				for i := range tc.fail {
					require.Contains(t, nat.err, fmt.Sprintf("item %d failed", i))
				}
			}
		})
	}
}

// The native pool runs every item of a Fixed pool on up to that many workers and
// declares that count to the host's cap (rule R4).
func TestDelugeEnv_NativeItemsDeclaresParallelism(t *testing.T) {
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{}, nil), protectedPathsSyncKind)
	h := newKindHost(context.Background(), def, tasks.ModeLive, nil)
	var declared []int
	h.capObserver = func(d int) { declared = append(declared, d) }
	var ran atomic.Int64
	err := nativeItems(tasks.NewRC(h))(context.Background(), tasks.Fixed(3, "test pool"), 9, kindItemsOptions{},
		func(context.Context, int) error { ran.Add(1); return nil })
	require.NoError(t, err)
	require.Equal(t, int64(9), ran.Load())
	require.Equal(t, []int{3}, declared)
}

func TestDelugeEnv_NativeEnvLiveAndProgress(t *testing.T) {
	def := byKind(t, (&Plugin{}).Kinds(KindDeps{}, nil), protectedPathsSyncKind)
	for _, tc := range []struct {
		mode tasks.Mode
		live bool
	}{{tasks.ModeLive, true}, {tasks.ModePreview, false}} {
		h := newKindHost(context.Background(), def, tc.mode, nil)
		env := nativeEnv(tasks.NewRC(h))
		require.Equal(t, tc.live, env.Live)
	}

	h := newKindHost(context.Background(), def, tasks.ModeLive, nil)
	env := nativeEnv(tasks.NewRC(h))
	env.Progress(2, 5, "two of five")
	env.Progress(3, 0, "") // unknown total, no note
	env.Progress(4, -1, "x")
	require.Equal(t, []barFrame{{2, 5, true}, {3, 0, false}, {4, 0, false}}, h.bars())
	require.Equal(t, []string{"two of five", "x"}, h.noteList())
	require.Nil(t, env.SetResult, "a native run has no SetResult hook")
	env.Touch()
	require.Equal(t, int64(1), h.touches.Load())
	env.Log(slog.LevelWarn, "careful")
	require.Contains(t, h.logged(), "careful")
	require.False(t, env.Canceled())
}

func TestDelugeEnv_ReporterSink(t *testing.T) {
	var got []string
	var touched, ckpt int
	canceled := false
	s := reporterSink{
		progress:   func(c, tot int, msg string) { got = append(got, fmt.Sprintf("%d/%d %s", c, tot, msg)) },
		touch:      func() { touched++ },
		canceled:   func() bool { return canceled },
		checkpoint: func(any) error { ckpt++; return nil },
	}
	require.NoError(t, s.UpdateProgress(1, 2, "half"))
	require.Equal(t, []string{"1/2 half"}, got)
	s.TouchLiveness()
	require.Equal(t, 1, touched)
	require.NoError(t, s.Checkpoint(struct{}{}))
	require.Equal(t, 1, ckpt)
	require.False(t, s.IsCanceled())
	canceled = true
	require.True(t, s.IsCanceled())
	require.NotNil(t, s.Logger(), "a sink without a logger discards, never nil")
	require.NoError(t, s.Log(slog.LevelInfo, "msg"))
	s.SetCurrentItem("anything")
	require.Error(t, s.Trigger(context.Background(), "event", nil))

	var ran bool
	require.NoError(t, s.RunPhase(context.Background(), "p", func(_ context.Context, r sdk.Reporter) error {
		ran = true
		require.IsType(t, reporterSink{}, r)
		return nil
	}))
	require.True(t, ran)

	// The zero sink: every hook is a no-op or a refusal, never a nil call.
	var z reporterSink
	require.NoError(t, z.UpdateProgress(1, 1, ""))
	z.TouchLiveness()
	require.False(t, z.IsCanceled())
	require.Error(t, z.Checkpoint(1))
	require.NotNil(t, z.Logger())
}

// The retired-row reads of HEAD's store adapter resolve through the store's
// capability, and fail closed with the sentinel when no layer has it.
func TestDelugeEnv_HeadKindStoreRetiredFallback(t *testing.T) {
	absent := headKindStore{s: noRetiredStore{&database.MockStore{}}}
	calls := map[string]func() error{
		"GetBookFilesIncludingRetired":        func() error { _, err := absent.GetBookFilesIncludingRetired("b"); return err },
		"GetRetiredBookFiles":                 func() error { _, err := absent.GetRetiredBookFiles("b"); return err },
		"GetAllBookFilesCoreIncludingRetired": func() error { _, err := absent.GetAllBookFilesCoreIncludingRetired(); return err },
		"GetRetiredVersionsFromBook":          func() error { _, err := absent.GetRetiredVersionsFromBook("b"); return err },
		"GetBookFileByPIDIncludingRetired":    func() error { _, err := absent.GetBookFileByPIDIncludingRetired("pid"); return err },
		"GetBookFileByPathIncludingRetired":   func() error { _, err := absent.GetBookFileByPathIncludingRetired("/p"); return err },
	}
	for name, call := range calls {
		t.Run("absent/"+name, func(t *testing.T) {
			require.ErrorIs(t, call(), database.ErrRetiredReaderUnavailable)
		})
	}
	present := headKindStore{s: &database.MockStore{}}
	_, err := present.GetBookFileByPathIncludingRetired("/p")
	require.NotErrorIs(t, err, database.ErrRetiredReaderUnavailable, "a store with the capability is asked")

	// The text ImportToLibraryWith's own helper gives is the text the fallback gives.
	_, want := database.BookFileByPathIncludingRetired(noRetiredStore{&database.MockStore{}}, "/p")
	_, got := absent.GetBookFileByPathIncludingRetired("/p")
	require.EqualError(t, got, want.Error())
}

// A plugin with no store or client hands the cores untyped nils, so their own nil
// checks (HEAD's p.store == nil, p.client == nil) stay true.
func TestDelugeEnv_HeadAdaptersNilWhenUnset(t *testing.T) {
	p := &Plugin{}
	require.Nil(t, p.headStore())
	require.Nil(t, p.headClient())
	var typedNil *delugeclient.Client
	require.Nil(t, (&Plugin{client: typedNil}).headClient())
	require.NotNil(t, p.headImporter(), "the importer adapter is always present: a nil client never moves storage")
	require.NotNil(t, p.headWriter())
}

// The HEAD writer forwards both methods to the store with the same arguments.
func TestDelugeEnv_HeadKindWriterForwards(t *testing.T) {
	var modified, updated []string
	m := &database.MockStore{
		ModifyBookFileFunc: func(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
			modified = append(modified, bookID+"/"+fileID)
			return &database.BookFile{ID: fileID}, nil
		},
		UpdateBookFileFunc: func(id string, f *database.BookFile) error {
			updated = append(updated, id+"="+f.FilePath)
			return errors.New("boom")
		},
	}
	w := (&Plugin{store: m}).headWriter()
	got, err := w.ModifyBookFile("book-hush", "bf-1", nil)
	require.NoError(t, err)
	require.Equal(t, "bf-1", got.ID)
	require.EqualError(t, w.UpdateBookFile("bf-2", &database.BookFile{FilePath: "/lib/hush.mp3"}), "boom")
	require.Equal(t, []string{"book-hush/bf-1"}, modified)
	require.Equal(t, []string{"bf-2=/lib/hush.mp3"}, updated)
}

var _ = taskstest.NewFakeRun

// HEAD's cancel switch is the env's Canceled; the native env's is the run's.
func TestDelugeEnv_CanceledWiring(t *testing.T) {
	rep := &parityReporter{}
	env := headEnv(rep, true)
	require.False(t, env.Canceled())
	rep.canceled.Store(true)
	require.True(t, env.Canceled(), "HEAD's Reporter.IsCanceled")

	def := byKind(t, (&Plugin{}).Kinds(KindDeps{}, nil), protectedPathsSyncKind)
	ctx, cancel := context.WithCancel(context.Background())
	h := newKindHost(ctx, def, tasks.ModeLive, nil)
	nenv := nativeEnv(tasks.NewRC(h))
	require.False(t, nenv.Canceled())
	cancel()
	require.True(t, nenv.Canceled(), "the run's cancel")
	require.True(t, nenv.Reporter.IsCanceled(), "the sink the shared helpers get carries it")
}
