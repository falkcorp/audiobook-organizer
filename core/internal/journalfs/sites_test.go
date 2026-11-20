// file: core/internal/journalfs/sites_test.go
// version: 1.0.1
// guid: 98f6560d-0f04-49df-945a-41e7973e52cb
// last-edited: 2026-11-19

package journalfs_test

import (
	"errors"
	"io/fs"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
)

// site is one journal write site run on the recording filesystem: its setup
// (recorded, but not part of the site), the failure injected into it (nil for
// none), the call under test, and the exact ordered operations the matrix
// must see, each with the label that names the writer.
type site struct {
	name    string
	setup   func(e *env)
	fail    func(ev bootseamtest.Event) error
	run     func(e *env) error
	wantErr bool
	want    []string
}

// failAt injects an error into the first operation of kind that carries label.
func failAt(label string, kind errorfs.OpKind) func(ev bootseamtest.Event) error {
	var done atomic.Bool
	return func(ev bootseamtest.Event) error {
		if ev.Label == label && ev.Kind == kind && done.CompareAndSwap(false, true) {
			return errorfs.ErrInjected
		}
		return nil
	}
}

// primed is a closed journal holding its header and two lines.
func primed(e *env) {
	j := e.mustOpen(journalAt)
	for range 2 {
		if err := e.appendUncommitted(j); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		e.t.Fatal(err)
	}
}

// vanishing is an FS whose Lstat of path, once, finds the file and then
// removes it behind the caller's back (not through the recorder): the file
// that vanished between MoveFile's existence check and its open of the source.
type vanishing struct {
	journal.FS
	path string
	rm   func(string) error
	done bool
}

func (v *vanishing) Lstat(path string) (fs.FileInfo, error) {
	fi, err := v.FS.Lstat(path)
	if err == nil && path == v.path && !v.done {
		v.done = true
		if rmErr := v.rm(path); rmErr != nil {
			return nil, rmErr
		}
	}
	return fi, err
}

