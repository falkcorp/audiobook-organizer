// file: internal/activity/writer.go
// version: 1.8.2
// guid: c3d4e5f6-a7b8-4c9d-0e1f-2a3b4c5d6e7f
// last-edited: 2026-08-30

package activity

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// batchRecorder is the optional capability an activity store may offer: persist
// many entries with ONE durable commit instead of one commit per entry. A store
// that does not implement it is written to one entry at a time, exactly as
// before.
//
// WHY a narrow interface plus a runtime type assertion, and NOT a method on
// database.ActivityStorer — the placement question, answered rather than
// assumed. Widening the interface was measured, not estimated: adding
// RecordBatch to database.ActivityWriter and rebuilding breaks the four real
// implementations (Pebble, Nuts, DualWrite, Instrumented) plus exactly one test
// fake, which is a small enough blast radius that cost alone does not decide it.
//
// What decides it is the shape of the capability. The comment on
// RepairActivityIndexes argues for putting a backend-specific method ON the
// interface, and it is right about that method: repair has one caller, it MUST
// run, and there is nothing else it could do — so a type assertion that quietly
// missed would be a bug that says nothing, the failure shape this repo keeps
// getting burned by. RecordBatch is the opposite case. It is a PERFORMANCE
// capability with a complete, correct fallback: a store without it still
// records every entry, durably, with identical results — only slower. Nothing
// is lost when the assertion misses, so the objection to assertions does not
// apply, and the alternative would put a method on three implementations that
// can only fake it with a loop.
//
// The miss is not silent either: Start logs which path is live (see
// logBatchPath), so "is the batched write actually running in production" is a
// question the startup log answers rather than one requiring a profiler.
type batchRecorder interface {
	RecordBatch([]database.ActivityEntry) (int, error)
}

// flushChunkSize bounds how many entries Flush accumulates before writing them.
// It matches the store's own per-commit cap, so the common case is one commit
// and the bound only bites when producers outrun the drain. A var so tests can
// shrink it.
var flushChunkSize = 500

// The production store must satisfy batchRecorder, checked at COMPILE time.
//
// The runtime assertion in NewWriter is deliberate, but on its own it is not
// enough of a guarantee: every test fixture that "has" the capability declares
// its OWN RecordBatch method, so none of them can observe the real store losing
// it. If PebbleActivityStore.RecordBatch's signature ever drifted, or the
// method were renamed, prod would silently fall back to one fsync per entry and
// the whole suite would still pass. This line makes that a build failure.
var _ batchRecorder = (*database.PebbleActivityStore)(nil)

// Writer is an io.Writer that tees log output to stdout AND sends
// parsed entries through a buffered channel to an ActivityStore.
type Writer struct {
	stdout io.Writer
	ch     chan database.ActivityEntry
	store  database.ActivityStorer
	// batchStore is store again when it implements batchRecorder, else nil.
	// Resolved once at construction rather than asserted on every flush.
	batchStore  batchRecorder
	batcher     *ActivityBatcher
	done        chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	mu          sync.Mutex
	partial     string // incomplete line buffer
	closed      atomic.Bool
	skipSources map[string]struct{}
}

// NewWriter creates a new Writer backed by store.
// chanSize controls the depth of the internal entry buffer.
// By default the "gin" source is skipped — HTTP request logs are not
// useful as persistent activity entries and would flood the database.
func NewWriter(store database.ActivityStorer, chanSize int) *Writer {
	w := &Writer{
		stdout:      os.Stdout,
		ch:          make(chan database.ActivityEntry, chanSize),
		store:       store,
		done:        make(chan struct{}),
		skipSources: map[string]struct{}{"gin": {}},
	}
	if br, ok := store.(batchRecorder); ok {
		w.batchStore = br
	}
	w.batcher = NewActivityBatcher(w.ch)
	return w
}

