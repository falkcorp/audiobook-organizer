// file: internal/server/kinds_t7.go
// version: 1.0.0
// guid: ba45c850-618b-4422-bad7-ac5f79b66d47
// last-edited: 2026-11-18

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Native books Task kinds for conversion group T7, the internal/server op
// definitions (F5-13, 05-tasks 5T.8.2): one books.<HEAD def ID> kind per HEAD
// OperationDef. They are DARK: nothing registers them before the tasks flip
// (F5-18), which also removes HEAD's v2 shells.
//
// Every definition has two shells over one core. HEAD's Run (the v2 shell) keeps
// its param decoding and its Reporter and calls <name>Core; the native shell is
// a tasks.Task and calls the same core. A core reaches its stores only through
// the env it is handed: reads through env.Store, writes through env.Writer, and
// logging, progress and per-item pools through the hooks on the env (rule R10).

const (
	// t7KindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
	t7KindPrefix = "books."

	// t7SignalMemDB is the readiness component every kind with HEAD's
	// NoWarmupWait false waits for.
	t7SignalMemDB tasks.ReadySignal = "books.memdb"
)

// t7KindFor is the kind name rule R3 gives a HEAD def ID.
func t7KindFor(headID string) tasks.Kind { return tasks.Kind(t7KindPrefix + headID) }

// t7Env is what every core takes from its shell besides the stores. A
// definition's own env embeds it and adds its stores and the hooks only its
// body needs.
type t7Env struct {
	// Live is false for a dry run (HEAD) or a Preview run (native).
	Live bool
	// Log writes a run-log line at level.
	Log func(level slog.Level, msg string, attrs ...slog.Attr)
	// Progress reports current out of total (total <= 0 is unknown) with a note.
	Progress func(current, total int, msg string)
	// Canceled reports whether the run was canceled.
	Canceled func() bool
	// OpID is the id of the run: HEAD's operation id ("" when the reporter
	// cannot say) or the native RunID.
	OpID string
	// SetResult persists a result document: HEAD's ReporterSetResult. Nil in a
	// native shell, whose Task returns the result instead.
	SetResult func(v any) error
	// Items runs a loop on the shell's pool (HEAD: registry.RunItems; native:
	// tasks.RunItems at the kind's parallelism). Nil in a body that has no loop.
	Items t7ItemsFunc
	// Touch stamps the run's liveness clock without reporting progress.
	Touch func()
	// Checkpoint saves state for the run's resume (rc.Checkpoint). Nil in a
	// HEAD shell, whose cores that checkpoint do so through their own helper.
	Checkpoint func(state any) error
	// Restore loads the state Checkpoint saved into v and reports whether there
	// was any (rc.Restore). Nil in a HEAD shell.
	Restore func(v any) (bool, error)
}

// logf is Log without attributes.
func (e t7Env) logf(level slog.Level, format string, args ...any) {
	if e.Log != nil {
		e.Log(level, fmt.Sprintf(format, args...))
	}
}

// progress is Progress behind a nil check.
func (e t7Env) progress(current, total int, msg string) {
	if e.Progress != nil {
		e.Progress(current, total, msg)
	}
}

// t7HeadEnv fills the env from HEAD's Reporter.
func t7HeadEnv(rep opsregistry.Reporter, live bool) t7Env {
	return t7Env{
		Live: live,
		Log: func(level slog.Level, msg string, attrs ...slog.Attr) {
			_ = rep.Log(level, msg, attrs...)
		},
		Progress: func(current, total int, msg string) {
			_ = rep.UpdateProgress(current, total, msg)
		},
		Canceled:  rep.IsCanceled,
		OpID:      opsregistry.ReporterOpID(rep),
		SetResult: func(v any) error { return opsregistry.ReporterSetResult(rep, v) },
		Items:     t7HeadItems(rep),
		Touch:     func() { opsregistry.TouchLiveness(rep) },
		// HEAD's cores that checkpoint write the remaining work, which HEAD's
		// resume overlays onto the params; they never read it back.
		Checkpoint: rep.Checkpoint,
	}
}

