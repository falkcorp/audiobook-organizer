// file: internal/activity/writer.go
// version: 1.7.0
// guid: c3d4e5f6-a7b8-4c9d-0e1f-2a3b4c5d6e7f
// last-edited: 2026-08-16

package activity

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Writer is an io.Writer that tees log output to stdout AND sends
// parsed entries through a buffered channel to an ActivityStore.
type Writer struct {
	stdout      io.Writer
	ch          chan database.ActivityEntry
	store       database.ActivityStorer
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
	w.batcher = NewActivityBatcher(w.ch)
	return w
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

// writeBatch persists a slice of entries to the store, ignoring individual errors.
// Each entry is enriched with derived tags (outcome:, source:, action:,
// lifecycle: for system entries) before persistence so the Activity Log UI
// has structured tags on every row — not just rows that went through
// Service.Record.
func (w *Writer) writeBatch(entries []database.ActivityEntry) {
	for _, e := range entries {
		EnrichTags(&e)
		w.store.Record(e) //nolint:errcheck
	}
}

// Flush synchronously drains any entries currently in the channel without
// stopping the background goroutine.
func (w *Writer) Flush() {
	w.batcher.FlushAll()
	for {
		select {
		case e := <-w.ch:
			EnrichTags(&e)
			w.store.Record(e) //nolint:errcheck
		default:
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
