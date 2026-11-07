// file: core/log/logging.go
// version: 1.0.0
// guid: 00563821-7bde-4a0b-a1a7-ec6ede43cdb2
// last-edited: 2026-11-07

package log

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Levels (05-logging §3). slog has no Trace; ABO's LevelTrace maps here
// instead of to Debug, and the pipeline renders it as "TRACE".
const (
	LevelTrace = slog.Level(-8)
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// ParseLevel parses "trace", "debug", "info", "warn" or "error", in any case
// and with surrounding space ignored. Anything else is an error: a typo must
// not silently mean "info" (05-logging §6, "Invalid level string").
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	}
	return 0, fmt.Errorf("log: unknown level %q (want trace, debug, info, warn or error)", s)
}

// Normative attribute keys (05-logging §3). Other specs use these constants,
// not string literals.
const (
	KeyModule    = "module"    // module ID or "core"
	KeySubsystem = "subsystem" // e.g. "tasks", "scanner"
	KeyTaskKind  = "task_kind" // "books.scan"
	KeyRunID     = "run_id"
	KeyRequestID = "request_id"
	KeyRoute     = "route" // route template, never the raw path
	KeyPath      = "path"  // raw request path; always passes the path rules
	KeyUserID    = "user_id"
	KeyTraceID   = "trace_id"
	KeySpanID    = "span_id"
	KeyError     = "err"
)

// rootModule is the module of Root() and Handler().
const rootModule = "core"

// Format is the primary output's encoding.
type Format uint8

const (
	FormatText Format = iota + 1
	FormatJSON
)

// Settings is the core.log section type (05-logging §3, §8). It is declared
// here as a plain struct, because log cannot import config (03 R4), and the
// kernel registers it. The journal's keys are the separate sections
// core.log.journal and core.log.schedules (core/internal/logjournal).
type Settings struct {
	Level      string            `json:"level" schema:"enum=trace|debug|info|warn|error"`
	Levels     map[string]string `json:"levels"` // "books" or "books.scanner" -> level
	Format     string            `json:"format" reload:"restart" schema:"enum=text|json"`
	Output     string            `json:"output" reload:"restart" schema:"enum=stderr|stdout|file"`
	File       FileSettings      `json:"file" reload:"restart"`
	AddSource  bool              `json:"add_source"`
	RedactKeys []string          `json:"redact_keys"`
}

// FileSettings is core.log.file: the RotatingFile behind output=file. Path is
// relative to the state directory.
type FileSettings struct {
	Path      string `json:"path" schema:"format=path" protect:"admin"`
	MaxSizeMB int    `json:"max_size_mb" schema:"minimum=1"`
	MaxFiles  int    `json:"max_files" schema:"minimum=1"`
	TeeStderr bool   `json:"tee_stderr"`
}

// DefaultSettings returns 05-logging §8's defaults, as a fresh value on every
// call. File.Path is left empty: its default is logs/<app-id>.log, and the
// kernel fills it in because only the kernel knows the app ID.
func DefaultSettings() Settings {
	return Settings{
		Level:  "info",
		Levels: map[string]string{},
		Format: "text",
		Output: "stderr",
		File: FileSettings{
			MaxSizeMB: 100,
			MaxFiles:  7,
			TeeStderr: true,
		},
		AddSource:  false,
		RedactKeys: []string{},
	}
}

// Options configures New. The kernel builds it from core.log (05-logging §4
// step 1).
type Options struct {
	Output    io.Writer // already opened by the kernel (stderr, stdout, or a *RotatingFile)
	Format    Format
	Level     slog.Level
	Levels    map[string]slog.Level
	AddSource bool
	Redactor  *Redactor
	Now       func() time.Time
}