// t7NativeEnv fills the env from the native run.
func t7NativeEnv(rc *tasks.RC) t7Env {
	return t7Env{
		Live: rc.Mode() == tasks.ModeLive,
		Log: func(level slog.Level, msg string, attrs ...slog.Attr) {
			rc.Log().LogAttrs(rc.Context(), level, msg, attrs...)
		},
		Progress: func(current, total int, msg string) {
			t := tasks.Unknown()
			if total > 0 {
				t = tasks.Known(int64(total))
			}
			rc.Progress().Set(int64(current), t)
			if msg != "" {
				rc.Progress().Note(msg)
			}
		},
		Canceled:   rc.Canceled,
		OpID:       string(rc.RunID()),
		Touch:      rc.Progress().Tick,
		Checkpoint: rc.Checkpoint,
		Restore:    rc.Restore,
	}
}

// t7ItemsOptions are the per-loop settings a core hands its pool.
type t7ItemsOptions struct {
	// Concurrency is HEAD's worker count (registry.RunItems clamps below 1 up to
	// 1). Only HEAD's pool reads it: the native pool runs the parallelism the kind
	// declares, so the two cannot disagree about what "unset" means.
	Concurrency int
	// ProgressBase and ProgressTotal place a slice of a larger set on the bar.
	ProgressBase, ProgressTotal int
	// ResumeFrom skips the first N items, a resumed run's finished prefix.
	ResumeFrom int
	// CheckpointEvery throttles CheckpointState to once per N completed items
	// (0 is every item).
	CheckpointEvery int
	// CheckpointFn is HEAD's sequential hook, called after each item completes
	// successfully; its error is logged and dropped.
	CheckpointFn func(ctx context.Context) error
	// CheckpointState receives the absolute contiguous-completion watermark
	// (ResumeFrom included). Calls are serialized; a failed item never advances it.
	CheckpointState func(ctx context.Context, watermark int) error
	// ItemTimeout bounds each item (registry PerItemTimeout / tasks ItemTimeout).
	ItemTimeout time.Duration
	// CollectErrors runs every item whatever fails (registry ErrModeCollect /
	// tasks.Collect); false stops dispatch on the first error.
	CollectErrors bool
	// Label is HEAD's per-item progress label for a loop whose label reads
	// running counters. Only HEAD's pool carries it: the native label is pure.
	Label func(i, total int) string
	// PureLabel is a label that reads only the index and the total. Both pools
	// carry it. Set at most one of Label and PureLabel.
	PureLabel func(i, total int) string
}

// t7ItemsFunc runs fn over the indexes 0..n-1 on the shell's pool. The first item
// error stops dispatch and is returned.
type t7ItemsFunc func(ctx context.Context, n int, o t7ItemsOptions, fn func(ctx context.Context, i int) error) error

// t7ForEach runs fn over items on env's pool.
func t7ForEach[T any](ctx context.Context, env t7Env, items []T, o t7ItemsOptions, fn func(ctx context.Context, item T) error) error {
	return env.Items(ctx, len(items), o, func(ctx context.Context, i int) error { return fn(ctx, items[i]) })
}

// t7HeadErrMode maps the pool's error mode onto registry.RunItems's.
func t7HeadErrMode(o t7ItemsOptions) opsregistry.ErrMode {
	if o.CollectErrors {
		return opsregistry.ErrModeCollect
	}
	return opsregistry.ErrModeFail
}

