// file: core/internal/bootseam/bootseamtest/recorder.go
// version: 1.0.2
// guid: 6473c7ec-c6f5-4957-863f-62991f66eaa2
// last-edited: 2026-11-08

// Package bootseamtest is the fault-injecting, labelling filesystem behind
// the crash-matrix kit (coretest/crashmatrix) and the swap's own crash
// tests: a crashable in-memory filesystem (pebble vfs.NewCrashableMem)
// behind an errorfs wrapper whose injector records every durable operation
// with the boot-writer label open when it ran (bootseam.Labeler), takes a
// crash clone before each kill point, and can return an error from one
// chosen operation. Test support only: no non-test core package imports it.
package bootseamtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
)

// Labels an operation can carry besides a writer-table label.
const (
	// LabelPebble is an operation of Pebble's own engine (a table, the
	// MANIFEST, OPTIONS, a marker, the WAL's creation or recycling, the
	// store directory's creation and syncs) outside any region: Pebble's
	// crash safety is relied on, not re-proven, and its operations are kill
	// points only (decision D12).
	LabelPebble = "pebble.engine"
	// LabelNone is a durable operation outside every region that is not
	// Pebble's own; a WAL write or sync outside a region is one (an
	// unlabelled batch).
	LabelNone = ""
)

// Event is one durable operation.
type Event struct {
	Seq   int            // order within the recorder
	Label string         // the region's label, LabelPebble or LabelNone
	Kind  errorfs.OpKind // the operation
	Path  string         // its path (a two-path operation's source)
	// Ordinal is the operation's index among the non-Pebble operations of
	// its label, which is deterministic across runs of one boot (Pebble's
	// background work is not).
	Ordinal int
	// Engine is an operation of Pebble's engine, whatever region it ran in:
	// one of Pebble's own files, or the creation or a sync of a store
	// directory (Options.Stores), or, outside every region, a sync of one
	// of its ancestors (Pebble's MkdirAll). Such an operation is a kill
	// point only (decision D12): Pebble fails a WAL or MANIFEST write
	// fatally, in goroutines no recover reaches.
	Engine bool
}

// Pebble reports an operation of Pebble's engine (Engine).
func (e Event) Pebble() bool { return e.Engine }

// Kill reports a kill point: an operation whose effect a crash can cut
// (a create, link, remove, rename, mkdir or sync). A write collapses into
// the next sync of its file: under CrashClone with no unsynced data a kill
// before a write equals a kill before that sync.
func (e Event) Kill() bool {
	switch e.Kind {
	case errorfs.OpCreate, errorfs.OpLink, errorfs.OpRemove, errorfs.OpRemoveAll, errorfs.OpRename,
		errorfs.OpReuseForWrite, errorfs.OpMkdirAll, errorfs.OpFileSync, errorfs.OpFileSyncData, errorfs.OpFileSyncTo:
		return true
	}
	return false
}

// Clone is the filesystem a crash just before Event would have left.
type Clone struct {
	Event Event
	FS    *vfs.MemFS
}

// Options configure a Recorder.
type Options struct {
	// Clones takes a crash clone (no unsynced data) before every kill point.
	Clones bool
	// CloneIf takes a crash clone before each kill point it accepts, when
	// Clones is not set: the nested kill points of a boot the matrix already
	// broke (R1-21 review M6). It runs with the recorder locked and must not
	// call back into it.
	CloneIf func(ev Event) bool
	// Fail returns the error to inject into ev, nil to let it run.
	Fail func(ev Event) error
	// Stores are the absolute paths of the Pebble stores the boot opens: the
	// creation and syncs of these directories are Pebble's (Event.Engine).
	Stores []string
}

// Recorder is one boot's filesystem.
type Recorder struct {
	mem  *vfs.MemFS
	fs   vfs.FS
	opts Options

	pebbleRE *regexp.Regexp

	mu       sync.Mutex
	label    string
	depth    int
	frozen   bool
	events   []Event
	clones   []Clone
	ordinals map[string]int
}

// New wraps mem, which must be crashable when opts.Clones is set.
func New(mem *vfs.MemFS, opts Options) *Recorder {
	r := &Recorder{mem: mem, opts: opts, ordinals: map[string]int{}, pebbleRE: pebbleName()}
	r.fs = errorfs.Wrap(mem, r)
	return r
}

// FS is the recording filesystem.
func (r *Recorder) FS() vfs.FS { return r.fs }