func sites() []site {
	hdr := journal.Header{InstallID: installID, App: "gotham"}
	base := journal.Head{Seq: 5, MAC: "oracle"}
	return []site{
		{name: "OpenFile creates the journal, its header and its directory entry",
			run: func(e *env) error { j, err := e.open(journalAt); return closeAfter(j, err) },
			want: []string{
				"journal.create:create", "journal.header:writeat", "journal.header:sync", "journal.dirsync:sync",
			}},
		{name: "Append writes and fsyncs the line",
			setup: func(e *env) { e.holdOpen(journalAt) },
			run:   func(e *env) error { return e.appendUncommitted(e.held) },
			want:  []string{"journal.append:writeat", "journal.append:sync"}},
		{name: "a failed fsync rolls the append back",
			setup:   func(e *env) { e.holdOpen(journalAt) },
			fail:    failAt(journal.LabelAppend, errorfs.OpFileSync),
			run:     func(e *env) error { return e.appendUncommitted(e.held) },
			wantErr: true,
			want: []string{
				"journal.append:writeat", "journal.append:sync",
				"journal.rollback:create", "journal.rollback:write", "journal.rollback:sync",
				"journal.rollback:rename", "journal.rollback:sync",
			}},
		{name: "OpenFile cuts an incomplete last line",
			setup: func(e *env) { primed(e); e.tearTail(journalAt) },
			run:   func(e *env) error { j, err := e.open(journalAt); return closeAfter(j, err) },
			want: []string{
				"journal.cut:create", "journal.cut:write", "journal.cut:sync",
				"journal.cut:rename", "journal.cut:sync",
			}},
		{name: "CreateBased creates, writes, fsyncs and syncs the directory",
			run: func(e *env) error {
				return journal.CreateBased(journalAt, keys{}, hdr, base, journal.WithFS(e.fsys))
			},
			want: []string{
				"journal.base.create:create", "journal.base.create:write", "journal.base.create:sync", "journal.base.dirsync:sync",
			}},
		{name: "CreateBased removes a file whose fsync failed",
			fail: failAt(journal.LabelBaseCreate, errorfs.OpFileSync),
			run: func(e *env) error {
				return journal.CreateBased(journalAt, keys{}, hdr, base, journal.WithFS(e.fsys))
			},
			wantErr: true,
			want: []string{
				"journal.base.create:create", "journal.base.create:write", "journal.base.create:sync", "journal.base.create:remove",
			}},
		{name: "WriteLocation writes a temporary file, renames it and syncs the directory",
			run: func(e *env) error { return journal.WriteLocation(stateDir, location("robin"), journal.WithFS(e.fsys)) },
			want: []string{
				"journal.location.tmp:create", "journal.location.tmp:write", "journal.location.tmp:sync",
				"journal.location.rename:rename", "journal.location.dirsync:sync",
			}},
		{name: "WriteLocation replaces an existing file the same way",
			setup: func(e *env) {
				if err := journal.WriteLocation(stateDir, location("batgirl"), journal.WithFS(e.fsys)); err != nil {
					e.t.Fatal(err)
				}
			},
			run: func(e *env) error { return journal.WriteLocation(stateDir, location("robin"), journal.WithFS(e.fsys)) },
			want: []string{
				"journal.location.tmp:create", "journal.location.tmp:write", "journal.location.tmp:sync",
				"journal.location.rename:rename", "journal.location.dirsync:sync",
			}},
		{name: "WriteLocation removes a temporary file whose fsync failed",
			fail:    failAt(journal.LabelLocationTmp, errorfs.OpFileSync),
			run:     func(e *env) error { return journal.WriteLocation(stateDir, location("robin"), journal.WithFS(e.fsys)) },
			wantErr: true,
			want: []string{
				"journal.location.tmp:create", "journal.location.tmp:write", "journal.location.tmp:sync", "journal.location.cleanup:remove",
			}},
		{name: "WriteLocation whose temporary file cannot be opened removes nothing",
			setup: func(e *env) {
				if err := e.rec.Mem().MkdirAll(stateDir+"/"+journal.LocationFile+".tmp", 0o700); err != nil {
					e.t.Fatal(err)
				}
			},
			run:     func(e *env) error { return journal.WriteLocation(stateDir, location("robin"), journal.WithFS(e.fsys)) },
			wantErr: true,
			want:    nil},
		{name: "a failed header write of a new journal has nothing to cut back (a zero-byte truncation is a no-op)",
			fail:    failAt(journal.LabelHeader, errorfs.OpFileWriteAt),
			run:     func(e *env) error { j, err := e.open(journalAt); return closeAfter(j, err) },
			wantErr: true,
			want:    []string{"journal.create:create", "journal.header:writeat", "journal.rollback:sync"}},
		{name: "WriteLocation removes a temporary file whose rename failed",
			fail:    failAt(journal.LabelLocationRename, errorfs.OpRename),
			run:     func(e *env) error { return journal.WriteLocation(stateDir, location("robin"), journal.WithFS(e.fsys)) },
			wantErr: true,
			want: []string{
				"journal.location.tmp:create", "journal.location.tmp:write", "journal.location.tmp:sync",
				"journal.location.rename:rename", "journal.location.cleanup:remove",
			}},
		{name: "MoveFile copies, fsyncs, renames and syncs the directory",
			setup: primed,
			run:   func(e *env) error { return journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)) },
			want: []string{
				"journal.move.copy:create", "journal.move.copy:write", "journal.move.copy:sync",
				"journal.move.rename:rename", "journal.move.dirsync:sync",
			}},
		{name: "MoveFile's open of a source that vanished creates it under the move's own label",
			setup: primed,
			run: func(e *env) error {
				v := &vanishing{FS: e.fsys, path: journalAt, rm: e.rec.Mem().Remove}
				return journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(v))
			},
			want: []string{"journal.move.source:create"}},
		{name: "MoveFile clears a temporary copy an earlier attempt left",
			setup: func(e *env) { primed(e); e.plant(movedTo+".moving", "a torn copy") },
			run:   func(e *env) error { return journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)) },
			want: []string{
				"journal.move.clear:remove", "journal.move.copy:create", "journal.move.copy:write", "journal.move.copy:sync",
				"journal.move.rename:rename", "journal.move.dirsync:sync",
			}},
		{name: "MoveFile creates a missing destination directory first",
			setup: primed,
			run: func(e *env) error {
				return journal.MoveFile(journalAt, "/gotham/lazarus/pit/gotham_revocations.journal", keys{}, journal.WithFS(e.fsys))
			},
			want: []string{
				"journal.move.mkdir:mkdir", "journal.move.copy:create", "journal.move.copy:write",
				"journal.move.copy:sync", "journal.move.rename:rename", "journal.move.dirsync:sync",
			}},
		{name: "MoveFile removes a copy whose fsync failed",
			setup:   primed,
			fail:    failAt(journal.LabelMoveCopy, errorfs.OpFileSync),
			run:     func(e *env) error { return journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)) },
			wantErr: true,
			want: []string{
				"journal.move.copy:create", "journal.move.copy:write", "journal.move.copy:sync",
				"journal.move.cleanup:remove",
			}},
		{name: "MoveFile removes a copy whose rename failed",
			setup:   primed,
			fail:    failAt(journal.LabelMoveRename, errorfs.OpRename),
			run:     func(e *env) error { return journal.MoveFile(journalAt, movedTo, keys{}, journal.WithFS(e.fsys)) },
			wantErr: true,
			want: []string{
				"journal.move.copy:create", "journal.move.copy:write", "journal.move.copy:sync",
				"journal.move.rename:rename", "journal.move.cleanup:remove",
			}},
		{name: "RemoveMoved removes the old journal",
			setup: primed,
			run:   func(e *env) error { return journal.RemoveMoved(journalAt, journal.WithFS(e.fsys)) },
			want:  []string{"journal.move.remove_source:remove"}},
	}
}