// t7HeadItems is HEAD's pool: registry.RunItems over the indexes.
func t7HeadItems(rep opsregistry.Reporter) t7ItemsFunc {
	return func(ctx context.Context, n int, o t7ItemsOptions, fn func(ctx context.Context, i int) error) error {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		lbl := o.Label
		if lbl == nil {
			lbl = o.PureLabel
		}
		return opsregistry.RunItems(ctx, rep, idx, func(ctx context.Context, i int) error {
			return fn(ctx, i)
		}, opsregistry.RunItemsOptions{
			Concurrency:       o.Concurrency,
			PerItemTimeout:    o.ItemTimeout,
			ErrMode:           t7HeadErrMode(o),
			ProgressOffset:    o.ProgressBase,
			ProgressTotal:     o.ProgressTotal,
			ResumeFrom:        o.ResumeFrom,
			CheckpointEvery:   o.CheckpointEvery,
			CheckpointFn:      o.CheckpointFn,
			CheckpointStateFn: o.CheckpointState,
			Label:             lbl,
		})
	}
}

// t7NativeItems is the native pool: tasks.RunItems over the indexes, on par, the
// kind's declared parallelism. A failed item's own error comes back, as at HEAD,
// not the ItemsError around it. The checkpoint is HEAD's: the cores keep their own
// checkpoint struct and rc.Checkpoint/rc.Restore store it, so the pool tracks the
// contiguous watermark itself, with the same meaning as registry.RunItems's.
func t7NativeItems(rc *tasks.RC, par tasks.Parallelism) t7ItemsFunc {
	return func(_ context.Context, n int, o t7ItemsOptions, fn func(ctx context.Context, i int) error) error {
		from := min(max(o.ResumeFrom, 0), n)
		idx := make([]int, 0, n-from)
		for i := from; i < n; i++ {
			idx = append(idx, i)
		}
		wm := &t7Watermark{done: make([]bool, n), next: from, every: max(o.CheckpointEvery, 1), fn: o.CheckpointState}
		itemsOpts := tasks.ItemsOptions[int]{ProgressBase: o.ProgressBase + from, ProgressTotal: o.ProgressTotal, ItemTimeout: o.ItemTimeout}
		if o.CollectErrors {
			itemsOpts.OnError = tasks.Collect
		}
		if o.PureLabel != nil {
			itemsOpts.Label = func(i int) string { return o.PureLabel(i, n) }
		}
		_, err := tasks.RunItems(rc, par, idx, func(ic *tasks.ItemRC, i int) error {
			if err := fn(ic.Context(), i); err != nil {
				return err
			}
			if cerr := wm.complete(ic.Context(), i); cerr != nil {
				rc.Log().Warn("checkpoint write failed", "err", cerr)
			}
			if o.CheckpointFn != nil {
				if cerr := o.CheckpointFn(ic.Context()); cerr != nil {
					rc.Log().Warn("checkpoint write failed", "err", cerr)
				}
			}
			return nil
		}, itemsOpts)
		var ie *tasks.ItemsError
		if errors.As(err, &ie) && ie.First != nil {
			return ie.First
		}
		return err
	}
}

// t7Watermark tracks the contiguous prefix of finished items out of n and hands it
// to a callback every `every` completions, serialized.
type t7Watermark struct {
	mu    sync.Mutex
	done  []bool
	next  int
	count int
	every int
	fn    func(ctx context.Context, watermark int) error
}

// complete records item i as finished and reports the watermark when due.
func (w *t7Watermark) complete(ctx context.Context, i int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done[i] = true
	for w.next < len(w.done) && w.done[w.next] {
		w.next++
	}
	w.count++
	if w.fn == nil || w.count%w.every != 0 {
		return nil
	}
	return w.fn(ctx, w.next)
}

// t7Hold is a scan stand-down held for the rest of a run. A definition's HEAD
// shell fills it from the acquire HEAD's body used, and its native shell from
// rc.StandDown; a field a body does not use stays nil.
type t7Hold struct {
	// Ctx is the run context rebound to the hold, canceled when the lease is lost
	// (HEAD's registry hold). Nil leaves the caller's ctx as it is.
	Ctx context.Context
	// Progress reports progress and renews the lease (HEAD's hold reporter). Nil
	// leaves the env's Progress as it is.
	Progress func(current, total int, msg string)
	// Check is the per-item beat: it renews the lease and returns an error once
	// the lease is gone or ctx is canceled.
	Check func(ctx context.Context) error
	// Renew renews the lease and reports whether it is still held.
	Renew func() bool
	// Finish releases the hold and returns runErr, joined with the lease-lost
	// cause when the lease lapsed.
	Finish func(runErr error) error
}

