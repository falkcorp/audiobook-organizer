// file: internal/operations/registry/reporter_db.go
// version: 1.5.0
// guid: 1a2b3c4d-5e6f-7890-abcd-ef0123456789
// last-edited: 2026-06-24

package registry

// DB-backed Reporter implementation for UOS-03.
// Plugins must call gob.Register(MyState{}) in their init() functions
// before using Checkpoint with custom state types.

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Bus is satisfied by the EventHub in UOS-06. A nil Bus is safe; all
// Publish calls are skipped when the bus has not been wired.
type Bus interface {
	Publish(ctx context.Context, eventName string, payload any) error
}

// ActivityRecorder is the narrow activity-log surface used by dbReporter.
// *activity.Service satisfies this without creating an import cycle.
type ActivityRecorder interface {
	Record(database.ActivityEntry) error
}

// logEntry is a buffered log line held in memory until flush.
type logEntry struct {
	level     slog.Level
	message   string
	attrs     []slog.Attr
	createdAt time.Time
}

// dbReporter is the UOS-03 DB-backed Reporter.
type dbReporter struct {
	opID        string
	defID       string
	displayName string
	plugin      string
	traceID     string
	spanID      string

	store            database.OpsV2Store
	bus              Bus // may be nil until UOS-06
	activityRecorder ActivityRecorder
	logger           *slog.Logger

	logMu   sync.Mutex
	logBuf  []logEntry
	flushCh chan struct{}

	progressMu          sync.Mutex
	progressCurrent     int
	progressTotal       int
	lastProgressMessage string
	progressGen         atomic.Uint64 // incremented on every UpdateProgress; flush goroutine tracks last-flushed gen

	// setCurrentItemFn, if non-nil, updates the runHandle's in-memory label.
	setCurrentItemFn func(string)
	// touchProgressFn, if non-nil, stamps the owning runHandle's lastProgressAt
	// atomic nanosecond clock. Called at the very top of UpdateProgress before
	// any lock or DB write, so the watchdog always sees a fresh timestamp even
	// when UpdateOpProgressV2 is blocked behind PebbleDB compaction.
	touchProgressFn      func()
	synchronous          bool          // legacy: write DB on every UpdateProgress
	progressFlushInterval time.Duration // how often lazy flush fires (0 = no lazy flush)

	runCtx context.Context
}

// fanoutHandler is a slog.Handler that writes to slog.Default() and also
// calls r.Log() so log lines are persisted to op_logs_v2.
type fanoutHandler struct {
	base   slog.Handler
	rep    *dbReporter
	prefix string // phase prefix, empty at root
}

func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *fanoutHandler) Handle(ctx context.Context, rec slog.Record) error {
	// Forward to the underlying handler (journalctl / stderr).
	_ = h.base.Handle(ctx, rec)

	// Collect attrs from the record.
	var attrs []slog.Attr
	rec.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	msg := rec.Message
	if h.prefix != "" {
		msg = h.prefix + ": " + msg
	}
	_ = h.rep.Log(rec.Level, msg, attrs...)
	return nil
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fanoutHandler{base: h.base.WithAttrs(attrs), rep: h.rep, prefix: h.prefix}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	return &fanoutHandler{base: h.base.WithGroup(name), rep: h.rep, prefix: h.prefix}
}

// NewDBReporterForTest is an exported wrapper around newDBReporter for use in
// external _test packages. Do not use in production code.
// NewDBReporterForTest creates a synchronous (legacy-mode) DB reporter for
// tests that assert on immediate DB state after UpdateProgress.
func NewDBReporterForTest(
	runCtx context.Context,
	opID, defID, plugin, traceID, spanID string,
	store database.OpsV2Store,
	bus Bus,
	logger *slog.Logger,
) Reporter {
	return newDBReporter(runCtx, opID, defID, "", plugin, traceID, spanID, store, bus, nil, logger, nil, nil, 0, true)
}

// NewDBReporterForTestWithTouch creates a reporter with a custom touchFn for
// tests that verify the in-memory atomic clock path.
func NewDBReporterForTestWithTouch(
	runCtx context.Context,
	opID, defID, plugin, traceID, spanID string,
	store database.OpsV2Store,
	bus Bus,
	logger *slog.Logger,
	touchFn func(),
) Reporter {
	return newDBReporter(runCtx, opID, defID, "", plugin, traceID, spanID, store, bus, nil, logger, nil, touchFn, 0, false)
}