// logBatchPath reports on stdout whether the batched write path is live for
// this writer's store.
//
// It writes to w.stdout rather than through slog for the same reason
// sendEntry's channel-full warning does: this Writer IS the destination the log
// system tees into, so logging from inside it feeds the message back into its
// own channel. One startup line would survive that; the loss report in
// writeBatch would not — see the comment there.
func (w *Writer) logBatchPath() {
	if w.batchStore != nil {
		w.stdout.Write([]byte("[INFO] activity: batched activity writes enabled (one commit per flush)\n")) //nolint:errcheck
		return
	}
	// WARN, not INFO: this is a real degradation, not a configuration note. It
	// costs two orders of magnitude of write throughput (one fsync per row
	// instead of one per flush), and it means some wrapper is intercepting the
	// store without forwarding RecordBatch — which is worth seeing named.
	w.stdout.Write([]byte("[WARN] activity: store has no batch write path, falling back to one fsync per entry\n")) //nolint:errcheck
}

// SetSkipSources replaces the set of log sources that are dropped before
// being written to the activity store. Entries are still printed to stdout.
// Call before Start(). Pass no arguments to disable all skipping.
func (w *Writer) SetSkipSources(sources ...string) {
	m := make(map[string]struct{}, len(sources))
	for _, s := range sources {
		m[s] = struct{}{}
	}
	w.skipSources = m
}

// Start launches the background drain goroutine. Call once before writing.
// Implements the Starter interface for serviceregistry.
func (w *Writer) Start(ctx context.Context) error {
	w.logBatchPath()
	w.wg.Add(1)
	go w.drain()
	return nil
}

// Write implements io.Writer. Always writes to stdout first, then parses
// complete lines and sends ActivityEntry values to the background drain.
func (w *Writer) Write(p []byte) (n int, err error) {
	n, err = w.stdout.Write(p)

	w.mu.Lock()
	defer w.mu.Unlock()

	data := w.partial + string(p)
	w.partial = ""

	for {
		idx := strings.IndexByte(data, '\n')
		if idx < 0 {
			w.partial = data
			break
		}
		line := strings.TrimRight(data[:idx], "\r")
		data = data[idx+1:]
		if line != "" {
			w.sendEntry(line)
		}
	}
	return n, nil
}

// isBatchable returns true if e should be routed through the ActivityBatcher
// rather than written directly to the channel. Entries are batchable only when
// they come from a structured LogBatch call (operationID non-empty) AND their
// type is registered as a high-volume batch type.
func isBatchable(e database.ActivityEntry) bool {
	if e.OperationID == "" || e.Tier != "debug" {
		return false
	}
	switch e.Type {
	case "embedded-tag-load", "tag-scan", "metadata-apply", "path-repair", "isbn-enrich":
		return true
	}
	return false
}

// sendEntry parses a single log line and enqueues an ActivityEntry.
// Debug entries are silently dropped when the channel is full.
// Non-debug entries emit a warning to stdout when dropped.
func (w *Writer) sendEntry(line string) {
	if w.closed.Load() {
		return
	}
	parsed := ParseLogLineFull(line)
	if _, skip := w.skipSources[parsed.Source]; skip {
		return
	}
	// Tier is derived from level. The Activity Log UI defaults to
	// "debug tier excluded", so persisting every line at tier=debug
	// meant the page always showed 0 entries even though writes were
	// happening. info/warn/error → change so the user sees actual
	// progress; debug stays debug for the firehose.
	tier := "change"
	switch parsed.Level {
	case "debug":
		tier = "debug"
	case "warn", "warning", "error":
		tier = "change"
	}
	entry := database.ActivityEntry{
		Tier:        tier,
		Type:        "system",
		Level:       parsed.Level,
		Source:      parsed.Source,
		Summary:     RenderSummary(parsed),
		OperationID: parsed.OpID,
	}
	// Propagate component into Details so EnrichTags can produce
	// a component: tag without requiring a DB schema change.
	if parsed.Component != "" {
		entry.Details = map[string]any{"component": parsed.Component}
	}
	// Keep the attrs structured as well as rendered. RenderSummary is for
	// reading; Details is what a consumer can actually query, and a path or a
	// book id is worth more as a field than as a substring of a sentence.
	if len(parsed.Attrs) > 0 {
		if entry.Details == nil {
			entry.Details = make(map[string]any, len(parsed.Attrs))
		}
		for _, a := range parsed.Attrs {
			if _, exists := entry.Details[a.Key]; !exists {
				entry.Details[a.Key] = a.Value
			}
		}
	}
	if isBatchable(entry) {
		w.batcher.Submit(BatchKey{
			Type:        entry.Type,
			Source:      entry.Source,
			OperationID: entry.OperationID,
		}, BatchItem{Name: entry.Summary})
		return
	}
	select {
	case w.ch <- entry:
	default:
		if parsed.Level != "debug" {
			w.stdout.Write([]byte("[WARN] activity channel full, dropped: " + parsed.Message + "\n")) //nolint:errcheck
		}
	}
}