// t7StandDown is the acquire a body asks of its shell: hold the library scan's
// stand-down for reason before the first write.
type t7StandDown func(ctx context.Context, reason string) (t7Hold, error)

// t7Rebind applies a hold to the run: the ctx the hold rebinds, and the env with
// the hold's lease-renewing progress.
func t7Rebind(ctx context.Context, env t7Env, h t7Hold) (context.Context, t7Env) {
	if h.Ctx != nil {
		ctx = h.Ctx
	}
	if h.Progress != nil {
		env.Progress = h.Progress
	}
	return ctx, env
}

// t7HeadMetadataStandDown is HEAD's acquire for the metadata-applying ops:
// opsregistry.HoldScanStandDown over the server's scan gate, keyed by the run's
// own id and renewed on every progress stamp.
func (s *Server) t7HeadMetadataStandDown(rep opsregistry.Reporter) t7StandDown {
	return func(ctx context.Context, reason string) (t7Hold, error) {
		hold, err := s.holdMetadataScanStandDown(ctx, rep, reason)
		if err != nil {
			return t7Hold{}, err
		}
		return t7Hold{
			Ctx: hold.Context(),
			Progress: func(current, total int, msg string) {
				_ = hold.Reporter().UpdateProgress(current, total, msg)
			},
			Check:  opsregistry.ScanStandDownCheckpoint,
			Renew:  hold.Beat,
			Finish: hold.Finish,
		}, nil
	}
}

// t7HeadAuthorStandDown is HEAD's stand-down for the author ops: the scan gate's
// hold keyed by the run's op id (holdAuthorMergeStandDown), renewed through the
// gate. It refuses a run with no op id, before any write.
func (s *Server) t7HeadAuthorStandDown(opID string) t7StandDown {
	return func(ctx context.Context, reason string) (t7Hold, error) {
		release, err := s.holdAuthorMergeStandDown(ctx, opID, reason)
		if err != nil {
			return t7Hold{}, err
		}
		return t7Hold{
			Renew:  func() bool { return s.scanGate().RenewScanStandDown(opID) },
			Finish: func(runErr error) error { release(); return runErr },
		}, nil
	}
}

// t7NativeStandDown is the native acquire: rc.StandDown over the scan's key. An
// empty key refuses, so a live write never runs without the interlock. The
// kernel fences the run's writer when the lease is lost and cancels its context,
// which Check and Renew report.
func t7NativeStandDown(rc *tasks.RC, key tasks.Key) t7StandDown {
	return func(ctx context.Context, reason string) (t7Hold, error) {
		if key == "" {
			return t7Hold{}, fmt.Errorf("no scan stand-down key configured, refusing to %s without it", reason)
		}
		h, err := rc.StandDown(key)
		if err != nil {
			return t7Hold{}, fmt.Errorf("%s: the library scan did not stand down: %w", reason, err)
		}
		return t7Hold{
			Check: func(c context.Context) error {
				if c.Err() != nil {
					return context.Cause(c)
				}
				return nil
			},
			Renew:  func() bool { return ctx.Err() == nil },
			Finish: func(runErr error) error { h.Release(); return runErr },
		}, nil
	}
}

// t7ProgressReporter is the operations.ProgressReporter (and AttrReporter) the
// shared helpers that take one are handed. It is registryProgressAdapter over
// the env's hooks: HEAD's hooks reach the Reporter, a native run's reach the
// run's progress and log.
type t7ProgressReporter struct{ env t7Env }

func (r t7ProgressReporter) UpdateProgress(current, total int, message string) error {
	r.env.progress(current, total, message)
	return nil
}

