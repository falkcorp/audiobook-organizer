// file: core/backup/journal/component.go
// version: 1.1.0
// guid: c3d9f6a0-71e4-4a52-8b0d-6f2e91a4c7b5
// last-edited: 2026-11-11

package journal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// ErrNotBound is what Append returns before the component is bound to a
// journal file (before P4d step 2). A revoking caller that gets it still
// commits with Gap, like any other append failure.
var ErrNotBound = errors.New("journal: the revocation journal is not bound to a file yet")

// The health tokens the component reports (05-backup §5B.5, §5B.3.6). The
// kernel registers them on the authenticated health endpoints (F4-08); this
// package only computes them.
const (
	HealthUnwritable = "revocation_journal_unwritable" // degraded: Append fails, or a gap is open
	HealthBroken     = "revocation_journal_broken"     // degraded: the chain does not verify, or is truncated below the store's head
	HealthReset      = "revocation_journal_reset"      // info: another install's journal was set aside at this boot
	HealthColocated  = "revocation_journal_colocated"  // info: the journal sits on the store's filesystem device
)

// Severity is how a Health entry shows in a health report.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityDegraded Severity = "degraded"
)

// Health is one token the component reports and why.
type Health struct {
	Token    string
	Severity Severity
	Detail   string
}

// ComponentOptions is what the kernel builds the journal component with at
// P4c (the "journal component", 06 P4d; no file I/O until Bind).
type ComponentOptions struct {
	// StateDir is <state_dir>, where backup.location.json lives.
	StateDir string
	// DBPath is the resolved core.storage.path, absolute: the file describes
	// this store only when its db_path equals it.
	DBPath string
	// Keyspace is the c/backup/ handle.
	Keyspace storage.Keyspace
	// Keys derives the line MAC's key.
	Keys Keys
	// InstallID is the store's c/storage/install.
	InstallID string
	// App is the app's archive prefix, written into a new header.
	App string
	// Source is the writing binary's ID, stamped on lines that carry none.
	Source string
	// Clock stamps headers and gap lines; nil is time.Now.
	Clock func() time.Time
	// Log receives the anchor's failures (it never blocks a revocation); nil
	// discards.
	Log *slog.Logger
	// Tick is the anchor's rewrite interval; zero is one minute (§5B.3.6
	// row "Head in the store", SIMP-6's in-process exception).
	Tick time.Duration
	// DirDev is the device of the directory at a path; nil is the OS probe.
	// A failure means "unknown": no colocation token, never an error.
	DirDev func(path string) (uint64, error)
}

// Component is the journal component: the RevocationJournal auth and rbac
// receive, the single writer of backup.location.json in a running process, and
// the committed-head anchor's owner (05-backup §5B.3.6 rows "Where", "Head in
// the store"). It is constructed at P4c without file I/O and bound to the
// journal file at P4d step 2 (Bind).
//
// One mutex, mu, is held across every read-modify-write of
// backup.location.json (DATA7-14): the anchor's tick, Append, shutdown and
// the boot's and Create's writes all go through it, so no lost update can
// revert a pair or a head. The file is read afresh on every use, never cached:
// restoreswap's step x renames the saved copy back under it.
type Component struct {
	o ComponentOptions

	mu      sync.Mutex
	file    *File
	path    string
	frozen  bool
	reset   bool
	broken  error // a chain break the boot found and failed closed on (NoteBroken)
	closed  bool
	started bool
	quit    chan struct{}
	done    chan struct{}
}

var _ RevocationJournal = (*Component)(nil)

// NewComponent builds the component. It reads and writes nothing: a missing
// option is a caller's mistake.
func NewComponent(o ComponentOptions) (*Component, error) {
	switch {
	case o.StateDir == "":
		return nil, errors.New("journal: NewComponent without a state directory")
	case o.DBPath == "":
		return nil, errors.New("journal: NewComponent without the store's path")
	case o.Keyspace.Owner() != Owner:
		return nil, fmt.Errorf("journal: NewComponent needs the %s keyspace, got %q", Owner, o.Keyspace.Owner())
	case o.Keys == nil:
		return nil, errors.New("journal: NewComponent without Keys")
	case o.InstallID == "":
		return nil, errors.New("journal: NewComponent without the store's install ID")
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tick <= 0 {
		o.Tick = time.Minute
	}
	if o.DirDev == nil {
		o.DirDev = restorejournal.OSProbe().DirDev
	}
	return &Component{o: o}, nil
}

// Bind opens the journal at path and holds it (P4d step 2), replacing a file
// bound before. A non-nil error is OpenFile's: the component then holds the
// broken File it returned, so Gap still works and Close releases its lock,
// and Health reports why (unwritable, or broken for a chain that does not
// verify).
func (c *Component) Bind(ctx context.Context, path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	var closeErr error
	if c.file != nil {
		closeErr = c.file.Close()
		c.file, c.path = nil, ""
	}
	f, err := OpenFile(ctx, FileOptions{Path: path, Keyspace: c.o.Keyspace, Source: c.o.Source, Keys: c.o.Keys,
		InstallID: c.o.InstallID, App: c.o.App, Clock: c.o.Clock})
	if f != nil {
		c.file, c.path = f, path
	}
	return errors.Join(err, closeErr)
}

// Path is the bound journal file's path ("" before Bind).
func (c *Component) Path() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path
}

