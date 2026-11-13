// file: internal/plugins/maintenance/kinds_t6_g5.go
// version: 1.0.0
// guid: 30f92b7b-033e-4403-927a-99445211075c
// last-edited: 2026-11-13

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// Native books Task kinds for the maintenance plugin's file group 5 (F5-10,
// 05-tasks 5T.8.2 group T6, sub-batch 5 of 7): the two iTunes-clone/regroup
// ops, the iTunes playlist import, the library-state repair, the missing-file
// ops, the metadata ops and the two narrator ops. Each definition's body is a
// shared core in its own file; HEAD's v2 Run is the core's v2 shell and the
// kind built here is its native one. The kinds are DARK: nothing registers
// them before the tasks flip (F5-18).
//
// Rule R10: a core reaches its writer and its stores only through the env its
// shell hands it (Writer and Store). The HEAD shell hands adapters over p.deps
// (headT6g5Writer, headT6g5Store); the native shell hands what the owner
// declared in t6g5Deps, with the writer built over rc.Writer().

// --- the env every core takes ---

// t6g5Base is the part of every core's env that both shells fill the same way.
// A def's own env embeds it and adds only what that body needs beyond it.
type t6g5Base struct {
	// Live is false for a preview (HEAD: dry run).
	Live bool
	// Log writes one line to the run's log.
	Log func(level slog.Level, msg string)
	// Logger is the run's logger, for the cores that log with attributes (HEAD:
	// reporter.Logger(); native: rc.Log()). Never nil.
	Logger *slog.Logger
	// Progress reports done of total with a message; a total of 0 is unknown.
	Progress func(done, total int, msg string)
	// OpID is the run's operation ID (HEAD: ctxOpID(ctx); native: rc.RunID()).
	OpID string
	// Writer is the only write path (rule R10).
	Writer t6g5Writer
	// Store is the only read path (rule R10).
	Store t6g5Store
}

// t6g5Pool is one in-body loop's worker count in both engines' terms. HEAD's
// registry.RunItems takes Workers; the native tasks.RunItems takes Par. Both
// come from one of the constructors below, so rule R4's 1:1 mapping holds by
// construction.
type t6g5Pool struct {
	// Workers is HEAD's RunItemsOptions.Concurrency (0 and 1 are sequential).
	Workers int
	// Par is the native parallelism the same worker count maps to.
	Par tasks.Parallelism
}

// t6g5FixedPool is the pool of a loop that HEAD ran with Concurrency: n (n > 0):
// tasks.Fixed(n) (rule R4).
func t6g5FixedPool(n int, reason string) t6g5Pool {
	return t6g5Pool{Workers: n, Par: tasks.Fixed(n, reason)}
}

// t6g5SeqPool is the pool of a loop that HEAD ran with Concurrency zero or
// omitted: sequential (registry clamps it to 1), tasks.Sequential (rule R4).
func t6g5SeqPool(reason string) t6g5Pool {
	return t6g5Pool{Workers: 0, Par: tasks.Sequential(reason)}
}

// t6g5CPUPool is the pool of a loop that HEAD ran with
// Concurrency: runtime.NumCPU(): tasks.CPU() (rule R4).
func t6g5CPUPool() t6g5Pool {
	return t6g5Pool{Workers: runtime.NumCPU(), Par: tasks.CPU()}
}

// t6g5ItemsOpts is what one in-body loop asks of its engine beyond the items:
// the options HEAD's registry.RunItems call carries, in the engine-neutral form
// both shells map (HEAD to registry.RunItemsOptions, native to
// tasks.ItemsOptions).
type t6g5ItemsOpts struct {
	// Pool is the loop's worker count (rule R4).
	Pool t6g5Pool
	// Label is the progress label of item i of total. Called from worker
	// goroutines: whatever it reads needs a mutex or atomic.
	Label func(i, total int) string
	// Collect runs every item and joins the errors (HEAD's ErrModeCollect);
	// otherwise the first error stops the loop.
	Collect bool
}