// Record is an immutable, already-redacted copy of one log record, handed to
// taps (05-logging §3). Attrs holds every attribute except the module,
// subsystem and trace and span IDs (which have their own fields): the
// logger's own attributes, the record's, then the context's (WithAttrs),
// resolved, with groups preserved and empty-key groups inlined as the stdlib
// handlers inline them. Each tap receives its own Attrs slice; nested group
// values are shared and must not be modified.
type Record struct {
	Time      time.Time
	Level     slog.Level
	Module    string
	Subsystem string
	Message   string
	Attrs     []slog.Attr // resolved (LogValuer applied), groups preserved
	TraceID   string
	SpanID    string
}

// Counters is a snapshot of the pipeline's counters (05-logging §3), read by
// the kernel's metrics bridge (05-logging §9; log cannot import metrics). Every
// map is a fresh copy: a caller never shares the pipeline's state. Counts only
// grow for the life of the Logging, and a stopped tap's drops stay counted.
type Counters struct {
	// Records counts records written, by module ("core" or a module ID) and
	// level: mediacore_log_records_total{level,module}.
	Records map[string]map[slog.Level]uint64
	// Dropped counts records lost, by sink: "primary" for a failed write to
	// the primary output, otherwise a tap's name (a full queue, or a tap
	// function that panicked): mediacore_log_dropped_total{sink}.
	// "primary" is always present.
	Dropped map[string]uint64
	// Redactions counts redactions, by kind: "key", "url", "path" and
	// "valuer" (RedactionKind's labels), all four always present:
	// mediacore_log_redactions_total{kind}. One attribute (or the message)
	// adds one to each kind it reported, however many values inside it were
	// replaced.
	Redactions map[string]uint64
}

// Logging is one App's logging pipeline (05-logging §3, decision L2). Safe for
// concurrent use. Two Logging values share nothing: no package-level state,
// no process default (decision L3).
type Logging struct {
	primary  slog.Handler // stdlib Text or JSON handler over Options.Output
	redactor *Redactor
	now      func() time.Time // Options.Now, or nil: keep the record's own time
	clock    func() time.Time // now, or time.Now: for the stderr rate limit
	stderr   io.Writer        // where the pipeline reports its own failures
	root     *scope           // Root() and Handler()

	mu     sync.Mutex
	rootLv slog.Level            // SetLevels' root (guarded by mu)
	table  map[string]slog.Level // SetLevels' per-scope table (guarded by mu)
	scopes map[string]*scope     // every scope created so far, by key (guarded by mu)
	drops  map[string]*atomic.Uint64
	closed bool // Close has run: no new taps, no fan-out (guarded by mu)

	taps atomic.Pointer[[]*tap] // copy-on-write; Handle reads it without the lock

	records        sync.Map // recordKey -> *atomic.Uint64
	primaryDrops   atomic.Uint64
	redactions     [redactionKinds]atomic.Uint64
	lastStderrWarn atomic.Int64 // clock() UnixNano of the last primary-failure line
}

// redactionKinds is the number of RedactionKind bits.
const redactionKinds = 4

// recordKey keys Logging.records.
type recordKey struct {
	module string
	level  slog.Level
}

// scope is one level gate: Root ("core"), a module, or a module.subsystem.
type scope struct {
	module    string
	subsystem string
	key       string         // module, or module.subsystem
	gate      *slog.LevelVar // the scope's resolved level, read atomically by Enabled
}

// New builds a pipeline over o. Output, a known Format and Redactor are
// required: an App never logs unredacted, so a nil Redactor is an error, not
// an implicit NewRedactor(nil). Options.Levels is copied.
func New(o Options) (*Logging, error) {
	if o.Output == nil {
		return nil, errors.New("log: Options.Output is nil")
	}
	if o.Redactor == nil {
		return nil, errors.New("log: Options.Redactor is nil: records would reach the output unredacted")
	}
	ho := &slog.HandlerOptions{
		AddSource: o.AddSource,
		// The pipeline gates levels itself, per scope; the stdlib handler
		// passes everything it is handed.
		Level:       slog.Level(math.MinInt),
		ReplaceAttr: replaceLevel,
	}
	var primary slog.Handler
	switch o.Format {
	case FormatText:
		primary = slog.NewTextHandler(o.Output, ho)
	case FormatJSON:
		primary = slog.NewJSONHandler(o.Output, ho)
	default:
		return nil, fmt.Errorf("log: unknown Options.Format %d", o.Format)
	}
	l := &Logging{
		primary:  primary,
		redactor: o.Redactor,
		now:      o.Now,
		clock:    o.Now,
		stderr:   os.Stderr,
		rootLv:   o.Level,
		table:    copyLevels(o.Levels),
		scopes:   map[string]*scope{},
		drops:    map[string]*atomic.Uint64{},
	}
	if l.clock == nil {
		l.clock = time.Now
	}
	l.taps.Store(&[]*tap{})
	l.root = l.scope(rootModule, "")
	return l, nil
}