// newDBReporter creates a DB-backed Reporter.
// displayName is the human-readable op name (def.DisplayName) bound as the
// op_name attribute on every log line; empty falls back to defID.
// setCurrentItemFn, if non-nil, is called by SetCurrentItem to update
// the registry's in-memory runHandle without a DB write.
// touchProgressFn, if non-nil, stamps the owning runHandle's atomic clock.
// flushInterval controls lazy DB flush cadence (0 = disable lazy flush).
// synchronous = true writes the DB on every UpdateProgress (legacy).
func newDBReporter(
	runCtx context.Context,
	opID, defID, displayName, plugin, traceID, spanID string,
	store database.OpsV2Store,
	bus Bus,
	activityRecorder ActivityRecorder,
	logger *slog.Logger,
	setCurrentItemFn func(string),
	touchProgressFn func(),
	flushInterval time.Duration,
	synchronous bool,
) Reporter {
	if displayName == "" {
		displayName = defID
	}
	r := &dbReporter{
		opID:                  opID,
		defID:                 defID,
		displayName:           displayName,
		plugin:                plugin,
		traceID:               traceID,
		spanID:                spanID,
		store:                 store,
		bus:                   bus,
		activityRecorder:      activityRecorder,
		flushCh:               make(chan struct{}, 1),
		runCtx:                runCtx,
		setCurrentItemFn:      setCurrentItemFn,
		touchProgressFn:       touchProgressFn,
		synchronous:           synchronous,
		progressFlushInterval: flushInterval,
	}

	// Every log line emitted via reporter.Logger() inherits these attrs.
	// op_name is the human label (e.g. "AcoustID fingerprint scan") so
	// digest aggregation can group by name without joining against the
	// def table.
	baseAttrs := []slog.Attr{
		slog.String("op_id", opID),
		slog.String("def_id", defID),
		slog.String("op_name", displayName),
		slog.String("plugin", plugin),
		slog.String("trace_id", traceID),
		slog.String("span_id", spanID),
	}

	baseHandler := slog.Default().Handler().WithAttrs(baseAttrs)
	r.logger = slog.New(&fanoutHandler{base: baseHandler, rep: r})

	if logger != nil {
		baseHandler2 := logger.Handler().WithAttrs(baseAttrs)
		r.logger = slog.New(&fanoutHandler{base: baseHandler2, rep: r})
	}

	// Background flush goroutine: flushes every 250ms or when signalled.
	go r.flushLoop(runCtx)

	return r
}

// flushLoop is the background goroutine that periodically flushes the log
// buffer and, when lazy progress flushing is active, persists the in-memory
// progress state to the DB for crash-recovery observability.
func (r *dbReporter) flushLoop(ctx context.Context) {
	logTicker := time.NewTicker(250 * time.Millisecond)
	defer logTicker.Stop()

	// Wire lazy progress flush when not in synchronous mode.
	var progressTickC <-chan time.Time
	var progressTicker *time.Ticker
	if !r.synchronous && r.progressFlushInterval > 0 {
		progressTicker = time.NewTicker(r.progressFlushInterval)
		progressTickC = progressTicker.C
		defer progressTicker.Stop()
	}

	var lastFlushedGen uint64
	for {
		select {
		case <-ctx.Done():
			r.flushLogs()
			r.flushProgressLazy(&lastFlushedGen) // terminal flush — final state persisted
			return
		case <-logTicker.C:
			r.flushLogs()
		case <-r.flushCh:
			r.flushLogs()
		case <-progressTickC:
			r.flushProgressLazy(&lastFlushedGen)
		}
	}
}

// flushProgressLazy writes the current in-memory progress to the DB only when
// it has changed since the last flush (tracked by generation counter).
func (r *dbReporter) flushProgressLazy(lastFlushedGen *uint64) {
	cur := r.progressGen.Load()
	if cur == *lastFlushedGen {
		return
	}
	r.progressMu.Lock()
	current := r.progressCurrent
	total := r.progressTotal
	msg := r.lastProgressMessage
	r.progressMu.Unlock()
	if err := r.store.UpdateOpProgressV2(r.opID, current, total, msg); err != nil {
		slog.Default().Warn("dbReporter: lazy progress flush failed", "op_id", r.opID, "error", err)
	}
	*lastFlushedGen = cur
}