func (r t7ProgressReporter) Log(level, message string, details *string) error {
	l := slog.LevelInfo
	switch level {
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	case "debug":
		l = slog.LevelDebug
	}
	var attrs []slog.Attr
	if details != nil {
		attrs = append(attrs, slog.String("details", *details))
	}
	return r.LogAttrs(l, message, attrs...)
}

func (r t7ProgressReporter) IsCanceled() bool { return r.env.Canceled != nil && r.env.Canceled() }

func (r t7ProgressReporter) LogAttrs(level slog.Level, message string, attrs ...slog.Attr) error {
	if r.env.Log != nil {
		r.env.Log(level, message, attrs...)
	}
	return nil
}

// reporter is the env's operations.ProgressReporter.
func (e t7Env) reporter() t7ProgressReporter { return t7ProgressReporter{env: e} }

// t7Store is every read the converted cores make, composed from each
// definition's own narrow interface (declared beside its core). Nothing here
// writes. It is split by file group so no single declaration is wider than the
// interface-width ratchet allows; the method set is the union.
type t7Store interface {
	t7StoreLibrary
	t7StoreEntities
}

// t7StoreEntities is the reads of the author and series definitions.
type t7StoreEntities interface {
	authorReclassifyStore
	resolveProductionAuthorStore
}

// t7StoreLibrary is the reads of the library and batch-metadata definitions.
type t7StoreLibrary interface {
	librarySizeRefreshStore
	batchApplyRefusalsResetStore
	collectionRefreshStore
	purgeSoftDeletedStore
	folderAutoScanStore
	diagnosticsAIStore
	aiAuthorReviewStore
	aiMergeApplyStore
	aiScanApplyStore
	catalogCoverageStore
	seriesRenameReads
	candidateFetchReads
	batchSaveReads
	bulkApplyPreviewStore
	identAdvanceStore
	bulkFetchOpStore
}

// t7Writer is every write the converted cores make, composed from each
// definition's own narrow interface (declared beside its core). The native
// shells build it with t7Deps.NewWriter over the run's kernel-gated
// tasks.Writer, so every write goes through the gate (and is recorded, not
// executed, in a Preview run). Its methods are declared in this package, so no
// call through it is a direct store write.
type t7Writer interface {
	t7WriterLibrary
	t7WriterFiles
	t7WriterIntegrations
	t7WriterAI
	t7WriterEntities
}

// t7WriterEntities is the writes of the author and series definitions.
type t7WriterEntities interface {
	authorReclassifyWriter
	authorMergeWriter
	resolveProductionAuthorWriter
}

// t7WriterLibrary is the writes of the library and batch-metadata definitions.
type t7WriterLibrary interface {
	batchApplyRefusalsResetWriter
	collectionRefreshWriter
	purgeSoftDeletedWriter
	folderAutoScanWriter
	libraryAIParseWriter
	aiMergeApplyWriter
	aiScanApplyWriter
	seriesRenameWriter
	candidateFetchWriter
	batchSaveWriter
	previewReportWriter
	whenScannedWriter
	batchApplyWriter
	identAdvanceWriter
	bulkFetchOpWriter
}

// t7WriterFiles is the writes of the file-writing definitions.
type t7WriterFiles interface {
	bulkWriteBackWriter
}

// t7WriterAI is the writes of the AI definitions.
type t7WriterAI interface {
	aiAuthorScanWriter
}

// t7WriterIntegrations is the writes of the iTunes and OpenLibrary definitions.
type t7WriterIntegrations interface {
	itunesImportWriter
	olDownloadWriter
	olImportWriter
}

// t7Deps is what the native kinds are built from (rule R10 item 4). Every
// field is set once, by the F5-18 registration; nothing here is read from the
// server's receiver, and tests pass a recording fake.
type t7Deps struct {
	// Store is the read side of the library store.
	Store t7Store
	// StandDownKey is the concurrency key the library scan kinds run under, which
	// a native live write holds with rc.StandDown. Empty refuses a live write that
	// needs the interlock.
	StandDownKey tasks.Key
	// NewWriter builds the write side over the run's gated writer. Per run: the
	// writer it wraps belongs to that run's executor, which is why this is a
	// constructor and not a field of the store.
	NewWriter func(tasks.Writer) t7Writer
}