// closeAfter closes j when the open produced one.
func closeAfter(j *journal.File, err error) error {
	if j != nil {
		err = errors.Join(err, j.Close())
	}
	return err
}

// TestEverySiteIsVisibleWithItsLabel: each journal write site, run over the
// recording filesystem, shows the matrix exactly the durable operations it
// makes, in order, each under the label the writer table lists for it, and no
// operation without one, setup and error paths included. This is what lets the
// kit kill the boot before, or fail, any single step of any of them.
func TestEverySiteIsVisibleWithItsLabel(t *testing.T) {
	st := newStore(t)
	for _, s := range sites() {
		t.Run(s.name, func(t *testing.T) {
			var armed atomic.Bool
			e := newEnv(t, st, bootseamtest.Options{Fail: func(ev bootseamtest.Event) error {
				if s.fail == nil || !armed.Load() {
					return nil
				}
				return s.fail(ev)
			}})
			if s.setup != nil {
				s.setup(e)
			}
			before := len(e.rec.Events())
			armed.Store(true)
			err := s.run(e)
			armed.Store(false)
			if (err != nil) != s.wantErr {
				t.Fatalf("run = %v, want error %v", err, s.wantErr)
			}
			got := e.since(before)
			if want := s.want; !slices.Equal(summarize(got), want) {
				t.Errorf("operations\n got  %q\n want %q", summarize(got), want)
			}
			unlabelled(t, e.rec.Events())
		})
	}
}

// TestEveryLabelOfTheWriterTableIsReachedBySomeSite: the labels the sites
// produce are exactly journal.FSLabels(), so a label the writer table lists
// but no site carries (a dropped region) fails here, and so does a site that
// carries a label the table does not list.
func TestEveryLabelOfTheWriterTableIsReachedBySomeSite(t *testing.T) {
	st := newStore(t)
	seen := map[string]bool{}
	for _, s := range sites() {
		var armed atomic.Bool
		e := newEnv(t, st, bootseamtest.Options{Fail: func(ev bootseamtest.Event) error {
			if s.fail == nil || !armed.Load() {
				return nil
			}
			return s.fail(ev)
		}})
		if s.setup != nil {
			s.setup(e)
		}
		armed.Store(true)
		_ = s.run(e)
		armed.Store(false)
		for _, ev := range e.rec.Events() {
			seen[ev.Label] = true
		}
	}
	for _, l := range journal.FSLabels() {
		if !seen[l] {
			t.Errorf("label %s: no site run reaches it", l)
		}
		delete(seen, l)
	}
	for l := range seen {
		t.Errorf("label %q is carried by a durable operation but is not in journal.FSLabels()", l)
	}
}