// flushLogs drains logBuf to the DB.
func (r *dbReporter) flushLogs() {
	// WHY recover: if the underlying store is closed during test teardown
	// (PebbleDB panics with "pebble: closed" when used after Close()), we
	// convert the panic to a logged warning. In production the store is
	// closed only at process exit, well after all reporters finish.
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Warn("dbReporter: flush recovered from panic", "op_id", r.opID, "panic", rec)
		}
	}()

	r.logMu.Lock()
	if len(r.logBuf) == 0 {
		r.logMu.Unlock()
		return
	}
	batch := r.logBuf
	r.logBuf = nil
	r.logMu.Unlock()

	rows := make([]database.OpLogV2Row, len(batch))
	for i, e := range batch {
		rows[i] = database.OpLogV2Row{
			OperationID: r.opID,
			Level:       levelString(e.level),
			Message:     e.message,
			Attrs:       attrsToJSON(e.attrs),
			CreatedAt:   e.createdAt,
		}
	}
	if err := r.store.AppendOpLogsV2(rows); err != nil {
		// Log to default logger to avoid recursion; can't use r.Log here.
		slog.Default().Warn("dbReporter: failed to flush op logs", "op_id", r.opID, "error", err)
	}
}

// UpdateProgress implements Reporter.
func (r *dbReporter) UpdateProgress(current, total int, message string) error {
	// Stamp the in-memory atomic clock first — before any lock or DB write.
	// The watchdog reads this (lock-free) so it never fires due to a blocked
	// UpdateOpProgressV2 call during PebbleDB L0 compaction.
	if r.touchProgressFn != nil {
		r.touchProgressFn()
	}

	r.progressMu.Lock()
	last := r.lastProgressMessage
	r.progressCurrent = current
	r.progressTotal = total
	r.lastProgressMessage = message
	r.progressMu.Unlock()
	r.progressGen.Add(1)

	if r.synchronous {
		if err := r.store.UpdateOpProgressV2(r.opID, current, total, message); err != nil {
			return err
		}
	}
	if r.bus != nil {
		_ = r.bus.Publish(r.runCtx, "op.updated", map[string]any{
			"op_id":            r.opID,
			"progress_current": current,
			"progress_total":   total,
		})
	}
	// Emit one log line per *distinct* progress message so the op_log feed
	// has a searchable trail of the phases the Run went through. Skipping
	// duplicates keeps a 50K-row scan from producing 50K log lines.
	if message != "" && message != last {
		r.logger.LogAttrs(r.runCtx, slog.LevelInfo, message,
			slog.String("phase", "progress"),
			slog.Int("progress_current", current),
			slog.Int("progress_total", total),
		)
	}
	return nil
}

// Log implements Reporter.
func (r *dbReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	entry := logEntry{
		level:     level,
		message:   message,
		attrs:     attrs,
		createdAt: time.Now().UTC(),
	}

	r.logMu.Lock()
	r.logBuf = append(r.logBuf, entry)
	shouldFlush := len(r.logBuf) >= 100
	r.logMu.Unlock()

	if shouldFlush {
		select {
		case r.flushCh <- struct{}{}:
		default:
		}
	}

	// Error-level entries also go to op_errors_v2 immediately.
	if level >= slog.LevelError {
		errRow := database.OpErrorV2Row{
			OperationID: r.opID,
			Plugin:      r.plugin,
			DefID:       r.defID,
			Message:     message,
			Attrs:       attrsToJSON(attrs),
			OccurredAt:  entry.createdAt,
		}
		if err := r.store.InsertOpErrorV2(errRow); err != nil {
			slog.Default().Warn("dbReporter: failed to insert op error", "op_id", r.opID, "error", err)
		}
	}

	if r.bus != nil {
		_ = r.bus.Publish(r.runCtx, "op.log", map[string]any{
			"op_id":      r.opID,
			"level":      levelString(level),
			"message":    message,
			"created_at": entry.createdAt.Format(time.RFC3339Nano),
		})
	}
	r.recordActivity(entry)
	return nil
}

// Logger implements Reporter.
func (r *dbReporter) Logger() *slog.Logger {
	return r.logger
}