// drain reads from the channel and persists entries in batches of up to 100,
// flushing at least every 500 ms. It stops when the done signal is received.
func (w *Writer) drain() {
	defer w.wg.Done()
	batch := make([]database.ActivityEntry, 0, 100)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case entry := <-w.ch:
			batch = append(batch, entry)
			if len(batch) >= 100 {
				w.writeBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				w.writeBatch(batch)
				batch = batch[:0]
			}
		case <-w.done:
			// Drain remaining entries before exiting.
		drainLoop:
			for {
				select {
				case entry := <-w.ch:
					batch = append(batch, entry)
				default:
					break drainLoop
				}
			}
			if len(batch) > 0 {
				w.writeBatch(batch)
			}
			return
		}
	}
}

// writeBatch persists a slice of entries to the store.
// Each entry is enriched with derived tags (outcome:, source:, action:,
// lifecycle: for system entries) before persistence so the Activity Log UI
// has structured tags on every row — not just rows that went through
// Service.Record.
//
// When the store offers the batchRecorder capability the whole slice goes down
// in one durable commit. It used to call store.Record once per entry, and
// Record fsyncs per call, so this batching layer amortized ROWS but not
// FSYNCS — a flush of 100 entries was 100 fsyncs. Measured on this repo at 5,000
// rows a side, same durability: 76-199 rows/sec that way against 27,440-54,531
// batched. See RecordBatch for why those are ranges.
//
// A store without the capability keeps the per-entry loop, which is slower but
// otherwise equivalent: it writes the same rows and, importantly, reports a
// lost row exactly the same way. Which path is live is logged once at Start.
func (w *Writer) writeBatch(entries []database.ActivityEntry) {
	for i := range entries {
		EnrichTags(&entries[i])
	}

	if w.batchStore != nil {
		written, err := w.batchStore.RecordBatch(entries)
		// The count comes from RecordBatch's return value, not from the error
		// text: it is the number of rows actually made durable, so the loss
		// reported here cannot overstate what survived. RecordBatch's own
		// message describes only the commit that failed, which on a multi-chunk
		// call is a subset of the loss — len(entries)-written is the whole of it.
		w.reportLoss(len(entries)-written, len(entries), err)
		return
	}

	// The fallback must not be quieter than the path it stands in for. Record
	// returns an error per entry and the pre-batch code discarded it, so a
	// store without the capability used to lose rows in total silence. Count
	// them and report through the same line, so "did we lose activity rows?"
	// has one answer regardless of which path ran.
	lost := 0
	var lastErr error
	for _, e := range entries {
		if _, err := w.store.Record(e); err != nil {
			lost++
			lastErr = err
		}
	}
	w.reportLoss(lost, len(entries), lastErr)
}

// reportLoss writes one line naming how many rows of a flush failed to reach
// the store. It is a no-op when nothing was lost.
//
// It reports on stdout, NOT through slog. This Writer is the io.Writer the log
// system tees into, so an slog call here would parse back into this same
// channel — and because this only runs when the store is failing, each failure
// would enqueue an entry whose own flush fails and logs again. A persistent
// disk error would become a write-amplification loop instead of a report.
// sendEntry's channel-full warning writes to stdout for the same reason.
func (w *Writer) reportLoss(lost, total int, err error) {
	if lost <= 0 && err == nil {
		return
	}
	w.stdout.Write([]byte(fmt.Sprintf( //nolint:errcheck
		"[WARN] activity: write lost %d of %d entries: %v\n", lost, total, err)))
}