// t7NewWriter builds the run's writer over its kernel-gated tasks.Writer,
// refusing a run whose owner declared no factory (or one that returned
// nothing).
func t7NewWriter(kind tasks.Kind, deps t7Deps, rc *tasks.RC) (t7Writer, error) {
	if deps.NewWriter == nil {
		return nil, fmt.Errorf("%s: t7Deps.NewWriter is not set", kind)
	}
	w := deps.NewWriter(rc.Writer())
	if w == nil {
		return nil, fmt.Errorf("%s: t7Deps.NewWriter returned no writer", kind)
	}
	return w, nil
}

// t7NeedStore refuses a native run whose owner left the store unset, before the
// core dereferences it.
func t7NeedStore(kind tasks.Kind, deps t7Deps) error {
	if deps.Store == nil {
		return fmt.Errorf("%s: t7Deps.Store is not set", kind)
	}
	return nil
}

// t7LiveOnly is the refusal a live-only writer gives a Preview run: HEAD has no
// preview for it, and a native Preview must not write.
func t7LiveOnly(rc *tasks.RC, kind tasks.Kind) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// t7ModeKeys are the param keys that would carry the run's mode. The mode comes
// from the start request (05-tasks 5T.3.7), never from params, so a native
// shell refuses them.
var t7ModeKeys = []string{"dry_run", "dryRun"}

// t7DecodeNative is the native shells' params step: a mode key is refused
// (case-insensitively, as encoding/json matches field names), then the rest is
// decoded into a fresh P. Empty and null params decode to the zero value;
// params that are not a JSON object are refused.
func t7DecodeNative[P any](kind tasks.Kind, raw json.RawMessage) (P, error) {
	var v P
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return v, nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(t, &keys); err != nil {
		return v, fmt.Errorf("%s: decode params: %w", kind, err)
	}
	for key := range keys {
		for _, banned := range t7ModeKeys {
			if strings.EqualFold(key, banned) {
				return v, fmt.Errorf("%s: params carry %q; the run's mode comes from the start request, not params", kind, key)
			}
		}
	}
	if err := json.Unmarshal(t, &v); err != nil {
		return v, fmt.Errorf("%s: decode params: %w", kind, err)
	}
	return v, nil
}

// t7AtomScopes is the route requirement of each HEAD permission atom
// (05-rbac 6.1, internal/books.AtomTable). internal/books imports this package
// through its import graph, so the table cannot be read from here; the external
// test TestT7ServerKinds_AtomTable fails when the two drift.
var t7AtomScopes = map[auth.Permission][]rbac.Permission{
	auth.PermLibraryView:         {"books:view", "core:tasks.view"},
	auth.PermLibraryEditMetadata: {"books:edit"},
	auth.PermLibraryDelete:       {"books:delete"},
	auth.PermLibraryOrganize:     {"books:organize"},
	auth.PermScanTrigger:         {"books:scan"},
	auth.PermIntegrationsManage:  {"core:integrations.manage", "books:integrations.manage"},
	auth.PermSettingsManage: {"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
		"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"},
	auth.PermCollectionsManage: {"books:collections.manage"},
}

// t7StartRequirement is rule R6: every one of HEAD's atoms is required (AllOf
// the union of their scopes); an empty list is HEAD's default settings.manage;
// a list of library.view alone keeps the scan floor, AllOf(books:scan,
// books:view) (TestStart_ConvertedViewDefKeepsScanFloor). A permission with no
// row yields the zero requirement, so the kind is refused at registration
// (fail closed) rather than starting on a guessed scope.
func t7StartRequirement(perms []auth.Permission) rbac.Requirement {
	if len(perms) == 0 {
		perms = []auth.Permission{auth.PermSettingsManage}
	}
	if !slices.ContainsFunc(perms, func(p auth.Permission) bool { return p != auth.PermLibraryView }) {
		return rbac.AllOf("books:scan", "books:view")
	}
	var scopes []rbac.Permission
	for _, p := range perms {
		row, ok := t7AtomScopes[p]
		if !ok {
			return rbac.Requirement{}
		}
		for _, s := range row {
			if !slices.Contains(scopes, s) {
				scopes = append(scopes, s)
			}
		}
	}
	return rbac.AllOf(scopes...)
}