// t6g5ItemsFunc runs fn over n items on the engine's worker pool; i indexes the
// caller's own slice.
type t6g5ItemsFunc func(ctx context.Context, n int, o t6g5ItemsOpts, fn func(ctx context.Context, i int) error) error

// t6g5HeadItems is HEAD's loop: registry.RunItems over the indexes with the
// loop's own worker count (0 or 1 is sequential, as there).
func t6g5HeadItems(reporter sdk.Reporter) t6g5ItemsFunc {
	return func(ctx context.Context, n int, o t6g5ItemsOpts, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		opts := opsregistry.RunItemsOptions{Concurrency: o.Pool.Workers, Label: o.Label}
		if o.Collect {
			opts.ErrMode = opsregistry.ErrModeCollect
		}
		return opsregistry.RunItems(ctx, reporter, idx, func(ctx context.Context, i int) error { return fn(ctx, i) }, opts)
	}
}

// t6g5NativeItems is the native loop: tasks.RunItems over the indexes with the
// loop's parallelism (rule R4). An item's own error comes back, as at HEAD,
// not the ItemsError around it.
func t6g5NativeItems(rc *tasks.RC) t6g5ItemsFunc {
	return func(ctx context.Context, n int, o t6g5ItemsOpts, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		opts := tasks.ItemsOptions[int]{}
		if o.Label != nil {
			opts.Label = func(i int) string { return o.Label(i, n) }
		}
		if o.Collect {
			opts.OnError = tasks.Collect
		}
		_, err := tasks.RunItems(rc, o.Pool.Par, idx, func(ic *tasks.ItemRC, i int) error { return fn(ic.Context(), i) }, opts)
		var ie *tasks.ItemsError
		if errors.As(err, &ie) && ie.First != nil && !o.Collect {
			return ie.First
		}
		return err
	}
}

// t6g5HeadBase fills the base the way HEAD's shells do: from the reporter and
// the plugin's dependencies.
func (p *Plugin) t6g5HeadBase(ctx context.Context, reporter sdk.Reporter, live bool) t6g5Base {
	b := t6g5ReporterBase(ctx, reporter, live)
	b.Writer = headT6g5Writer{p: p}
	b.Store = headT6g5Store{p: p}
	return b
}

// t6g5ReporterBase is the base without a writer or a store: what a helper that
// is handed its own store (the HEAD-signature wrappers the tests call) needs.
func t6g5ReporterBase(ctx context.Context, reporter sdk.Reporter, live bool) t6g5Base {
	return t6g5Base{
		Live:     live,
		Log:      func(level slog.Level, msg string) { _ = reporter.Log(level, msg) },
		Logger:   reporter.Logger(),
		Progress: func(done, total int, msg string) { _ = reporter.UpdateProgress(done, total, msg) },
		OpID:     ctxOpID(ctx),
	}
}

// t6g5NativeBase fills the base the way the native shells do: from rc and the
// owner's deps. writes is false for a ReadOnly kind, which gets no writer.
func t6g5NativeBase(kind tasks.Kind, rc *tasks.RC, deps t6g5Deps, writes bool) (t6g5Base, error) {
	if deps.Store == nil {
		return t6g5Base{}, fmt.Errorf("%s: t6g5Deps.Store is not set", kind)
	}
	b := t6g5Base{
		Live:   rc.Mode() == tasks.ModeLive,
		Logger: rc.Log(),
		Log: func(level slog.Level, msg string) {
			rc.Log().Log(rc.Context(), level, msg)
		},
		Progress: func(done, total int, msg string) {
			n := tasks.Unknown()
			if total > 0 {
				n = tasks.Known(int64(total))
			}
			rc.Progress().Set(int64(done), n)
			if msg != "" {
				rc.Progress().Note(msg)
			}
		},
		OpID:  string(rc.RunID()),
		Store: deps.Store,
	}
	if writes {
		if deps.NewWriter == nil {
			return t6g5Base{}, fmt.Errorf("%s: t6g5Deps.NewWriter is not set", kind)
		}
		b.Writer = deps.NewWriter(rc.Writer())
		if b.Writer == nil {
			return t6g5Base{}, fmt.Errorf("%s: t6g5Deps.NewWriter returned no writer", kind)
		}
	}
	return b, nil
}