func (c *Component) current() *File {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.file
}

// Append syncs the committed-head anchor, then appends r to the bound file
// (File.Append). The anchor's failure is logged and never blocks the
// revocation: it is a bookkeeping rewrite, not the security action.
func (c *Component) Append(ctx context.Context, b *storage.Batch, r Revocation) error {
	f := c.current()
	if f == nil {
		return ErrNotBound
	}
	if err := c.SyncAnchor(ctx); err != nil {
		c.o.Log.Warn("journal: the committed-head anchor was not rewritten", "err", err)
	}
	return f.Append(ctx, b, r)
}

// Gap stages journal_gap/<since> into b. It needs no file, so it works before
// Bind and on a broken journal.
func (c *Component) Gap(b *storage.Batch, since time.Time) error {
	if f := c.current(); f != nil {
		return f.Gap(b, since)
	}
	return stageGapRecord(b, c.o.Keyspace, since)
}

// AppendLine writes a bookkeeping line (a gap, restore or restore_aborted
// line) and commits the head (File.AppendLine). It does not touch the anchor:
// the boot writes backup.location.json explicitly after its bookkeeping lines.
func (c *Component) AppendLine(ctx context.Context, r Revocation) (Head, error) {
	f := c.current()
	if f == nil {
		return Head{}, ErrNotBound
	}
	return f.AppendLine(ctx, r)
}

// WriteLine writes a bookkeeping line like AppendLine but leaves the head
// uncommitted: it returns the Pending the caller stages into its own batch or
// commits (File.WriteLine). The boot writes the replaced store's restore line
// this way before the re-apply, so the store's head moves last.
func (c *Component) WriteLine(ctx context.Context, r Revocation) (Pending, error) {
	f := c.current()
	if f == nil {
		return Pending{}, ErrNotBound
	}
	return f.WriteLine(ctx, r)
}

// Refuse makes the bound file fail every later append with err (File.Refuse):
// before Bind it does nothing. The boot uses it when the store's head must not
// be moved past a line it left unapplied.
func (c *Component) Refuse(err error) {
	if f := c.current(); f != nil {
		f.Refuse(err)
	}
}

// Tail is the last line of the bound file (zero before Bind).
func (c *Component) Tail() Head {
	if f := c.current(); f != nil {
		return f.Tail()
	}
	return Head{}
}

// SetFrozen freezes or thaws the anchor. While frozen no automatic rewrite
// of backup.location.json happens (not on a tick, not on Append, not at
// shutdown, and not by AdvanceServedBoot): on a restore boot, and whenever a
// restore journal is between `swapped` and `applied`, only the boot's own
// explicit WriteLocation after the hooks writes it (§5B.3.6 row "Head in the
// store", R7-4, DATA7-5).
func (c *Component) SetFrozen(frozen bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frozen = frozen
}

// NoteReset records that this boot set another install's journal aside
// (health: revocation_journal_reset).
func (c *Component) NoteReset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reset = true
}

// NoteBroken records a chain break the boot found and failed closed on, or a
// journal move that did not verify (health: revocation_journal_broken).
func (c *Component) NoteBroken(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.broken = err
}

// SyncAnchor rewrites backup.location.json's head from the committed
// c/backup/journal_head when that moved (the committed-head anchor, DATA6-4).
// It reads only committed state, so the anchor may lag the store and is never
// ahead of it; it never lowers; it rewrites only a file that describes this
// store (§5B.3.6 row "Where"), so a missing file or another store's is left
// for the boot to write; and it does nothing while frozen.
func (c *Component) SyncAnchor(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.syncAnchorLocked(ctx)
}

func (c *Component) syncAnchorLocked(ctx context.Context) error {
	if c.frozen {
		return nil
	}
	head, err := ReadHead(ctx, c.o.Keyspace)
	if err != nil {
		return err
	}
	loc, ok, err := ReadLocation(c.o.StateDir)
	if err != nil || !ok || !loc.Describes(c.o.DBPath, c.o.InstallID) || head.Seq <= loc.Head.Seq {
		return err
	}
	loc.Head = head
	return WriteLocation(c.o.StateDir, loc)
}

// LocationDesc is what the boot (or a journal move) tells the component about
// where the journal is and which store generation serves.
type LocationDesc struct {
	BackupDir string
	Journal   string
	Store     storage.StoreIdentity
}