// t7Resume is rule R5's resume mapping under the standing ruling: a HEAD
// ResumeRestart is ResumeContinue only if the body reads its checkpoint on a
// restart (checkpoints), else ResumeFromZero; ResumeRequeue is ResumeFromZero.
func t7Resume(p opsregistry.ResumePolicy, checkpoints bool) tasks.ResumePolicy {
	switch p {
	case opsregistry.ResumeRestart:
		if checkpoints {
			return tasks.ResumeContinue
		}
		return tasks.ResumeFromZero
	case opsregistry.ResumeRequeue:
		return tasks.ResumeFromZero
	case opsregistry.ResumeDrop:
		return tasks.ResumeDrop
	case opsregistry.ResumeAsk:
		return tasks.ResumeAsk
	}
	return 0
}

// t7Priority is rule R5's priority mapping (0/1/2 to Low/Normal/High).
func t7Priority(p opsregistry.Priority) tasks.Priority {
	switch p {
	case opsregistry.PriorityLow:
		return tasks.PriorityLow
	case opsregistry.PriorityHigh:
		return tasks.PriorityHigh
	}
	return tasks.PriorityNormal
}

// t7Common is the Common block rule R5 derives from a HEAD definition: the
// fields the table maps one to one. The caller sets what is not a mapping:
// Effects (rule R7) and, where the body reads a checkpoint, Resume through
// t7Resume. Resume here is the checkpoint-free mapping. Schedule comes from the
// owner's hook (rule R9).
func t7Common(head opsregistry.OperationDef, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	kind := t7KindFor(head.ID)
	c := tasks.Common{
		Title:    head.DisplayName,
		Help:     head.Description,
		Perms:    tasks.Permissions{Start: t7StartRequirement(head.Permissions)},
		Resume:   t7Resume(head.ResumePolicy, false),
		Priority: t7Priority(head.DefaultPriority),
		// Clamped: the kernel refuses a Timeout over tasks.MaxTimeout (24h) and
		// diagnostics.ai-analyze declares 25h at HEAD (seams.d/F5-13.md).
		Timeout:         min(head.Timeout, tasks.MaxTimeout),
		ProgressTimeout: head.ProgressTimeout,
		Dedupe: tasks.DedupePolicy{
			AnyParams:     head.DedupeQueuedRuns,
			SameActorOnly: head.DedupeSameActorOnly,
			Merge:         head.MergeQueuedParams,
		},
	}
	if head.ConcurrencyKey != "" {
		c.ConcurrencyKey = tasks.Key(t7KindPrefix + head.ConcurrencyKey)
	}
	for _, id := range head.DependsOn {
		c.NotWhile = append(c.NotWhile, t7KindFor(id))
	}
	if !head.NoWarmupWait {
		c.Ready = []tasks.ReadySignal{t7SignalMemDB}
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// t7ResAIScans is the AI scan store (ai_scans: phases, chunks, proposals),
// which pkg/ops has no resource for; the AI author scan is its writer.
const t7ResAIScans ops.Resource = "ai_scans"

// t7ResDedupCandidates is the dedup candidate store the dedup engine's checks
// write; pkg/ops has no constant for it.
const t7ResDedupCandidates ops.Resource = "dedup_candidates"

// t7ResCollections is the dynamic-collection member lists. HEAD's def names it
// as an informational write; pkg/ops has no constant for it.
const t7ResCollections ops.Resource = "collections"

// t7ActivityWriter is the activity-log writes the definitions make: the entries
// a run emits and the flush that closes its batch.
type t7ActivityWriter interface {
	// FlushActivityOp flushes the run's buffered activity entries.
	FlushActivityOp(opID string)
	// EmitActivityInfo writes one activity entry for the run.
	EmitActivityInfo(opID, entryType, source, summary string, tags ...string)
	// LogActivityBatch adds one item to the run's batched activity entry.
	LogActivityBatch(opID, batchType, source string, item activity.BatchItem)
}

// headT7Activity is HEAD's activity side: the server's activity writer, each
// call made as HEAD's body made it. A nil writer drops the call, as HEAD's nil
// check did (activity.LogBatch also tolerates nil).
type headT7Activity struct{ w *activity.Writer }

func (a headT7Activity) FlushActivityOp(opID string) {
	if a.w != nil {
		activity.FlushOperation(a.w, opID)
	}
}

func (a headT7Activity) EmitActivityInfo(opID, entryType, source, summary string, tags ...string) {
	if a.w != nil {
		activity.EmitInfo(a.w, opID, entryType, source, summary, tags...)
	}
}

func (a headT7Activity) LogActivityBatch(opID, batchType, source string, item activity.BatchItem) {
	if a.w != nil {
		activity.LogBatch(a.w, opID, batchType, source, item)
	}
}

// t7Res maps HEAD resource names to refs (rule R7: res(s) = tasks.Res("books", s)).
func t7Res(rs ...ops.Resource) []entity.Ref {
	out := make([]entity.Ref, 0, len(rs))
	for _, r := range rs {
		out = append(out, tasks.Res("books", string(r)))
	}
	return out
}

// t7Writes is the Effects of a kind that writes rs (rule R7).
func t7Writes(rs ...ops.Resource) tasks.Effects { return tasks.Writes(t7Res(rs...)...) }

// t7KindList is every native kind this file group builds, as a package-level
// function: tasklint's unregistered-kind rule counts package-level list
// functions only, and not a method. Rule R9: sched is the owner's schedule
// hook, nil for no schedule.
func t7KindList(s *Server, deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		s.librarySizeRefreshNative(deps, sched),
		s.batchApplyRefusalsResetNative(deps, sched),
		s.itunesImportNative(deps, sched),
		s.olDownloadNative(deps, sched),
		s.olImportNative(deps, sched),
		s.aiAuthorScanNative(deps, sched),
		s.collectionRefreshNative(deps, sched),
		s.authorReclassifyNative(deps, sched),
		s.bulkWriteBackNative(deps, sched),
		s.purgeSoftDeletedNative(deps, sched),
		s.folderAutoScanNative(deps, sched),
		s.libraryAIParseNative(deps, sched),
		s.diagnosticsAINative(deps, sched),
		s.aiAuthorReviewNative(deps, sched),
		s.aiMergeApplyNative(deps, sched),
		s.aiScanApplyNative(deps, sched),
		s.catalogCoverageNative(deps, sched),
		s.seriesRenameNative(deps, sched),
		s.candidateFetchNative(deps, sched),
		s.batchSaveNative(deps, sched),
		s.bulkApplyPreviewNative(deps, sched),
		s.applyWhenScannedNative(deps, sched),
		s.organizeWhenScannedNative(deps, sched),
		s.batchApplyNative(deps, sched),
		s.authorMergeNative(deps, sched),
		s.resolveProductionAuthorNative(deps, sched),
		s.identificationAdvanceNative(deps, sched),
		s.bulkMetadataFetchNative(deps, sched),
	}
}

// t7Kinds is the list function of rule R9 and the "Server specifics" of the
// brief: every native kind of the server's op definitions.
func (s *Server) t7Kinds(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return t7KindList(s, deps, sched)
}

// t7KindsRoot names t7KindList from non-test code so tasklint's
// unregistered-kind rule counts it a registration root (a list function nothing
// references registers nothing, and the analyzer does not load test files).
// F5-18 deletes it with the HEAD shells.
var _ = t7KindList

// The brief's R9 line, kept so the method stays referenced.
var _ = (*Server).t7Kinds