func copyLevels(m map[string]slog.Level) map[string]slog.Level {
	out := make(map[string]slog.Level, len(m))
	maps.Copy(out, m)
	return out
}

// replaceLevel renders LevelTrace as "TRACE" (and a level between trace and
// debug as "TRACE+n"), where slog would print "DEBUG-4".
func replaceLevel(groups []string, a slog.Attr) slog.Attr {
	if len(groups) != 0 || a.Key != slog.LevelKey {
		return a
	}
	lv, ok := a.Value.Any().(slog.Level)
	if !ok || lv >= LevelDebug {
		return a
	}
	return slog.String(slog.LevelKey, levelName(lv))
}

// levelName is the label replaceLevel writes for a level below debug.
func levelName(lv slog.Level) string {
	if lv == LevelTrace {
		return "TRACE"
	}
	return fmt.Sprintf("TRACE%+d", int(lv-LevelTrace))
}

// Root returns the core logger (module="core").
func (l *Logging) Root() *slog.Logger { return slog.New(&handler{l: l, sc: l.root}) }

// Module returns module id's logger (module=id). It never panics: an invalid
// id is refused at kernel validation, not here.
func (l *Logging) Module(id string) *slog.Logger {
	return slog.New(&handler{l: l, sc: l.scope(id, "")})
}

// Scope returns a logger for module's subsystem (module and subsystem
// attributes) with its own level gate, resolved from Levels["module.subsystem"],
// then Levels["module"], then the root level. An empty subsystem is Module.
func (l *Logging) Scope(module, subsystem string) *slog.Logger {
	return slog.New(&handler{l: l, sc: l.scope(module, subsystem)})
}

// Handler returns the core scope's handler, for adapters (the gin bridge, the
// logr sink).
func (l *Logging) Handler() slog.Handler { return &handler{l: l, sc: l.root} }

// Redactor returns the pipeline's redactor, for span redaction (05-metrics).
func (l *Logging) Redactor() *Redactor { return l.redactor }