// Checkpoint implements Reporter.
// It JSON-encodes state (schema_version=2) and UPSERTs into op_state_v2,
// then updates high_water_progress on operations_v2.
//
// Schema version 2 (JSON) is machine-readable by resumeRestart so the blob
// can be merged back into the re-queued params on server restart — closing
// the resume loop that schema version 1 (gob) left open. Existing v1 rows
// in op_state_v2 are ignored by resumeRestart (treated as no checkpoint).
func (r *dbReporter) Checkpoint(state any) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	r.progressMu.Lock()
	hwm := r.progressCurrent
	r.progressMu.Unlock()

	stateRow := database.OpStateV2Row{
		OperationID:   r.opID,
		StateBlob:     data,
		SchemaVersion: 2,
		WrittenAt:     time.Now().UTC(),
	}
	if err := r.store.UpsertOpStateV2(stateRow); err != nil {
		return err
	}
	return r.store.UpdateOpCheckpointV2(r.opID, hwm)
}

// IsCanceled implements Reporter.
func (r *dbReporter) IsCanceled() bool {
	return r.runCtx.Err() != nil
}

// RunPhase implements Reporter.
func (r *dbReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, Reporter) error) error {
	phase := name
	if err := r.store.UpdateOpPhaseV2(r.opID, &phase); err != nil {
		// Non-fatal; log and continue.
		slog.Default().Warn("dbReporter: failed to set current_phase", "op_id", r.opID, "phase", name, "error", err)
	}

	// Thin wrapper that prefixes the phase name in log attrs.
	phaseRep := &phaseReporter{dbReporter: r, phase: name}

	runErr := fn(ctx, phaseRep)

	// Clear phase on exit (best-effort).
	if clearErr := r.store.UpdateOpPhaseV2(r.opID, nil); clearErr != nil {
		slog.Default().Warn("dbReporter: failed to clear current_phase", "op_id", r.opID, "error", clearErr)
	}
	return runErr
}

// Trigger implements Reporter.
func (r *dbReporter) Trigger(ctx context.Context, eventName string, payload any) error {
	if r.bus == nil {
		return nil
	}
	// Inject parent_id into payload if it is a map.
	if m, ok := payload.(map[string]any); ok {
		m["parent_id"] = r.opID
		payload = m
	}
	return r.bus.Publish(ctx, eventName, payload)
}

// SetCurrentItem implements Reporter. Updates the registry's in-memory label
// for this run and emits an op.current_item SSE event. Zero DB writes.
func (r *dbReporter) SetCurrentItem(label string) {
	if r.setCurrentItemFn != nil {
		r.setCurrentItemFn(label)
	}
	if r.bus != nil {
		_ = r.bus.Publish(r.runCtx, "op.current_item", map[string]any{
			"op_id": r.opID,
			"label": label,
		})
	}
}

// --- phaseReporter wraps dbReporter and prefixes phase name in Log attrs ---

type phaseReporter struct {
	*dbReporter
	phase string
}

func (p *phaseReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	phaseAttr := slog.String("phase", p.phase)
	all := make([]slog.Attr, 0, len(attrs)+1)
	all = append(all, phaseAttr)
	all = append(all, attrs...)
	return p.dbReporter.Log(level, message, all...)
}

func (p *phaseReporter) Logger() *slog.Logger {
	return p.dbReporter.logger.With("phase", p.phase)
}

func (p *phaseReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, Reporter) error) error {
	return p.dbReporter.RunPhase(ctx, name, fn)
}

// --- helpers ---

func levelString(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

func tierForLevel(level string) string {
	if level == "debug" {
		return "debug"
	}
	return "change"
}

func (r *dbReporter) recordActivity(entry logEntry) {
	if r.activityRecorder == nil {
		return
	}
	level := levelString(entry.level)
	details := attrsToMap(entry.attrs)
	details["def_id"] = r.defID
	details["op_name"] = r.displayName
	details["plugin"] = r.plugin
	details["component"] = r.plugin
	tags := []string{
		"operation",
		"plugin:" + r.plugin,
		"def:" + r.defID,
	}
	if phase, ok := details["phase"].(string); ok && phase != "" {
		tags = append(tags, "phase:"+phase)
	}
	_ = r.activityRecorder.Record(database.ActivityEntry{
		Timestamp:   entry.createdAt,
		Tier:        tierForLevel(level),
		Type:        r.defID,
		Level:       level,
		Source:      r.plugin,
		OperationID: r.opID,
		Summary:     entry.message,
		Details:     details,
		Tags:        tags,
	})
}

func attrsToMap(attrs []slog.Attr) map[string]any {
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value.Any()
	}
	return m
}

func attrsToJSON(attrs []slog.Attr) string {
	if len(attrs) == 0 {
		return "{}"
	}
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value.Any()
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}