// Flush synchronously drains any entries currently in the channel without
// stopping the background goroutine.
func (w *Writer) Flush() {
	w.batcher.FlushAll()
	// Drain into a slice and hand the whole thing to writeBatch rather than
	// recording entry by entry: Flush had the same one-fsync-per-entry shape
	// writeBatch did, and routing it through the same function means the two
	// cannot drift on enrichment, batching or loss reporting.
	//
	// The accumulator is BOUNDED. The per-entry version could not grow — it
	// committed each entry as it pulled it — whereas a drain that buffers
	// everything before writing grows without limit if producers keep filling
	// the channel while it runs, which is exactly the case Flush is called in
	// (scanner and iTunes shutdown, with work still in flight). Writing every
	// flushChunkSize entries keeps the batching win and restores the old code's
	// inability to balloon.
	pending := make([]database.ActivityEntry, 0, flushChunkSize)
	for {
		select {
		case e := <-w.ch:
			pending = append(pending, e)
			if len(pending) >= flushChunkSize {
				w.writeBatch(pending)
				pending = pending[:0]
			}
		default:
			if len(pending) > 0 {
				w.writeBatch(pending)
			}
			return
		}
	}
}

// Chan returns the underlying entry channel. Intended for use in tests only —
// callers should not write to this channel directly.
func (w *Writer) Chan() <-chan database.ActivityEntry {
	return w.ch
}

// Stop marks the writer as closed, signals the drain goroutine to finish,
// and waits for it to flush all remaining entries. Safe to call multiple times.
// Implements the Stopper interface for serviceregistry.
func (w *Writer) Stop(ctx context.Context) error {
	w.batcher.Close()
	w.closed.Store(true)
	w.stopOnce.Do(func() { close(w.done) })
	w.wg.Wait()
	return nil
}

// ── log line parser ───────────────────────────────────────────────────────────

// ParsedLogLine holds all fields extracted from a single log line.
type ParsedLogLine struct {
	Level     string // "info", "warn", "error", "debug"
	Source    string // subsystem name, e.g. "scanner", "server"
	Message   string // human-readable message text
	OpID      string // operation_id / op_id slog attribute, if present
	Component string // component / subsystem slog attribute or source-path derived name
	// Attrs are the slog key=value attributes other than the structural ones
	// (time/level/msg/source, and the op_id/component keys lifted into the
	// fields above), in the order they appeared.
	//
	// These used to be discarded outright, which is what produced activity-log
	// rows reading "cover art saved to" (to WHERE?) and "ISBN enrichment
	// succeeded for" (for WHAT?) -- the sentence is the slog MESSAGE and the
	// thing it is about is an ATTR.
	Attrs []LogAttr
}

// LogAttr is one slog key=value attribute from a log line.
type LogAttr struct {
	Key   string
	Value string
}

// structuralSlogKeys are the attrs that are already represented elsewhere in
// ParsedLogLine, so repeating them in a rendered summary would be noise.
var structuralSlogKeys = map[string]struct{}{
	"time": {}, "level": {}, "msg": {}, "source": {},
	"op_id": {}, "operation_id": {}, "opID": {},
	"component": {}, "subsystem": {}, "pkg": {},
}

// trailingPrepositions are the words that, when a log message ENDS with one,
// mean the message is a sentence fragment whose object is the first attr.
// "cover art saved to" + path=/lib/x.jpg reads correctly as
// "cover art saved to /lib/x.jpg", where "cover art saved to path=/lib/x.jpg"
// does not. Any other shape gets plain key=value appending, which is honest
// even when it is not elegant.
var trailingPrepositions = map[string]struct{}{
	"to": {}, "for": {}, "from": {}, "at": {}, "in": {},
	"on": {}, "of": {}, "with": {}, "into": {}, "by": {},
}

// RenderSummary builds the human-readable activity summary for a parsed line:
// the message, followed by the attrs that carry its actual content.
func RenderSummary(p ParsedLogLine) string {
	if len(p.Attrs) == 0 {
		return p.Message
	}
	msg := strings.TrimRight(p.Message, " ")
	attrs := p.Attrs

	// Sentence fragment ("... saved to", "... succeeded for", "... wrote:")
	// takes the first attr's value bare as its object.
	var b strings.Builder
	b.WriteString(msg)
	if msg != "" && endsWithPreposition(msg) {
		b.WriteString(" ")
		b.WriteString(attrs[0].Value)
		attrs = attrs[1:]
	}
	for _, a := range attrs {
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		b.WriteString(a.Value)
	}
	return strings.TrimSpace(b.String())
}

// endsWithPreposition reports whether msg's last word is a preposition, or msg
// ends in a colon — either way the sentence is left dangling without its object.
func endsWithPreposition(msg string) bool {
	if strings.HasSuffix(msg, ":") {
		return true
	}
	idx := strings.LastIndexByte(msg, ' ')
	if idx < 0 {
		return false
	}
	_, ok := trailingPrepositions[strings.ToLower(msg[idx+1:])]
	return ok
}