// Mem is the underlying in-memory filesystem.
func (r *Recorder) Mem() *vfs.MemFS { return r.mem }

// Seams is a boot's bootseam.Seams over this recorder.
func (r *Recorder) Seams() *bootseam.Seams { return &bootseam.Seams{FS: r.fs, Labels: r} }

// Enter opens a region (bootseam.Labeler); the outer label wins.
func (r *Recorder) Enter(label string) func() {
	r.mu.Lock()
	if r.depth == 0 {
		r.label = label
	}
	r.depth++
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.depth--
			if r.depth == 0 {
				r.label = ""
			}
			r.mu.Unlock()
		})
	}
}

// Freeze ends the boot's window: operations after it are neither recorded,
// cloned nor failed. The crash-matrix kit freezes at readiness, the end of
// 06 §6.5.1's writer table, so the shutdown that follows (the store's close
// and the WAL sync it makes) is no boot writer's.
func (r *Recorder) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Events is every durable operation recorded so far.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// Clones is every kill point's clone, in order.
func (r *Recorder) Clones() []Clone {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Clone(nil), r.clones...)
}

// String implements errorfs.Injector.
func (r *Recorder) String() string { return "bootseamtest.Recorder" }

// MaybeError implements errorfs.Injector: it records each durable operation
// with its label, clones before a kill point, and injects Fail's error.
func (r *Recorder) MaybeError(op errorfs.Op) error {
	if !durable(op.Kind) {
		return nil
	}
	r.mu.Lock()
	if r.frozen {
		r.mu.Unlock()
		return nil
	}
	ev := Event{Seq: len(r.events), Kind: op.Kind, Path: op.Path, Engine: r.engine(op)}
	switch {
	case r.depth > 0:
		ev.Label = r.label
	case ev.Engine && !walWrite(op):
		ev.Label = LabelPebble
	}
	if !ev.Engine {
		ev.Ordinal = r.ordinals[ev.Label]
		r.ordinals[ev.Label]++
	}
	r.events = append(r.events, ev)
	if ev.Kill() && (r.opts.Clones || r.opts.CloneIf != nil && r.opts.CloneIf(ev)) {
		r.clones = append(r.clones, Clone{Event: ev, FS: r.mem.CrashClone(vfs.CrashCloneCfg{})})
	}
	fail := r.opts.Fail
	r.mu.Unlock()
	if fail != nil {
		return fail(ev)
	}
	return nil
}

// engine reports an operation of Pebble's engine (Event.Engine); r.mu is
// held.
func (r *Recorder) engine(op errorfs.Op) bool {
	if r.pebbleRE.MatchString(filepath.Base(op.Path)) {
		return true
	}
	sync := op.Kind == errorfs.OpFileSync || op.Kind == errorfs.OpFileSyncData || op.Kind == errorfs.OpFileSyncTo
	for _, store := range r.opts.Stores {
		switch {
		case (sync || op.Kind == errorfs.OpMkdirAll) && op.Path == store:
			return true
		case sync && r.depth == 0 && strings.HasPrefix(store, strings.TrimSuffix(op.Path, string(filepath.Separator))+string(filepath.Separator)):
			return true
		}
	}
	return false
}

// durable reports an operation that changes the filesystem.
func durable(k errorfs.OpKind) bool {
	switch k {
	case errorfs.OpCreate, errorfs.OpLink, errorfs.OpRemove, errorfs.OpRemoveAll, errorfs.OpRename,
		errorfs.OpReuseForWrite, errorfs.OpMkdirAll, errorfs.OpFileWrite, errorfs.OpFileWriteAt,
		errorfs.OpFileSync, errorfs.OpFileSyncData, errorfs.OpFileSyncTo:
		return true
	}
	return false
}

// pebbleName is the base name of one of Pebble's own files.
func pebbleName() *regexp.Regexp {
	return regexp.MustCompile(`^(\d+\.(sst|log|blob)|MANIFEST-\d+|OPTIONS-\d+|marker\..+|LOCK|CURRENT|.+\.dbtmp)$`)
}

// walWrite is a write or sync of a WAL file: a batch's commit, which carries
// the batch's label and is unlabelled outside a region.
func walWrite(op errorfs.Op) bool {
	if filepath.Ext(op.Path) != ".log" {
		return false
	}
	switch op.Kind {
	case errorfs.OpFileWrite, errorfs.OpFileWriteAt, errorfs.OpFileSync, errorfs.OpFileSyncData, errorfs.OpFileSyncTo:
		return true
	}
	return false
}
