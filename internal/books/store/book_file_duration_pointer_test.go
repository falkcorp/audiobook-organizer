// file: internal/books/store/book_file_duration_pointer_test.go
// version: 1.0.2
// guid: 7d41c9b2-6e83-4a15-b0f7-2c95e8a1d304
// last-edited: 2026-11-09

package store

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

func TestSecs(t *testing.T) {
	require.Nil(t, Secs(0), "0 is not a measured duration")
	require.Nil(t, Secs(-3))
	a, b := Secs(90), Secs(90)
	require.NotNil(t, a)
	require.Equal(t, 90, *a)
	require.NotSame(t, a, b, "every call returns a fresh pointer")
}

// A projection must not alias the source row's Duration: a writer assigns a
// fresh pointer, but nothing stops a caller from reading a stale one, and the
// aliasing rule is only safe if the copies are independent.
func TestDurationPointerIsNotSharedByProjections(t *testing.T) {
	f := BookFile{Duration: Secs(100)}
	c := f.Core()
	require.NotSame(t, f.Duration, c.Duration, "Core() copies the pointer")
	back := c.AsBookFile()
	require.NotSame(t, c.Duration, back.Duration, "AsBookFile() copies the pointer")
	cl := cloneBookFile(&f)
	require.NotSame(t, f.Duration, cl.Duration, "cloneBookFile copies the pointer")

	require.NoError(t, f.SetMeasuredDuration(200))
	require.Equal(t, 100, durtest.Sec(c.Duration), "a later write to the source does not reach the Core copy")
	require.Equal(t, 100, durtest.Sec(cl.Duration))
	require.Equal(t, 200, durtest.Sec(f.Duration))

	var nilRow BookFile
	require.Nil(t, nilRow.Core().Duration)
	cNil := BookFileCore{}
	require.Nil(t, cNil.AsBookFile().Duration)
}

func TestSetMeasuredDurationAssignsFreshPointer(t *testing.T) {
	f := BookFile{}
	require.NoError(t, f.SetMeasuredDuration(60))
	held := f.Duration
	require.NoError(t, f.SetMeasuredDuration(61))
	require.Equal(t, 60, *held, "the pointer a caller already holds is not written through")
	require.Equal(t, 61, *f.Duration)

	c := BookFileCore{}
	require.NoError(t, c.SetMeasuredDuration(60))
	heldC := c.Duration
	require.NoError(t, c.SetMeasuredDuration(62))
	require.Equal(t, 60, *heldC)
}

// A non-nil pointer to a non-positive value is not a measurement. Nothing in
// this repo builds one (Secs and SetMeasuredDuration refuse), but the accessors
// are the only readers so they carry the invariant.
func TestDurationAccessorsTreatNonPositivePointerAsUnknown(t *testing.T) {
	for _, d := range []int{0, -1} {
		v := d
		f := BookFile{Duration: &v}
		c := BookFileCore{Duration: &v}
		_, fk := f.DurationSec()
		_, ck := c.DurationSec()
		require.False(t, fk, "d=%d", d)
		require.False(t, ck, "d=%d", d)
		require.Equal(t, DurationUnprobed, f.DurationState())
		require.Equal(t, DurationUnprobed, c.DurationState())
	}
}

// The JSON shape of a row is the same as before the field became a pointer: an
// unknown duration omits the key (as omitempty did for 0) and a known one
// writes the same number.
func TestDurationJSONShape(t *testing.T) {
	for name, v := range map[string]any{"BookFile": BookFile{}, "BookFileCore": BookFileCore{}} {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		require.NotContains(t, string(raw), `"duration"`, "%s with an unknown duration must omit the key, not write null", name)
	}
	raw, err := json.Marshal(BookFile{Duration: Secs(27000)})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"duration":27000`)
	raw, err = json.Marshal(BookFileCore{Duration: Secs(27000)})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"duration":27000`)
}