// scanSlogAttrs walks a slog TextHandler line and returns every key=value pair
// in order, correctly handling quoted values that themselves contain spaces,
// '=' or escaped quotes.
//
// This exists because the old parser located the message with
// strings.LastIndexByte(line, '"'), which finds the last quote in the WHOLE
// line rather than the message's own closing quote. With no quoted attrs after
// it that lands on the right character by luck; with one, the "message" swallows
// the rest of the line. That is why the activity log showed
//
//	ISBN enrichment found" isbn="9780553293357" title="Foundation
//
// complete with the stray quote — the same defect that elsewhere merely looked
// like "attributes are dropped".
func scanSlogAttrs(line string) []LogAttr {
	var attrs []LogAttr
	i := 0
	for i < len(line) {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		eq := -1
		for j := i; j < len(line); j++ {
			if line[j] == '=' {
				eq = j
				break
			}
			if line[j] == ' ' {
				break
			}
		}
		if eq < 0 {
			break
		}
		key := line[i:eq]
		i = eq + 1
		var val string
		if i < len(line) && line[i] == '"' {
			i++
			var sb strings.Builder
			for i < len(line) {
				if line[i] == '\\' && i+1 < len(line) {
					sb.WriteByte(line[i+1])
					i += 2
					continue
				}
				if line[i] == '"' {
					i++
					break
				}
				sb.WriteByte(line[i])
				i++
			}
			val = sb.String()
		} else {
			start := i
			for i < len(line) && line[i] != ' ' {
				i++
			}
			val = line[start:i]
		}
		if key != "" {
			attrs = append(attrs, LogAttr{Key: key, Value: val})
		}
	}
	return attrs
}

// isSlogTextLine reports whether the line is in slog TextHandler format.
func isSlogTextLine(line string) bool {
	return strings.HasPrefix(line, "time=") &&
		strings.Contains(line, " level=") &&
		strings.Contains(line, " msg=")
}

// ParseLogLineFull extracts all structured fields from a single log line,
// including op_id and component attributes when the line is in slog text format.
// ParseLogLine is a thin wrapper for callers that only need level/source/message.
func ParseLogLineFull(line string) ParsedLogLine {
	p := ParsedLogLine{}
	p.Level, p.Source, p.Message = parseLogLineCore(line)

	// For slog text lines, also extract op_id, component, and subsystem attrs,
	// and keep everything else: the content of these messages lives in the
	// attrs, and dropping them is what left the activity log saying "cover art
	// saved to" without saying where.
	if isSlogTextLine(line) {
		p.OpID = extractSlogAttr(line, "op_id", "operation_id", "opID")
		p.Component = extractSlogAttr(line, "component", "subsystem", "pkg")
		for _, a := range scanSlogAttrs(line) {
			if _, structural := structuralSlogKeys[a.Key]; structural {
				continue
			}
			if a.Value == "" {
				continue
			}
			p.Attrs = append(p.Attrs, a)
		}
	}

	// If no explicit component, derive one from the source path field when the
	// slog Source attr includes a file path (e.g., "internal/plugins/acoustid/scan.go").
	if p.Component == "" {
		p.Component = deriveComponentFromSource(p.Source)
	}
	return p
}

// extractSlogAttr scans the slog key=value attrs in a text-format log line for
// any of the supplied key names and returns the first matching value. Values may
// be quoted or bare. Returns "" if none match.
func extractSlogAttr(line string, keys ...string) string {
	for _, key := range keys {
		needle := " " + key + "="
		idx := strings.Index(line, needle)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(needle):]
		if rest == "" {
			continue
		}
		if rest[0] == '"' {
			// Quoted value: find the closing quote (skip escaped quotes).
			i := 1
			for i < len(rest) {
				if rest[i] == '\\' {
					i += 2
					continue
				}
				if rest[i] == '"' {
					return rest[1:i]
				}
				i++
			}
			continue
		}
		// Bare value: ends at next space.
		if sp := strings.IndexByte(rest, ' '); sp > 0 {
			return rest[:sp]
		}
		return rest
	}
	return ""
}