// scope returns the gate for module[.subsystem], creating it on first use
// with its level resolved through the current table.
func (l *Logging) scope(module, subsystem string) *scope {
	key := module
	if subsystem != "" {
		key = module + "." + subsystem
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.scopes[key]; ok {
		return s
	}
	s := &scope{module: module, subsystem: subsystem, key: key, gate: new(slog.LevelVar)}
	s.gate.Set(l.resolveLocked(key, module))
	l.scopes[key] = s
	return s
}

// resolveLocked is 05-logging §3's resolution: table[key], then
// table[module], then the root level. l.mu must be held.
func (l *Logging) resolveLocked(key, module string) slog.Level {
	if lv, ok := l.table[key]; ok {
		return lv
	}
	if lv, ok := l.table[module]; ok {
		return lv
	}
	return l.rootLv
}

// SetLevels replaces the root level and the per-scope table and re-resolves
// every scope's gate, so existing loggers see the change without being
// rebuilt and Enabled stays one atomic load. The whole table is kept: a scope
// created later resolves through it. unknown lists, sorted, the keys of per
// that name no scope created so far (neither a module nor a module.subsystem
// in use); they are not an error (05-logging §6, "Unknown module in levels":
// fail-open, the caller warns), and they change no existing logger.
func (l *Logging) SetLevels(root slog.Level, per map[string]slog.Level) (unknown []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rootLv = root
	l.table = copyLevels(per)
	known := map[string]bool{}
	for _, s := range l.scopes {
		s.gate.Set(l.resolveLocked(s.key, s.module))
		known[s.key] = true
		known[s.module] = true
	}
	for k := range per {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	return unknown
}

// Level returns the level a record from scope ("module" or
// "module.subsystem") is gated at: the scope's gate when it exists, otherwise
// the same resolution a scope created now would get.
func (l *Logging) Level(scope string) slog.Level {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.scopes[scope]; ok {
		return s.gate.Level()
	}
	module, _, _ := strings.Cut(scope, ".")
	return l.resolveLocked(scope, module)
}

// Counters returns a snapshot of the pipeline's counters.
func (l *Logging) Counters() Counters {
	c := Counters{
		Records:    map[string]map[slog.Level]uint64{},
		Dropped:    map[string]uint64{"primary": l.primaryDrops.Load()},
		Redactions: map[string]uint64{},
	}
	l.records.Range(func(k, v any) bool {
		rk := k.(recordKey)
		m := c.Records[rk.module]
		if m == nil {
			m = map[slog.Level]uint64{}
			c.Records[rk.module] = m
		}
		m[rk.level] = v.(*atomic.Uint64).Load()
		return true
	})
	l.mu.Lock()
	for name, n := range l.drops {
		c.Dropped[name] += n.Load()
	}
	l.mu.Unlock()
	for i := range redactionKinds {
		c.Redactions[RedactionKind(1<<i).String()] = l.redactions[i].Load()
	}
	return c
}

// countRecord adds one record to the (module, level) counter.
func (l *Logging) countRecord(module string, level slog.Level) {
	k := recordKey{module: module, level: level}
	v, ok := l.records.Load(k)
	if !ok {
		v, _ = l.records.LoadOrStore(k, new(atomic.Uint64))
	}
	v.(*atomic.Uint64).Add(1)
}

// countRedactions adds one to each kind set in k.
func (l *Logging) countRedactions(k RedactionKind) {
	for i := range redactionKinds {
		if k&(1<<i) != 0 {
			l.redactions[i].Add(1)
		}
	}
}

// primaryFailed records a lost primary write (05-logging §6: fail-open,
// counted, at most one stderr line per minute).
func (l *Logging) primaryFailed(err error) {
	n := l.primaryDrops.Add(1)
	now := l.clock().UnixNano()
	last := l.lastStderrWarn.Load()
	if last != 0 && now-last < int64(time.Minute) {
		return
	}
	if !l.lastStderrWarn.CompareAndSwap(last, now) {
		return // another goroutine is writing this minute's line
	}
	if _, werr := fmt.Fprintf(l.stderr, "mediacore/log: primary output write failed, %d record(s) lost so far: %q\n", n, err.Error()); werr != nil {
		// The line did not reach stderr either: give back this minute's
		// budget so the next failure tries again.
		l.lastStderrWarn.CompareAndSwap(now, last)
	}
}

// Close stops fan-out to taps, lets every tap deliver what it has queued, and
// returns when they all have, or ctx.Err() when ctx expires first; then each
// tap's context is cancelled and its goroutine exits after its current call.
// The primary output keeps working (the kernel owns and closes it), and a Tap
// registered after Close receives nothing. Close runs last in App shutdown
// (05-logging §4 step 4). A second call returns nil.
func (l *Logging) Close(ctx context.Context) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	ts := *l.taps.Load()
	l.taps.Store(&[]*tap{})
	l.mu.Unlock()

	for _, t := range ts {
		t.beginDrain()
	}
	for _, t := range ts {
		select {
		case <-t.done:
		case <-ctx.Done():
			for _, t := range ts {
				t.cancel()
			}
			return ctx.Err()
		}
	}
	return nil
}