// WriteLocation writes backup.location.json whole for this store: d, this
// store's db_path and install_id, the content marker pair and the committed
// head. It is the boot's write at the end of P4d step 2 (on a restore boot
// after the hooks, §5B.3.4 step h) and a journal move's record update. It is
// an explicit write and ignores the freeze.
func (c *Component) WriteLocation(ctx context.Context, d LocationDesc, pair ServedBoot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	head, err := ReadHead(ctx, c.o.Keyspace)
	if err != nil {
		return fmt.Errorf("journal: read %s: %w", headKey, err)
	}
	return WriteLocation(c.o.StateDir, Location{BackupDir: d.BackupDir, Journal: d.Journal, DBPath: c.o.DBPath,
		InstallID: c.o.InstallID, Store: d.Store, ServedBoot: pair, Head: head})
}

// AdvanceServedBoot writes the store's next content marker (NextServedBoot,
// with the file's n when the file describes this store) and, when the file
// does describe this store and the component is not frozen, records the pair
// and the committed head in backup.location.json in the same mutex hold. It is
// what Create calls right after its checkpoint, "handing its pair to the
// component instead of writing the file" (DATA7-14); F8-01 calls it. The boot
// calls it too and then WriteLocation, because its file may not describe this
// store yet.
func (c *Component) AdvanceServedBoot(ctx context.Context) (ServedBoot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	loc, ok, locErr := ReadLocation(c.o.StateDir)
	describes := locErr == nil && ok && loc.Describes(c.o.DBPath, c.o.InstallID)
	var fileN uint64
	if describes {
		fileN = loc.ServedBoot.N
	}
	pair, err := NextServedBoot(ctx, c.o.Keyspace, fileN)
	if err != nil {
		return ServedBoot{}, err
	}
	if !describes || c.frozen {
		return pair, nil
	}
	loc.ServedBoot = pair
	if head, err := ReadHead(ctx, c.o.Keyspace); err == nil && head.Seq > loc.Head.Seq {
		loc.Head = head
	}
	if err := WriteLocation(c.o.StateDir, loc); err != nil {
		return pair, fmt.Errorf("journal: record the served_boot pair in %s: %w", LocationFile, err)
	}
	return pair, nil
}

// Start begins the one-minute anchor tick (an in-process goroutine, stopped by
// Close). It is a no-op after the first call.
func (c *Component) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.closed {
		return
	}
	c.started = true
	c.quit, c.done = make(chan struct{}), make(chan struct{})
	go c.tick(c.quit, c.done)
}

func (c *Component) tick(quit <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(c.o.Tick)
	defer t.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-quit
		cancel()
	}()
	for {
		select {
		case <-quit:
			return
		case <-t.C:
			if err := c.SyncAnchor(ctx); err != nil && ctx.Err() == nil {
				c.o.Log.Warn("journal: the committed-head anchor was not rewritten", "err", err)
			}
		}
	}
}

// Close stops the tick, rewrites the anchor one last time (at shutdown, unless
// frozen) and closes the journal file, releasing its lock. It is safe to call
// twice.
func (c *Component) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	quit, done := c.quit, c.done
	c.mu.Unlock()
	if quit != nil {
		close(quit)
		<-done
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.syncAnchorLocked(ctx)
	if c.file != nil {
		err = errors.Join(err, c.file.Close())
		c.file = nil
	}
	return err
}

// Health reports the tokens the journal's state calls for. It reads no store
// and writes nothing.
func (c *Component) Health() []Health {
	c.mu.Lock()
	f, path, reset, broken := c.file, c.path, c.reset, c.broken
	c.mu.Unlock()
	var out []Health
	if broken != nil {
		out = append(out, Health{HealthBroken, SeverityDegraded, broken.Error()})
	}
	if f != nil {
		st := f.Status()
		switch {
		case st.Broken != nil && (errors.Is(st.Broken, ErrCorrupt) || errors.Is(st.Broken, ErrOtherInstall)):
			if broken == nil {
				out = append(out, Health{HealthBroken, SeverityDegraded, st.Broken.Error()})
			}
		case st.Broken != nil:
			out = append(out, Health{HealthUnwritable, SeverityDegraded, st.Broken.Error()})
		case st.OpenGaps > 0:
			out = append(out, Health{HealthUnwritable, SeverityDegraded, fmt.Sprintf("%d interval(s) may hold a revocation the journal lacks", st.OpenGaps)})
		}
	}
	if reset {
		out = append(out, Health{HealthReset, SeverityInfo, "another install's journal was set aside and a new one started"})
	}
	if path != "" && c.colocated(path) {
		out = append(out, Health{HealthColocated, SeverityInfo, "the journal and the store share a filesystem device: a volume snapshot rollback reverts both"})
	}
	return out
}

// colocated reports whether the journal's directory and the store's are on one
// device (SEC6-10). A stat failure is "unknown": false.
func (c *Component) colocated(path string) bool {
	jd, err := c.o.DirDev(filepath.Dir(path))
	if err != nil {
		return false
	}
	sd, err := c.o.DirDev(c.o.DBPath)
	return err == nil && jd == sd
}