// Every decode of a stored row maps a non-positive duration to nil. A row never
// stores one (omitzero on a nil pointer), so only a hand-edited or foreign row
// reaches this; a legacy row with no key is nil by construction.
func TestDecodeNormalizesNonPositiveDuration(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want *int
	}{
		{"legacy row, no key", `{"id":"f1"}`, nil},
		{"explicit zero", `{"id":"f1","duration":0}`, nil},
		{"negative", `{"id":"f1","duration":-7}`, nil},
		{"null", `{"id":"f1","duration":null}`, nil},
		{"known", `{"id":"f1","duration":3600}`, Secs(3600)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var full BookFile
			require.NoError(t, unmarshalBookFile([]byte(tc.raw), &full))
			require.Equal(t, tc.want, full.Duration)

			core, err := decodeBookFileCore([]byte(tc.raw))
			require.NoError(t, err)
			require.Equal(t, tc.want, core.Duration)
			require.Equal(t, full.Core(), core, "narrow and full decode agree")

			state := DurationUnprobed
			if tc.want != nil {
				state = DurationKnown
			}
			require.Equal(t, state, full.DurationState())
		})
	}
}

// The same rows through the stores' real read paths, planted raw the way a
// foreign writer would leave them.
func TestStoreReadsNormalizeStoredZeroDuration(t *testing.T) {
	s := setupTestPebbleStore(t)
	book, err := s.CreateBook(&Book{Title: "Year One", FilePath: "/test/year-one"})
	require.NoError(t, err)
	rows := map[string]string{
		"zero":   `"duration":0,`,
		"minus":  `"duration":-5,`,
		"absent": ``,
		"known":  `"duration":1800,`,
	}
	for id, dur := range rows {
		raw := `{"id":"` + id + `","book_id":"` + book.ID + `","file_path":"/test/year-one/` + id + `.mp3",` + dur + `"format":"mp3"}`
		require.NoError(t, s.db.Set([]byte("book_file:"+book.ID+":"+id), []byte(raw), nil))
	}

	core, err := s.getBookFilesCoreForBook(book.ID)
	require.NoError(t, err)
	require.Len(t, core, len(rows))
	for _, c := range core {
		if c.ID == "known" {
			require.Equal(t, 1800, durtest.Sec(c.Duration))
			continue
		}
		require.Nil(t, c.Duration, "core row %s", c.ID)
	}
	for id := range rows {
		f, err := s.GetBookFileByID(book.ID, id)
		require.NoError(t, err)
		require.NotNil(t, f)
		if id == "known" {
			require.Equal(t, 1800, durtest.Sec(f.Duration))
			continue
		}
		require.Nil(t, f.Duration, "row %s", id)
	}
}

// normalizeBookFileDuration repairs a millisecond value in place and must not
// write through a pointer the row shares with a copy.
func TestNormalizeBookFileDurationOnPointer(t *testing.T) {
	require.False(t, normalizeBookFileDuration(nil))
	require.False(t, normalizeBookFileDuration(&BookFile{FileSize: 1 << 30}), "an unknown duration has nothing to repair")
	for _, v := range []int{0, -5} {
		bad := BookFile{FileSize: 1 << 30, Duration: &v}
		require.True(t, normalizeBookFileDuration(&bad), "a non-positive duration is not a measurement")
		require.Nil(t, bad.Duration, "stored means > 0: %d must become unknown", v)
	}

	// 3,600,000 "seconds" over 100 MB is 0.2 kbps: milliseconds.
	f := BookFile{FileSize: 100 << 20, Duration: Secs(3_600_000)}
	shared := f.Duration
	require.True(t, normalizeBookFileDuration(&f))
	require.Equal(t, 3600, durtest.Sec(f.Duration))
	require.Equal(t, 3_600_000, *shared, "the repair assigned a fresh pointer")

	ok := BookFile{FileSize: 100 << 20, Duration: Secs(3600)}
	require.False(t, normalizeBookFileDuration(&ok))
	require.Equal(t, 3600, durtest.Sec(ok.Duration))
}

