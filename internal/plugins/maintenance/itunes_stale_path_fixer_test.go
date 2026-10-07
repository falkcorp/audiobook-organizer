// file: internal/plugins/maintenance/itunes_stale_path_fixer_test.go
// version: 1.0.0
// guid: 77c1b0a8-cdba-4f18-8dc7-b8dce6613846
// last-edited: 2026-10-07

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Synthetic locations only: the placeholder library layout, never a real
// title or path.
const (
	stalePathA   = "file://localhost/W:/audiobook-organizer/Placeholder Work/001 of 003/001%20of%20003.m4b"
	stalePathB   = "file://localhost/W:/audiobook-organizer/Placeholder Work/002 of 003/002 of 003.m4b"
	backedNative = `W:\itunes\iTunes Media\Audiobooks\Placeholder Author\Placeholder Work - 001 of 003.m4b`
	backedURL    = "file://localhost/W:/itunes/iTunes%20Media/Audiobooks/Placeholder%20Author/Placeholder%20Work%20-%20001%20of%20003.m4b"
)

// itunesXML is a minimal iTunes Library.xml with one track per location.
func itunesXML(locations ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
`)
	for i, loc := range locations {
		fmt.Fprintf(&b, `		<key>%d</key>
		<dict>
			<key>Track ID</key><integer>%d</integer>
			<key>Persistent ID</key><string>00112233445566%02X</string>
			<key>Name</key><string>Track %d</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Location</key><string>%s</string>
		</dict>
`, i+1, i+1, i, i, loc)
	}
	b.WriteString(`	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>`)
	return b.String()
}

// staleFixture is a fragFixture whose iTunes libraries are two XML files
// (imported, write-back) holding the given locations. A library left nil is
// a path that does not exist.
type staleFixture struct {
	*fragFixture
	readPath, writePath string
}

func newStaleFixture(t *testing.T, read, write []string) *staleFixture {
	t.Helper()
	f := &staleFixture{fragFixture: newFragFixture(t)}
	dir := t.TempDir()
	f.readPath = filepath.Join(dir, "imported", "iTunes Library.xml")
	f.writePath = filepath.Join(dir, "writeback", "iTunes Library.itl")
	for _, l := range []struct {
		path string
		locs []string
	}{{f.readPath, read}, {f.writePath, write}} {
		if l.locs == nil {
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(l.path), 0o755))
		require.NoError(t, os.WriteFile(l.path, []byte(itunesXML(l.locs...)), 0o644))
	}
	f.p.itunesLibraryPaths = func() (string, string) { return f.readPath, f.writePath }
	return f
}

// fixerPlan runs repairs.plan for fixerID with params and stores the result
// as op opID (as the op queue does).
func (f *fragFixture) fixerPlan(t *testing.T, fixerID, opID string, params any) *repairs.PlanResult {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		require.NoError(t, err)
	}
	p, err := json.Marshal(repairs.PlanParams{FixerID: fixerID, Params: raw})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsPlan(context.Background(), p, rep))
	res, ok := rep.result.(*repairs.PlanResult)
	require.True(t, ok)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	s := string(data)
	f.ops.mu.Lock()
	f.ops.rows[opID] = &database.OperationV2Row{ID: opID, DefID: repairs.PlanOpID, Status: "completed", ResultData: &s}
	f.ops.mu.Unlock()
	return res
}

// fixerApply applies rowIDs of plan planOpID for fixerID (dry_run=false).
func (f *fragFixture) fixerApply(t *testing.T, fixerID, planOpID, opID string, rowIDs []string) *repairs.ApplyResult {
	t.Helper()
	f.applyOp(opID, fixerID)
	no := false
	params, err := json.Marshal(repairs.ApplyParams{FixerID: fixerID, PlanOpID: planOpID, RowIDs: rowIDs, DryRun: &no})
	require.NoError(t, err)
	rep := &repairsOpReporter{id: opID}
	require.NoError(t, f.p.runRepairsApply(context.Background(), params, rep))
	res, ok := rep.result.(*repairs.ApplyResult)
	require.True(t, ok)
	return res
}

// staleBook seeds a one-file book whose row carries itunesPath.
func (f *staleFixture) staleBook(t *testing.T, role, itunesPath string) string {
	t.Helper()
	p := f.file(t, "lib/Placeholder Work/"+role+".m4b", 100)
	id := f.book(t, role, "Placeholder "+role, p, nil)
	f.row(t, role, id, p, role+".m4b", 100, 60, 1)
	f.updateRow(t, id, f.rowIDs[role], func(r *database.BookFile) { r.ITunesPath = itunesPath })
	return id
}

func (f *staleFixture) rowITunesPath(t *testing.T, role string) string {
	t.Helper()
	r, err := f.s.GetBookFileByID(f.ids[role], f.rowIDs[role])
	require.NoError(t, err)
	require.NotNil(t, r)
	return r.ITunesPath
}

func TestStaleITunesPath(t *testing.T) {
	t.Parallel()

	t.Run("a stale row path is cleared, journaled, and the op revert restores it", func(t *testing.T) {
		t.Parallel()
		f := newStaleFixture(t, []string{backedURL}, []string{backedNative})
		id := f.staleBook(t, "a", stalePathA)
		// A book-level stale path, too.
		bid := f.staleBook(t, "b", stalePathB)
		pb := stalePathB
		_, err := f.s.ModifyBook(bid, func(b *database.Book) error { b.ITunesPath = &pb; return nil })
		require.NoError(t, err)

		res := f.fixerPlan(t, staleITPFixerID, "op-plan", nil)
		r := findRow(t, res, id)
		require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
		require.Equal(t, staleITPClassStale, r.Class)
		require.Contains(t, r.Evidence[0], "no track at this location")
		rb := findRow(t, res, bid)
		require.True(t, rb.Applicable(), rb.SkipReason)
		require.Equal(t, "2", rb.Current["itunes_paths"])
		require.Equal(t, 2, res.ByClass[staleITPClassStale])

		out := f.fixerApply(t, staleITPFixerID, "op-plan", "op-apply", []string{id, bid})
		require.Equal(t, 2, out.Applied, "%+v", out.Rows)
		require.Empty(t, f.rowITunesPath(t, "a"))
		require.Empty(t, f.rowITunesPath(t, "b"))
		b, err := f.s.GetBookByID(bid)
		require.NoError(t, err)
		require.True(t, b.ITunesPath == nil || *b.ITunesPath == "")
		// The file and everything else on the row are untouched.
		rows, err := f.s.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, f.path("lib/Placeholder Work/a.m4b"), rows[0].FilePath)
		_, err = os.Stat(rows[0].FilePath)
		require.NoError(t, err)

		changes, err := f.s.GetOperationChanges("op-apply")
		require.NoError(t, err)
		require.Len(t, changes, 3)
		for _, c := range changes {
			require.Equal(t, undo.ChangeTypeITunesPathClear, c.ChangeType)
			require.Empty(t, c.NewValue)
			require.Empty(t, undo.NotRestorableLabel(c), "restorable")
		}

		// The preflight and the revert agree: every row is safe and restored.
		report, err := undo.PreflightUndoConflicts(f.s, "op-apply")
		require.NoError(t, err)
		require.Equal(t, 3, report.Safe, "%+v", report)
		rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
		require.NoError(t, err)
		require.Equal(t, 3, rr.Restored, "%+v", rr)
		require.Equal(t, stalePathA, f.rowITunesPath(t, "a"))
		require.Equal(t, stalePathB, f.rowITunesPath(t, "b"))
		b, err = f.s.GetBookByID(bid)
		require.NoError(t, err)
		require.NotNil(t, b.ITunesPath)
		require.Equal(t, stalePathB, *b.ITunesPath)
	})

	t.Run("a revert never overwrites a path written since", func(t *testing.T) {
		t.Parallel()
		f := newStaleFixture(t, []string{backedURL}, []string{backedURL})
		id := f.staleBook(t, "a", stalePathA)
		f.fixerPlan(t, staleITPFixerID, "op-plan", nil)
		out := f.fixerApply(t, staleITPFixerID, "op-plan", "op-apply", []string{id})
		require.Equal(t, 1, out.Applied, "%+v", out.Rows)
		f.updateRow(t, id, f.rowIDs["a"], func(r *database.BookFile) { r.ITunesPath = stalePathB })
		report, err := undo.PreflightUndoConflicts(f.s, "op-apply")
		require.NoError(t, err)
		require.Zero(t, report.Safe, "%+v", report)
		rr, _ := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
		require.NotNil(t, rr)
		require.Zero(t, rr.Restored)
		require.Equal(t, stalePathB, f.rowITunesPath(t, "a"))
	})

	t.Run("a real track at the path is not applicable, in either library and any spelling", func(t *testing.T) {
		t.Parallel()
		// The imported library has the track as a URL; the row names it as
		// a native path (and the reverse), differently cased.
		f := newStaleFixture(t, []string{backedURL}, []string{"file://localhost/W:/other/x.m4b"})
		f.staleBook(t, "native", strings.ToUpper(backedNative))
		// Only the write-back library has this one.
		g := newStaleFixture(t, []string{"file://localhost/W:/other/x.m4b"}, []string{backedNative})
		g.staleBook(t, "url", backedURL)

		r := findRow(t, f.fixerPlan(t, staleITPFixerID, "op-plan", nil), f.ids["native"])
		require.False(t, r.Applicable())
		require.Equal(t, staleITPClassBacked, r.Class, r.SkipReason)
		r = findRow(t, g.fixerPlan(t, staleITPFixerID, "op-plan", nil), g.ids["url"])
		require.False(t, r.Applicable())
		require.Equal(t, staleITPClassBacked, r.Class, r.SkipReason)

		// An apply of the held row writes nothing.
		out := g.fixerApply(t, staleITPFixerID, "op-plan", "op-apply", []string{g.ids["url"]})
		require.Equal(t, repairs.OutcomeNotApplicable, out.Rows[0].Outcome)
		require.Equal(t, backedURL, g.rowITunesPath(t, "url"))
	})

	t.Run("a track of the binary write-back .itl backs the path", func(t *testing.T) {
		t.Parallel()
		itl := filepath.Join("..", "..", "itunes", "testdata", "test_library.itl")
		lib, err := itunes.ParseITL(itl)
		require.NoError(t, err)
		loc := ""
		for _, tr := range lib.Tracks {
			if tr.Location != "" {
				loc = tr.Location
				break
			}
			if tr.LocalURL != "" {
				loc = tr.LocalURL
				break
			}
		}
		require.NotEmpty(t, loc, "the committed fixture .itl has a track with a location")
		f := newStaleFixture(t, []string{backedURL}, nil)
		abs, err := filepath.Abs(itl)
		require.NoError(t, err)
		f.writePath = abs
		f.staleBook(t, "itl", loc)
		f.staleBook(t, "stale", stalePathA)
		res := f.fixerPlan(t, staleITPFixerID, "op-plan", nil)
		require.Equal(t, staleITPClassBacked, findRow(t, res, f.ids["itl"]).Class)
		require.True(t, findRow(t, res, f.ids["stale"]).Applicable())
	})

	t.Run("an iTunes id on the book, a row or an external id is never applicable", func(t *testing.T) {
		t.Parallel()
		f := newStaleFixture(t, []string{backedURL}, []string{backedURL})
		bookPID := f.staleBook(t, "bookpid", stalePathA)
		pid := "AABBCCDDEEFF0011"
		_, err := f.s.ModifyBook(bookPID, func(b *database.Book) error { b.ITunesPersistentID = &pid; return nil })
		require.NoError(t, err)
		rowPID := f.staleBook(t, "rowpid", stalePathA)
		f.updateRow(t, rowPID, f.rowIDs["rowpid"], func(r *database.BookFile) { r.ITunesPersistentID = "AABBCCDDEEFF0022" })
		ext := f.staleBook(t, "ext", stalePathA)
		require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "AABBCCDDEEFF0033", BookID: ext}))
		tomb := f.staleBook(t, "tomb", stalePathA)
		require.NoError(t, f.s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "AABBCCDDEEFF0044", BookID: tomb, Tombstoned: true}))

		res := f.fixerPlan(t, staleITPFixerID, "op-plan", nil)
		for _, role := range []string{"bookpid", "rowpid", "ext", "tomb"} {
			r := findRow(t, res, f.ids[role])
			require.False(t, r.Applicable(), role)
			require.Equal(t, staleITPClassHasID, r.Class, "%s: %s", role, r.SkipReason)
		}
		require.Equal(t, 0, res.Applicable)
	})

	t.Run("an unreadable, unparsable or empty library makes nothing applicable", func(t *testing.T) {
		t.Parallel()
		cases := map[string]func(t *testing.T, f *staleFixture){
			"imported library missing": func(t *testing.T, f *staleFixture) {
				require.NoError(t, os.Remove(f.readPath))
			},
			"write-back library missing": func(t *testing.T, f *staleFixture) {
				require.NoError(t, os.Remove(f.writePath))
			},
			"write-back path not configured": func(t *testing.T, f *staleFixture) {
				f.p.itunesLibraryPaths = func() (string, string) { return f.readPath, "" }
			},
			"garbage": func(t *testing.T, f *staleFixture) {
				require.NoError(t, os.WriteFile(f.readPath, []byte("not a library at all"), 0o644))
			},
			"corrupt .itl magic": func(t *testing.T, f *staleFixture) {
				require.NoError(t, os.WriteFile(f.writePath, []byte("hdfm\x00\x00\x00\x01truncated"), 0o644))
			},
			"no tracks": func(t *testing.T, f *staleFixture) {
				require.NoError(t, os.WriteFile(f.writePath, []byte(itunesXML()), 0o644))
			},
		}
		for name, breakIt := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := newStaleFixture(t, []string{backedURL}, []string{backedURL})
				id := f.staleBook(t, "a", stalePathA)
				breakIt(t, f)
				res := f.fixerPlan(t, staleITPFixerID, "op-plan", nil)
				r := findRow(t, res, id)
				require.False(t, r.Applicable())
				require.Equal(t, staleITPClassUnreadable, r.Class, r.SkipReason)
				require.Equal(t, 0, res.Applicable)
			})
		}

		// A library that breaks between the plan and the apply: the re-plan
		// reads it as unreadable and the row is refused, nothing written.
		f := newStaleFixture(t, []string{backedURL}, []string{backedURL})
		id := f.staleBook(t, "a", stalePathA)
		require.True(t, findRow(t, f.fixerPlan(t, staleITPFixerID, "op-plan", nil), id).Applicable())
		require.NoError(t, os.Remove(f.writePath))
		out := f.fixerApply(t, staleITPFixerID, "op-plan", "op-apply", []string{id})
		require.Equal(t, repairs.OutcomeChangedSincePlan, out.Rows[0].Outcome)
		require.Equal(t, stalePathA, f.rowITunesPath(t, "a"))
	})

	t.Run("a path changed since the plan is refused, and so is a track added since", func(t *testing.T) {
		t.Parallel()
		f := newStaleFixture(t, []string{backedURL}, []string{backedURL})
		id := f.staleBook(t, "a", stalePathA)
		id2 := f.staleBook(t, "b", stalePathB)
		res := f.fixerPlan(t, staleITPFixerID, "op-plan", nil)
		require.True(t, findRow(t, res, id).Applicable())
		require.True(t, findRow(t, res, id2).Applicable())
		f.updateRow(t, id, f.rowIDs["a"], func(r *database.BookFile) { r.ITunesPath = stalePathA + ".moved" })
		require.NoError(t, os.WriteFile(f.readPath, []byte(itunesXML(backedURL, stalePathB)), 0o644))

		out := f.fixerApply(t, staleITPFixerID, "op-plan", "op-apply", []string{id, id2})
		for _, r := range out.Rows {
			require.Equal(t, repairs.OutcomeChangedSincePlan, r.Outcome, "%+v", r)
		}
		require.Equal(t, stalePathA+".moved", f.rowITunesPath(t, "a"))
		require.Equal(t, stalePathB, f.rowITunesPath(t, "b"))
		changes, err := f.s.GetOperationChanges("op-apply")
		require.NoError(t, err)
		require.Empty(t, changes)
	})

	t.Run("a book with one backed path is held whole; book_ids limits the trial", func(t *testing.T) {
		t.Parallel()
		f := newStaleFixture(t, []string{backedURL}, []string{backedURL})
		id := f.staleBook(t, "a", stalePathA)
		f.row(t, "a2", id, f.file(t, "lib/Placeholder Work/a2.m4b", 101), "a2.m4b", 101, 60, 2)
		f.updateRow(t, id, f.rowIDs["a2"], func(r *database.BookFile) { r.ITunesPath = backedURL })
		other := f.staleBook(t, "other", stalePathB)
		plain := f.book(t, "plain", "Placeholder plain", f.path("lib/plain.m4b"), nil)

		res := f.fixerPlan(t, staleITPFixerID, "op-plan", staleITPParams{BookIDs: []string{id, plain}})
		require.Len(t, res.Rows, 2, "only the requested books")
		r := findRow(t, res, id)
		require.Equal(t, staleITPClassPartly, r.Class, r.SkipReason)
		require.False(t, r.Applicable())
		require.Equal(t, staleITPClassNoPath, findRow(t, res, plain).Class)
		for _, row := range res.Rows {
			require.NotEqual(t, other, row.RowID)
		}
	})
}

func TestITunesLocationKeys(t *testing.T) {
	t.Parallel()
	same := [][2]string{
		{backedNative, backedURL},
		{"file:///W:/a/b%20c.m4b", `w:\A\B C.M4B`},
		{"file://W:/a/b+c.m4b", "W:/a/b+c.m4b"},
	}
	for _, p := range same {
		lib := &staleITPLibrary{locs: map[string]bool{}}
		for _, k := range itunesLocationKeys(p[0]) {
			lib.locs[k] = true
		}
		require.True(t, lib.has(p[1]), "%q should match %q", p[1], p[0])
	}
	lib := &staleITPLibrary{locs: map[string]bool{}}
	for _, k := range itunesLocationKeys("file://W:/a/b+c.m4b") {
		lib.locs[k] = true
	}
	require.False(t, lib.has("W:/a/b c.m4b"), "'+' is not a space in a file URL")
	require.Nil(t, itunesLocationKeys("  "))
}