// t6g5Progress is sdk.Progress over the env's progress callback: the same
// start + n + finalize + done frames and "(pct%)" suffix, so a core reports
// the numbers HEAD's operation row always showed.
type t6g5Progress struct {
	report        func(done, total int, msg string)
	n, total, cur int
}

// t6g5NewProgress is sdk.NewProgress(reporter, n) over env.Progress.
func t6g5NewProgress(env t6g5Base, n int) *t6g5Progress {
	if n < 0 {
		n = 0
	}
	return &t6g5Progress{report: env.Progress, n: n, total: n + 2}
}

func (p *t6g5Progress) format(cur int, msg string) string {
	if p.total <= 0 {
		return msg
	}
	pct := float64(cur) / float64(p.total) * 100
	if p.n >= 100 {
		return fmt.Sprintf("%s (%.2f%%)", msg, pct)
	}
	return fmt.Sprintf("%s (%.0f%%)", msg, pct)
}

// Start emits the initial frame at (0, total).
func (p *t6g5Progress) Start(msg string) {
	p.cur = 0
	p.report(0, p.total, p.format(0, msg))
}

// Step advances the cursor by one.
func (p *t6g5Progress) Step(msg string) {
	p.cur++
	if p.cur > p.n+1 {
		p.cur = p.n + 1
	}
	p.report(p.cur, p.total, p.format(p.cur, msg))
}

// StepN jumps to the i-th unit of N.
func (p *t6g5Progress) StepN(i int, msg string) {
	i = max(0, min(i, p.n))
	p.cur = i
	p.report(i, p.total, p.format(i, msg))
}

// Finalize emits the penultimate frame at (total-1, total).
func (p *t6g5Progress) Finalize(msg string) {
	p.cur = p.total - 1
	p.report(p.cur, p.total, p.format(p.cur, msg))
}

// Done emits the final frame at (total, total).
func (p *t6g5Progress) Done(msg string) {
	p.cur = p.total
	p.report(p.total, p.total, p.format(p.total, msg))
}

// t6g5Lease is the scan stand-down held for an apply phase.
type t6g5Lease struct {
	// Lost renews the lease (HEAD's per-item heartbeat) and reports whether the
	// caller must abort its remaining writes. Native: the run was fenced.
	Lost func() bool
	// Release gives the stand-down back. Safe to defer when nothing is held.
	Release func()
}

// t6g5LeaseFunc takes the scan stand-down for reason. A genuine failure to
// stand the scan down is returned and the caller must not write.
type t6g5LeaseFunc func(ctx context.Context, reason string) (t6g5Lease, error)

// t6g5HeadLease is HEAD's acquire: the scan controller the plugin holds,
// keyed by the operation's own ID.
func (p *Plugin) t6g5HeadLease(reporter sdk.Reporter) t6g5LeaseFunc {
	return func(ctx context.Context, reason string) (t6g5Lease, error) {
		holderID, held, release, err := acquireScanStandDownForApply(ctx, p.deps, reporter, reason)
		if err != nil {
			return t6g5Lease{Lost: func() bool { return false }, Release: release}, err
		}
		return t6g5Lease{
			Lost:    func() bool { return scanStandDownLostForApply(p.deps, holderID, held) },
			Release: release,
		}, nil
	}
}