func TestDurationPtrEqual(t *testing.T) {
	require.True(t, durationPtrEqual(nil, nil))
	require.False(t, durationPtrEqual(nil, Secs(5)))
	require.False(t, durationPtrEqual(Secs(5), nil))
	require.True(t, durationPtrEqual(Secs(5), Secs(5)), "equal by value, not by address")
	require.False(t, durationPtrEqual(Secs(5), Secs(6)))
}

// The store's write paths report "aggregates need recomputing" when a file's
// duration changed. Two separately decoded copies of the same row must NOT read
// as changed (a pointer comparison would say they always do), and a real change
// must.
func TestUpdateBookFileDurationChangeDetectionIsByValue(t *testing.T) {
	s := setupTestPebbleStore(t)
	book, err := s.CreateBook(&Book{Title: "The Killing Joke", FilePath: "/test/tkj"})
	require.NoError(t, err)
	f := &BookFile{BookID: book.ID, FilePath: "/test/tkj/a.m4b", Duration: Secs(900), FileSize: 100 << 20}
	require.NoError(t, s.CreateBookFile(f))

	rewrite := func(sec int) bool {
		t.Helper()
		stored, err := s.GetBookFileByID(book.ID, f.ID)
		require.NoError(t, err)
		require.NotNil(t, stored)
		row := cloneBookFile(stored)
		row.Duration = Secs(sec) // a different pointer from the stored row's
		unlock := s.lockBookFile(f.ID)
		defer unlock()
		// recomputeAggregates=false: only a moved Duration, FileSize or BookID asks for it.
		notify, err := s.updateBookFileLocked(f.ID, &row, bookFileMergeReplace, false, nil)
		require.NoError(t, err)
		return notify
	}
	require.False(t, rewrite(900), "the same duration in a new pointer is not a change")
	require.True(t, rewrite(901), "a different duration is a change")
}

// TestNoWriteThroughBookFileDurationPointer is the aliasing guard from the
// PLAN: Core(), AsBookFile() and the memdb projection copy the Duration
// pointer, so a writer must assign a fresh one (SetMeasuredDuration) and never
// write through it (`*f.Duration = x`), or every copy sees the write.
//
// It is syntactic: it parses non-test Go under internal/, cmd/ and tools/ and
// flags any assignment or inc/dec whose target is `*<expr>.Duration`.
// Book.Duration is also a *int, and a write through it is the same statement
// on a different type; none exists today, and one that appears is reviewed by
// hand and added to allowedDurationDerefWrites with a reason, never silently.
func TestNoWriteThroughBookFileDurationPointer(t *testing.T) {
	allowedDurationDerefWrites := map[string]string{
		// "internal/pkg/file.go": "Book.Duration: a fresh Book that nothing else references",
	}
	root := repoRootForGuard(t)
	var hits []string
	for _, dir := range []string{"internal", "cmd", "tools"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return nil // a file that does not parse is the compiler's problem
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			isDerefOfDuration := func(e ast.Expr) bool {
				star, ok := e.(*ast.StarExpr)
				if !ok {
					return false
				}
				sel, ok := star.X.(*ast.SelectorExpr)
				return ok && sel.Sel.Name == "Duration"
			}
			ast.Inspect(f, func(n ast.Node) bool {
				var targets []ast.Expr
				switch x := n.(type) {
				case *ast.AssignStmt:
					targets = x.Lhs
				case *ast.IncDecStmt:
					targets = []ast.Expr{x.X}
				}
				for _, tgt := range targets {
					if isDerefOfDuration(tgt) {
						if _, ok := allowedDurationDerefWrites[rel]; !ok {
							hits = append(hits, fset.Position(tgt.Pos()).String())
						}
					}
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	sort.Strings(hits)
	require.Empty(t, hits, "write through a Duration pointer: assign a fresh one (SetMeasuredDuration / Secs). "+
		"If this is Book.Duration on a row nothing else references, add it to allowedDurationDerefWrites with the reason")
}

// repoRootForGuard finds the module root by walking up to go.mod from the
// working directory (not runtime.Caller: CI builds with -trimpath).
func repoRootForGuard(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found above the test's working directory")
		dir = parent
	}
}