// deriveComponentFromSource maps known source file path segments to a
// component name. Returns "" when no known prefix matches, so we don't
// emit a misleading tag for unrecognised sources.
func deriveComponentFromSource(source string) string {
	lower := strings.ToLower(source)
	switch {
	case strings.Contains(lower, "acoustid") || strings.Contains(lower, "acousticid"):
		return "acoustid"
	case strings.Contains(lower, "itunes"):
		return "itunes_sync"
	case strings.Contains(lower, "scanner"):
		return "scanner"
	case strings.Contains(lower, "dedup"):
		return "dedup"
	case strings.Contains(lower, "isbn"):
		return "isbn_enrich"
	case strings.Contains(lower, "embed"):
		return "embedding"
	case strings.Contains(lower, "scheduler"):
		return "scheduler"
	case strings.Contains(lower, "maintenance"):
		return "maintenance"
	default:
		return ""
	}
}

// ParseLogLine extracts (level, source, message) from a single log line.
//
// Recognised formats:
//   - GIN: "[GIN] YYYY/MM/DD - HH:MM:SS | STATUS | ..."
//   - slog text: `time=... level=INFO msg="..."`
//   - Go standard log: "YYYY/MM/DD HH:MM:SS file.go:NNN: [level] source: message"
//   - Bare text: returned as-is with level=info, source=server.
func ParseLogLine(line string) (level, source, message string) {
	p := ParseLogLineFull(line)
	return p.Level, p.Source, p.Message
}

func parseLogLineCore(line string) (level, source, message string) {
	// GIN logs: [GIN] YYYY/MM/DD - HH:MM:SS | STATUS | ...
	if strings.HasPrefix(line, "[GIN]") {
		rest := line[5:]
		if idx := strings.Index(rest, "| "); idx >= 0 {
			message = strings.TrimSpace(rest[idx+2:])
		} else {
			message = strings.TrimSpace(rest)
		}
		return "info", "gin", message
	}

	// slog TextHandler: time=... level=INFO msg="..." [attrs...]
	// We only care about level + msg; drop time and attrs. After
	// extracting msg, recurse so the wrapped "[INFO] source: message"
	// payload parses through the standard [level] branch and gets a
	// proper source.
	if isSlogTextLine(line) {
		// One structured scan for the whole line rather than three independent
		// string searches. The previous msg extraction used
		// strings.LastIndexByte(msg, '"'), which takes the last quote in the
		// REST OF THE LINE -- correct only when no attribute after msg is
		// quoted, and otherwise swallowing every following attr into the
		// message, stray quote included.
		lvl := "info"
		msg := ""
		for _, a := range scanSlogAttrs(line) {
			switch a.Key {
			case "level":
				lvl = strings.ToLower(a.Value)
			case "msg":
				msg = a.Value
			}
		}
		// Recurse: msg often starts with "[INFO] source: ..." in our
		// code so the standard branch can extract a real source.
		if msg != "" && msg != line {
			rlvl, rsrc, rmsg := ParseLogLine(msg)
			if rsrc != "server" || rlvl != "info" {
				return rlvl, rsrc, rmsg
			}
			return lvl, "server", msg
		}
		return lvl, "server", msg
	}

	work := line

	// Strip date/time prefix: "YYYY/MM/DD HH:MM:SS" (19 chars + space)
	if len(work) > 20 && work[4] == '/' && work[7] == '/' && work[10] == ' ' {
		// Find "file:line: " part after timestamp and skip past it.
		if idx := strings.Index(work[20:], ": "); idx >= 0 {
			work = work[20+idx+2:]
		} else {
			work = work[20:]
		}
	}

	work = strings.TrimSpace(work)

	// Check for [level] prefix
	if len(work) > 2 && work[0] == '[' {
		if end := strings.Index(work, "] "); end > 0 {
			level = strings.ToLower(work[1:end])
			work = work[end+2:]
			// Check for "source: message" — source must look like a subsystem name:
			// short (< 30 chars), no spaces, typically lowercase with hyphens/underscores
			if idx := strings.Index(work, ": "); idx > 0 && idx < 30 && !strings.Contains(work[:idx], " ") {
				source = work[:idx]
				message = work[idx+2:]
				return level, source, message
			}
			return level, "server", work
		}
	}

	return "info", "server", work
}