// t6g5NativeLease is the native acquire: rc.StandDown over the library scan's
// key. A lost lease fences the run's writer and cancels its context, which the
// check below reports.
func t6g5NativeLease(rc *tasks.RC, deps t6g5Deps) t6g5LeaseFunc {
	return func(ctx context.Context, reason string) (t6g5Lease, error) {
		none := t6g5Lease{Lost: func() bool { return false }, Release: func() {}}
		if deps.ScanKey == "" {
			return none, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(deps.ScanKey)
		if err != nil {
			return none, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
		}
		return t6g5Lease{
			Lost:    func() bool { return ctx.Err() != nil },
			Release: h.Release,
		}, nil
	}
}

// t6g5Held is a scan stand-down held across a whole run, with everything the
// hold rebinds: HEAD's registry.HoldScanStandDown hands back a context that is
// canceled when the lease is lost and a reporter whose progress stamps renew it.
type t6g5Held struct {
	// Ctx is the context the run's work uses.
	Ctx context.Context
	// Progress is the run's progress and log sink under the hold (HEAD: renews
	// the lease).
	Progress operations.ProgressReporter
	// Finish releases the hold and folds a lost lease into the run's error.
	Finish func(runErr error) error
}

// t6g5HoldFunc takes the scan stand-down for the whole run.
type t6g5HoldFunc func(ctx context.Context, reason string) (t6g5Held, error)

// t6g5HeadHold is HEAD's hold (registry.HoldScanStandDown over the plugin's
// scan controller).
func (p *Plugin) t6g5HeadHold(reporter sdk.Reporter) t6g5HoldFunc {
	return func(ctx context.Context, reason string) (t6g5Held, error) {
		h, err := opsregistry.HoldScanStandDown(ctx, p.deps, reporter, reason)
		if err != nil {
			return t6g5Held{}, err
		}
		return t6g5Held{Ctx: h.Context(), Progress: newOpsAdapter(h.Reporter()), Finish: h.Finish}, nil
	}
}

// t6g5NativeHold is the native hold: rc.StandDown. The progress sink is the
// run's own.
func t6g5NativeHold(rc *tasks.RC, deps t6g5Deps, env t6g5Base) t6g5HoldFunc {
	return func(ctx context.Context, reason string) (t6g5Held, error) {
		if deps.ScanKey == "" {
			return t6g5Held{}, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(deps.ScanKey)
		if err != nil {
			return t6g5Held{}, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
		}
		return t6g5Held{
			Ctx:      ctx,
			Progress: t6g5OpsProgress{env: env},
			Finish: func(runErr error) error {
				h.Release()
				return runErr
			},
		}, nil
	}
}

// t6g5OpsProgress is operations.ProgressReporter over the env: the sink the
// store-side scans (RunMetadataRefreshScan) take.
type t6g5OpsProgress struct{ env t6g5Base }

func (r t6g5OpsProgress) UpdateProgress(current, total int, message string) error {
	r.env.Progress(current, total, message)
	return nil
}

func (r t6g5OpsProgress) Log(level, message string, details *string) error {
	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	if details != nil {
		message += ": " + *details
	}
	r.env.Log(lvl, message)
	return nil
}

func (r t6g5OpsProgress) IsCanceled() bool { return false }

// t6g5RefuseModeKeys is the first step of the native shells' params decode
// (05-tasks 5T.3.7 mode contract): a key that carries the run's mode (keys:
// dry_run, apply) is refused because the mode comes from the start request, not
// params. Keys compare case-insensitively, as encoding/json matches field
// names. Empty and null params carry none.
func t6g5RefuseModeKeys(kind tasks.Kind, raw json.RawMessage, modeKeys ...string) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(t, &keys); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	for key := range keys {
		for _, banned := range modeKeys {
			if strings.EqualFold(key, banned) {
				return fmt.Errorf("%s: params carry %q; the run's mode comes from the start request, not params", kind, key)
			}
		}
	}
	return nil
}

// t6g5DecodeParams refuses a mode key, then decodes the rest into v the way
// encoding/json does. Empty and null params decode to the zero value.
func t6g5DecodeParams(kind tasks.Kind, raw json.RawMessage, v any, modeKeys ...string) error {
	if err := t6g5RefuseModeKeys(kind, raw, modeKeys...); err != nil {
		return err
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(t, v); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	return nil
}

// t6g5DecodeStrictParams refuses a mode key, then decodes with HEAD's strict
// decoder (unknown and duplicate keys refused), for a def whose HEAD shell used
// decodeStrictParams.
func t6g5DecodeStrictParams(kind tasks.Kind, raw json.RawMessage, v any, modeKeys ...string) error {
	if err := t6g5RefuseModeKeys(kind, raw, modeKeys...); err != nil {
		return err
	}
	if err := decodeStrictParams(raw, v); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	return nil
}

// t6g5LiveOnly is rule R7's "Live-only writer": HEAD runs such a def only in an
// explicit live mode, so its native shell refuses a preview.
func t6g5LiveOnly(kind tasks.Kind, rc *tasks.RC) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// --- HEAD field mapping (rules R3, R5, R6) ---

const (
	// t6g5KindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
	t6g5KindPrefix = "books."

	// t6g5SignalMemDB is the readiness component the kinds wait for (rule R5,
	// NoWarmupWait false). F5-18 registers it with the health registry.
	t6g5SignalMemDB tasks.ReadySignal = "books.memdb"
)

// t6g5SettingsManageScopes is the route requirement of HEAD's settings.manage
// atom (05-rbac 6.1, internal/books.AtomTable). internal/books imports this
// package, so the table cannot be read from here;
// TestT6G5_StartRequirementMatchesAtomTable (an external test) fails when the
// two drift.
func t6g5SettingsManageScopes() []rbac.Permission {
	return []rbac.Permission{
		"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
		"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
	}
}

// t6g5Resume is rule R5's resume column. An unspecified policy stays zero,
// which registration refuses.
func t6g5Resume(p sdk.ResumePolicy) tasks.ResumePolicy {
	switch p {
	case sdk.ResumeRestart:
		return tasks.ResumeContinue
	case sdk.ResumeRequeue:
		return tasks.ResumeFromZero
	case sdk.ResumeDrop:
		return tasks.ResumeDrop
	case sdk.ResumeAsk:
		return tasks.ResumeAsk
	}
	return 0
}

// t6g5Priority is rule R5's priority column (HEAD 0/1/2 to Low/Normal/High).
func t6g5Priority(p sdk.Priority) tasks.Priority {
	switch p {
	case sdk.PriorityLow:
		return tasks.PriorityLow
	case sdk.PriorityHigh:
		return tasks.PriorityHigh
	}
	return tasks.PriorityNormal
}

// t6g5StartRequirement is rule R6: HEAD's empty permission list is its default
// settings.manage, and so is an explicit settings.manage. Any other atom has no
// row here and yields the zero requirement, which registration refuses (fail
// closed) rather than starting on a guessed scope.
func t6g5StartRequirement(def sdk.OperationDef) rbac.Requirement {
	for _, perm := range def.Permissions {
		if perm != auth.PermSettingsManage {
			return rbac.Requirement{}
		}
	}
	return rbac.AllOf(t6g5SettingsManageScopes()...)
}

// t6g5Common maps HEAD's definition onto tasks.Common (rules R5 and R6). The
// effects are each kind's own (rule R7); the schedule comes from sched (rule
// R9) and is never a HEAD cron string.
func t6g5Common(def sdk.OperationDef, kind tasks.Kind, effects tasks.Effects, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	c := tasks.Common{
		Title:           def.DisplayName,
		Help:            def.Description,
		Effects:         effects,
		Perms:           tasks.Permissions{Start: t6g5StartRequirement(def)},
		Resume:          t6g5Resume(def.ResumePolicy),
		Priority:        t6g5Priority(def.DefaultPriority),
		Timeout:         def.Timeout,
		ProgressTimeout: def.ProgressTimeout,
	}
	if !def.NoWarmupWait {
		c.Ready = []tasks.ReadySignal{t6g5SignalMemDB}
	}
	if def.ConcurrencyKey != "" {
		c.ConcurrencyKey = tasks.Key(t6g5KindPrefix + def.ConcurrencyKey)
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// --- writer: the write methods the group's cores call ---

// t6g5Writer holds exactly the write methods this group's cores call. Methods
// are only added, never re-signed (rule R10 item 2). Each core's writes are
// grouped into one small interface so the composition stays under the repo's
// interfacebloat limit.
type t6g5Writer interface {
	libraryStateRepairModifier
	// GROUPS-WRITER
}

// --- store: the read methods the group's cores call ---

// t6g5ScanReader is the read-only library scans that take a progress sink.
type t6g5ScanReader interface {
	// RunMetadataRefreshScan counts and logs books with incomplete metadata
	// (read-only; the server's runMetadataRefreshScan).
	RunMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error
}

// t6g5BookFileReader is the book_file reads of the missing-file ops.
type t6g5BookFileReader interface {
	// GetAllBookFilesCore reads the whole book_file table in its core projection.
	GetAllBookFilesCore() ([]database.BookFileCore, error)
}

// t6g5Store holds exactly the read methods this group's cores call; none of
// them writes (rule R10 item 3).
type t6g5Store interface {
	t6g5ScanReader
	t6g5BookFileReader
	libraryStateRepairReader
	// RootDir is the library root (HEAD: deps.RootDir()).
	RootDir() string
	// GROUPS-STORE
}

// t6g5Deps is what the owner (books, from F5-18) declares for the group's
// kinds (rule R10 item 4).
type t6g5Deps struct {
	Store t6g5Store
	// NewWriter returns the writer for one run, its write methods going through
	// w (F5-18: internal/books/writer over rc.Writer()). It is not called for a
	// ReadOnly kind, whose core writes nothing.
	NewWriter func(w tasks.Writer) t6g5Writer
	// ScanKey is the concurrency key of the library scan kinds, which an apply
	// stands down while it writes (rc.StandDown). Empty refuses an apply.
	ScanKey tasks.Key
}

// --- the HEAD adapters (rule R10 item 5) ---

// errT6g5NoStore is HEAD's "database not initialized".
var errT6g5NoStore = errors.New("database not initialized")

// headT6g5Writer is t6g5Writer over HEAD's dependencies: each method calls what
// HEAD's body called, in the same order with the same arguments. Deps are read
// lazily so a test double with no store behind it is only touched by the
// method a test reaches.
type headT6g5Writer struct{ p *Plugin }

// headT6g5Store is t6g5Store over HEAD's dependencies.
type headT6g5Store struct{ p *Plugin }

// t6g5OpsStore is the plugin's store, or HEAD's "database not initialized".
func (p *Plugin) t6g5OpsStore() (database.Store, error) {
	st := p.deps.OpsStore()
	if st == nil {
		return nil, errT6g5NoStore
	}
	return st, nil
}

func (s headT6g5Store) RunMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error {
	return s.p.deps.RunMetadataRefreshScan(ctx, progress)
}

func (s headT6g5Store) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	st, err := s.p.t6g5OpsStore()
	if err != nil {
		return nil, err
	}
	return st.GetAllBookFilesCore()
}

func (s headT6g5Store) GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error) {
	st, err := s.p.t6g5OpsStore()
	if err != nil {
		return nil, err
	}
	return st.GetAllBooksCoreComplete(limit, offset)
}

func (s headT6g5Store) RootDir() string { return s.p.deps.RootDir() }

func (w headT6g5Writer) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	st, err := w.p.t6g5OpsStore()
	if err != nil {
		return nil, err
	}
	return st.ModifyBook(id, fn)
}

// --- the group's list function ---

// t6g5Kinds returns every native kind of file group 5, in file order, each with
// Schedule: sched(kind) (rule R9; nil means none). Nothing calls it in
// production; F5-18 does.
func (p *Plugin) t6g5Kinds(deps t6g5Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.libraryStateRepairKind(deps, sched),
		p.metadataRefreshKind(deps, sched),
		p.missingFileAuditKind(deps, sched),
		p.missingFileRepairKind(deps, sched),
	}
}

// t6g5KindList names t6g5Kinds from a package-level function so tasklint's
// unregistered-kind rule counts the group a registration root until F5-18's
// Registrar.Tasks call replaces it (a method is not a list function, and the
// analyzer does not load test files). F5-18 deletes both lines.
func t6g5KindList(p *Plugin, deps t6g5Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return p.t6g5Kinds(deps, sched)
}

var _ = t6g5KindList
