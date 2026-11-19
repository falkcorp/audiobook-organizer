// file: internal/books/cutover/m4/convert.go
// version: 1.4.0
// guid: c41d9e07-5a3b-48f2-b6c8-0d7e2f91a354
// last-edited: 2026-11-18

package m4

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Names of the checkpoints a run carries (5T.3.6: HEAD's bytes, which the
// converted kind's Restore decodes or its converter rewrites).
const (
	// CheckpointHead is opv3:ckpt:'s rc.Checkpoint payload.
	CheckpointHead = "head"
	// CheckpointV1 is the migrated opv2:state: blob an opv3:ckpt: row kept
	// (LegacyState): the v1 checkpoint of 5T.8.3.
	CheckpointV1 = "v1"
	// CheckpointLedger and CheckpointSnapPrefix carry a Batch or Fixer run's
	// chunk ledger and item-key snapshot pages as HEAD wrote them (see the PR
	// description's open item on chunk mapping).
	CheckpointLedger     = "head-ledger"
	CheckpointSnapPrefix = "head-snap:"
	// CheckpointLegacyState and CheckpointV2State carry the v1-checkpoint
	// candidates that lost the precedence (opstate, then LegacyState, then
	// opv2:state), whole, so none of the three is discarded.
	CheckpointLegacyState = "head-legacy-state"
	CheckpointV2State     = "head-v2-state"
	// CheckpointItemSupersededPrefix carries an item outcome that lost to
	// another outcome of the same item key (see items).
	CheckpointItemSupersededPrefix = "head-item-superseded:"
	// CheckpointRunExtras is the JSON of the v3 record's fields the native run
	// record has no home for (see runExtras).
	CheckpointRunExtras = "head-run-extras"
)

// Quarantine reasons: fixed classes, never a decoder's text (a JSON error
// quotes the value it rejected).
const (
	reasonRun        = "the run record does not decode"
	reasonProg       = "the run's progress record does not decode"
	reasonID         = "the run record's ID is not its key's"
	reasonKind       = "the run's definition ID fails the kind grammar, or its params name no usable kind"
	reasonState      = "the run is in a state or stop reason 5T.8.3's table does not list"
	reasonCreated    = "the run has no queued time"
	reasonFinished   = "the run is terminal and HEAD recorded no time it finished"
	reasonPriority   = "the run's priority is outside 0 to 2"
	reasonPage       = "a row of the run's trail, intents, items or log does not decode"
	reasonCkpt       = "the run's checkpoint record does not decode"
	reasonLogV2      = "a v2 log or error line of the run does not decode"
	reasonOpV2       = "the run's v2 operation row is not JSON"
	reasonRowBlob    = "a plan row of the run is not JSON"
	reasonOrphan     = "the row belongs to no run record"
	reasonOfRunQuar  = "the row belongs to a run that is quarantined"
	reasonMarkFormat = "a mark key of the run has no name separator"
	reasonV1Op       = "the v1 operation row does not decode, or carries no ID, type or creation time"
	reasonV1Status   = "the v1 operation's status is not one 5T.8.3's v1 table lists"
	reasonV1Log      = "a v1 log line of the operation does not decode"
	reasonV1Params   = "opstate:<id>:params is the backfill-file-hashes chunk journal, a live record that no import consumes"
	reasonDuplicate  = "two rows of the run would import to the same page key, so one would silently replace the other"
	reasonV1Stray    = "the v1 operation has v3 or v2 rows but no v3 run record to import them under"
)

// CheckpointV1Result and CheckpointV1Summary carry HEAD's per-book result rows
// (op_result:<id>:<book>, keyed here by the book) and an operation's summary
// row (opsummary:<id>) as HEAD wrote them, for the operations that keep them.
const (
	CheckpointV1ResultPrefix = "head-v1-result:"
	CheckpointV1Summary      = "head-v1-summary"
	// CheckpointV1Operation is the v1 operation row of a run that also has a v3
	// record: the v3 record is the run's truth, and the v1 row's own fields
	// (message, folder, result text) ride along unread.
	CheckpointV1Operation = "head-v1-operation"
)

// V1Unfinished is the reason a v1 operation that never finished is imported
// as interrupted_dropped: v1 operations had no resume, so the run is history.
const V1Unfinished = "v1_unfinished"

// runRows is everything HEAD stores for one run, read by prefix.
type runRows struct {
	id string
	// run is the driver row; prog, ckpt and ledger are the one-row families.
	run, prog, ckpt, ledger *storage.KV
	evt, intent, item, log  []storage.KV
	snap, mark, rowmark     []storage.KV
	row, rowix              []storage.KV
	// The v2 mirror: the operation and resume-state rows, the log and error
	// lines.
	op2, state2 *storage.KV
	log2, err2  []storage.KV
	// The v1 history: the operation row (the driver of a run with no v3
	// record), its summary and checkpoint, and the log lines, per-book results
	// and checkpoint journal rows.
	op1, sum1, state1 *storage.KV
	log1, res1, jrnl1 []storage.KV
}

// all lists every row of the run, driver included, for deletion or quarantine.
func (r *runRows) all() []storage.KV {
	var out []storage.KV
	for _, one := range []*storage.KV{r.run, r.prog, r.ckpt, r.ledger, r.op2, r.state2, r.op1, r.sum1, r.state1} {
		if one != nil {
			out = append(out, *one)
		}
	}
	for _, many := range [][]storage.KV{r.evt, r.intent, r.item, r.log, r.snap, r.mark, r.rowmark, r.row, r.rowix, r.log2, r.err2, r.log1, r.res1, r.jrnl1} {
		out = append(out, many...)
	}
	return out
}

// converted is a run ready to import.
type converted struct {
	rec  tasks.RunRecord
	data tasks.RunData
	// superseded is how many item outcomes were carried as checkpoints because
	// another outcome of the same item won the item page (see items).
	superseded int
}

// convert builds the importer's record and data for a run, or the reason the
// run is quarantined. It reads nothing but rr.
func convert(rr *runRows) (*converted, string) {
	if rr.run == nil {
		return convertV1(rr)
	}
	var row runRow
	if err := json.Unmarshal(rr.run.Value, &row); err != nil {
		return nil, reasonRun
	}
	if row.ID == "" || row.ID != rr.id {
		return nil, reasonID
	}
	var prog progRow
	if rr.prog != nil {
		if err := json.Unmarshal(rr.prog.Value, &prog); err != nil {
			return nil, reasonProg
		}
	}
	tg, err := kindrename.Resolve(row.DefID)
	if err != nil {
		return nil, reasonKind
	}
	rt, err := tg.ForRun(row.Params)
	if err != nil {
		return nil, reasonKind
	}
	st, why, ok := MapState(row.State, row.WaitReason, row.StopReason, row.InterruptedWhy)
	if !ok {
		return nil, reasonState
	}
	if row.QueuedAt == nil || row.QueuedAt.IsZero() {
		return nil, reasonCreated
	}
	pri, ok := priority(row.Priority)
	if !ok {
		return nil, reasonPriority
	}

	out := &converted{}
	rec := &out.rec
	rec.ID, rec.Kind, rec.State, rec.Reason = tasks.RunID(row.ID), rt.Kind, st, why
	rec.Mode = rt.Mode
	switch row.Mode {
	case "preview":
		rec.Mode = tasks.ModePreview
	case "live":
		rec.Mode = tasks.ModeLive
	}
	if tg.Marker == kindrename.MarkerRepairsPlan || tg.Marker == kindrename.MarkerRepairsApply {
		rec.Mode = rt.Mode // rule 2: a plan is Preview, an apply is Live, whatever the record said
	}
	rec.Trigger = trigger(row.Trigger)
	rec.Actor = tasks.Actor{UserID: row.Actor.UserID, AuthMethod: row.Actor.AuthMethod}
	if row.Actor.AuthMethod == "system" && row.Actor.UserID == "" {
		rec.Actor.System = "books"
	}
	rec.Parent, rec.PlanRef = tasks.RunID(row.ParentID), rt.PlanRef
	rec.Created = row.QueuedAt.UTC()
	if row.StartedAt != nil {
		rec.Started = row.StartedAt.UTC()
	}
	rec.Done = int(prog.Progress.Done)
	if prog.Progress.Total != nil {
		rec.Total = int(*prog.Progress.Total)
	}
	rec.Current = prog.Progress.Current
	rec.Priority, rec.Params, rec.Result, rec.Error = pri, row.Params, row.Result, row.Error
	rec.Attempt, rec.FenceEpoch = row.Attempt, row.FenceEpoch

	if err := out.pages(rr); err != "" {
		return nil, err
	}
	if err := out.checkpoints(rr, !st.Terminal()); err != "" {
		return nil, err
	}
	if err := out.rows(rr); err != "" {
		return nil, err
	}
	if err := out.runExtras(rr); err != "" {
		return nil, err
	}

	if st.Terminal() {
		fin := finished(&row, &prog, out.lastEvent())
		if fin.IsZero() {
			return nil, reasonFinished
		}
		rec.Finished = fin.UTC()
	}
	rec.Seq.Event = max(row.EventSeq, out.maxSeq(tasks.PageEvents))
	rec.Seq.Intent = out.maxSeq(tasks.PageIntents)
	rec.Seq.Log = out.maxSeq(tasks.PageLog)
	return out, ""
}

// finished is when a terminal run finished. A run HEAD recorded as terminal has
// CompletedAt. A run the table turns terminal (a stopping run: HEAD never
// recorded a finish for it) finishes when its trail last moved, or failing
// that when its progress last did: both are HEAD's own timestamps, and neither
// is invented. No such time quarantines the run.
func finished(row *runRow, prog *progRow, lastEvent time.Time) time.Time {
	switch {
	case row.CompletedAt != nil && !row.CompletedAt.IsZero():
		return *row.CompletedAt
	case row.State == "stopping" && !lastEvent.IsZero():
		return lastEvent
	case row.State == "stopping" && prog.LastProgressAt != nil:
		return *prog.LastProgressAt
	}
	return time.Time{}
}

func priority(p int) (tasks.Priority, bool) {
	switch p {
	case 0:
		return tasks.PriorityLow, true
	case 1:
		return tasks.PriorityNormal, true
	case 2:
		return tasks.PriorityHigh, true
	}
	return 0, false
}

// trigger maps HEAD's trigger text. HEAD wrote "manual" and "scheduled"
// (internal/operations/trigger_source.go) and the v3 names the native engine
// shares; anything else is a manual start.
func trigger(s string) tasks.Trigger {
	switch s {
	case "scheduled":
		return tasks.TriggerSchedule
	case string(tasks.TriggerManual), string(tasks.TriggerSchedule), string(tasks.TriggerWindow),
		string(tasks.TriggerOnStart), string(tasks.TriggerOutbox), string(tasks.TriggerParent), string(tasks.TriggerResume):
		return tasks.Trigger(s)
	}
	return tasks.TriggerManual
}

func (c *converted) lastEvent() time.Time {
	var last time.Time
	for _, p := range c.data.Pages {
		if p.Page == tasks.PageEvents && p.Event.At.After(last) {
			last = p.Event.At
		}
	}
	return last
}

func (c *converted) maxSeq(pk tasks.PageKind) uint64 {
	var m uint64
	for _, p := range c.data.Pages {
		if p.Page == pk {
			m = max(m, p.Seq)
		}
	}
	return m
}

// pages decodes the run's trail, intents, items and log.
func (c *converted) pages(rr *runRows) string {
	for _, kv := range rr.evt {
		var t transitionRow
		if json.Unmarshal(kv.Value, &t) != nil {
			return reasonPage
		}
		ev := tasks.RunEvent{From: eventState(t.From), To: eventState(t.To), Event: t.Event, Reason: t.Reason, Attempt: t.Attempt, FenceEpoch: t.FenceEpoch, At: t.At.UTC()}
		if t.Actor != nil {
			ev.Actor = &tasks.Actor{UserID: t.Actor.UserID, AuthMethod: t.Actor.AuthMethod}
		}
		c.data.Pages = append(c.data.Pages, tasks.PageEntry{Page: tasks.PageEvents, Seq: t.Seq, Event: ev})
	}
	for _, kv := range rr.intent {
		var in intentRow
		if json.Unmarshal(kv.Value, &in) != nil {
			return reasonPage
		}
		sub := subjectRef(in.Subject)
		w := tasks.Write{Name: "books." + in.Kind, Resource: entity.Ref{Module: "books", Kind: sub.Kind}, Subject: sub}
		var ch []tasks.Change
		if len(in.Planned) > 0 {
			// HEAD's planned payload is a free-form blob; it rides as one change
			// so no byte of it is dropped.
			ch = []tasks.Change{{Subject: sub, Field: "planned", New: string(in.Planned)}}
		}
		c.data.Pages = append(c.data.Pages, tasks.PageEntry{Page: tasks.PageIntents, Seq: in.Seq, Write: w, Changes: ch})
	}
	if reason := c.items(rr); reason != "" {
		return reason
	}
	if reason := c.logs(rr); reason != "" {
		return reason
	}
	return c.unique()
}

// items imports the run's item outcomes. HEAD keys a row by outcome and item
// (opv3:item:<id>:<outcome>:<key>) and never removes the other outcome's row,
// so one item can hold a failed row and a skipped one; a native item page is
// keyed by the item alone. The rule: the outcome of the highest chunk is the
// item's (a later chunk is later processing: HEAD stamps no time on an outcome),
// and on a tie the greater outcome string wins, so "skipped:<reason>" follows
// "failed" (a failed attempt, then a skip on resume). Every other outcome of
// the item rides whole as a head-item-superseded:<outcome>:<key> checkpoint and
// is counted, so no outcome is lost.
func (c *converted) items(rr *runRows) string {
	byKey := map[string][]itemRow{}
	raw := map[[2]string][]byte{}
	var order []string
	for _, kv := range rr.item {
		var it itemRow
		if json.Unmarshal(kv.Value, &it) != nil {
			return reasonPage
		}
		if _, seen := byKey[it.Key]; !seen {
			order = append(order, it.Key)
		}
		byKey[it.Key] = append(byKey[it.Key], it)
		raw[[2]string{it.Key, it.Outcome}] = kv.Value
	}
	for _, key := range order {
		rows := byKey[key]
		win := 0
		for i, it := range rows {
			if it.Chunk > rows[win].Chunk || (it.Chunk == rows[win].Chunk && it.Outcome > rows[win].Outcome) {
				win = i
			}
		}
		w := rows[win]
		c.data.Pages = append(c.data.Pages, tasks.PageEntry{Page: tasks.PageItems, Key: w.Key, Item: tasks.ItemOutcome{Key: w.Key, Outcome: w.Outcome, Error: w.Error, Chunk: w.Chunk}})
		for i, it := range rows {
			if i == win {
				continue
			}
			if c.data.Checkpoints == nil {
				c.data.Checkpoints = map[string][]byte{}
			}
			c.data.Checkpoints[CheckpointItemSupersededPrefix+it.Outcome+":"+it.Key] = bytes.Clone(raw[[2]string{it.Key, it.Outcome}])
			c.superseded++
		}
	}
	return ""
}

// logEntry is one log line of any family, before the run's lines are numbered.
type logEntry struct {
	line tasks.LogLine
	rank int    // family: v3, v2 log, v2 error, v1
	src  uint64 // the family's own sequence number
	n    int    // arrival order, the last tie-break
}

// logs imports every log line of the run, from the v3 log, the v2 mirror's log
// and error lines and the v1 log, as one page numbered 1..n in HEAD's merged
// order. HEAD numbers a line from an in-memory counter that restarts at 1 every
// process start (the key stays unique only through its timestamp), and the v3,
// v2 and v1 families number independently, so a key's own number is not unique
// within a run: two lines from different boots would share a page key and one
// would replace the other. The order is HEAD's ListLogV3 order (time, then the
// source sequence) with the family as the tie-break between families: v3 lines,
// v2 lines, v2 errors, v1 lines. A line that does not decode quarantines the
// run: a half-imported log is not history.
func (c *converted) logs(rr *runRows) string {
	var all []logEntry
	add := func(at time.Time, rank int, key []byte, line tasks.LogLine) {
		line.At = at.UTC()
		all = append(all, logEntry{line: line, rank: rank, src: logSeq(string(key)), n: len(all)})
	}
	for _, kv := range rr.log {
		var l logRow
		if json.Unmarshal(kv.Value, &l) != nil {
			return reasonPage
		}
		add(l.At, 0, kv.Key, tasks.LogLine{Level: l.Level, Message: l.Message, Fields: l.Fields})
	}
	for _, kv := range rr.log2 {
		var l opLogV2
		if json.Unmarshal(kv.Value, &l) != nil {
			return reasonLogV2
		}
		add(l.CreatedAt, 1, kv.Key, tasks.LogLine{Level: l.Level, Message: l.Message, Fields: attrFields(l.Attrs)})
	}
	for _, kv := range rr.err2 {
		var e opErrV2
		if json.Unmarshal(kv.Value, &e) != nil {
			return reasonLogV2
		}
		f := attrFields(e.Attrs)
		for k, v := range map[string]string{"plugin": e.Plugin, "def_id": e.DefID} {
			if _, taken := f[k]; !taken && v != "" {
				if f == nil {
					f = map[string]string{}
				}
				f[k] = v
			}
		}
		add(e.OccurredAt, 2, kv.Key, tasks.LogLine{Level: "error", Message: e.Message, Fields: f})
	}
	for _, kv := range rr.log1 {
		var l opLog1Row
		if json.Unmarshal(kv.Value, &l) != nil {
			return reasonV1Log
		}
		var fields map[string]string
		if l.Details != nil {
			fields = attrFields(*l.Details)
		}
		add(l.CreatedAt, 3, kv.Key, tasks.LogLine{Level: l.Level, Message: l.Message, Fields: fields})
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case !a.line.At.Equal(b.line.At):
			return a.line.At.Before(b.line.At)
		case a.rank != b.rank:
			return a.rank < b.rank
		case a.src != b.src:
			return a.src < b.src
		}
		return a.n < b.n
	})
	for i, e := range all {
		c.data.Pages = append(c.data.Pages, tasks.PageEntry{Page: tasks.PageLog, Seq: uint64(i + 1), Log: e.line})
	}
	return ""
}

// unique refuses a run whose pages would collide: two events or intents with one
// sequence number, two items with one key, two log lines with one number. An
// import writes a page with a plain put, so the second would replace the first
// without a trace; a run that gets here is quarantined whole instead.
func (c *converted) unique() string {
	type pageKey struct {
		page tasks.PageKind
		seq  uint64
		key  string
	}
	seen := map[pageKey]bool{}
	for _, p := range c.data.Pages {
		k := pageKey{page: p.Page, seq: p.Seq, key: p.Key}
		if seen[k] {
			return reasonDuplicate
		}
		seen[k] = true
	}
	return ""
}

// attrFields is a v2 line's Attrs (a JSON object kept as text) as log fields:
// a string stays itself and any other value is its JSON text. Text that is not
// a JSON object rides whole under "attrs" rather than being dropped.
func attrFields(attrs string) map[string]string {
	if attrs == "" || attrs == "{}" || attrs == "null" {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(attrs), &m) != nil || m == nil {
		return map[string]string{"attrs": attrs}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
			continue
		}
		out[k] = string(v)
	}
	return out
}

// logSeq is the sequence a log key carries: opv3:log:<id>:<nanos>:<seq>.
func logSeq(key string) uint64 {
	i := strings.LastIndexByte(key, ':')
	if i < 0 {
		return 0
	}
	n, err := strconv.ParseUint(key[i+1:], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// subjectRef is the entity a HEAD intent subject ("book:<id>",
// "book_file:<id>") names.
func subjectRef(s string) entity.Ref {
	kind, id, _ := strings.Cut(s, ":")
	switch kind {
	case "book":
		return entity.Ref{Module: "books", Kind: "books", ID: id}
	case "book_file":
		return entity.Ref{Module: "books", Kind: "book_files", ID: id}
	}
	return entity.Ref{Module: "books", Kind: kind, ID: id}
}

// checkpoints carries HEAD's checkpoint, ledger and snapshot bytes.
func (c *converted) checkpoints(rr *runRows, open bool) string {
	if c.data.Checkpoints == nil { // items may already have carried superseded outcomes
		c.data.Checkpoints = map[string][]byte{}
	}
	var legacy []byte
	if rr.ckpt != nil {
		var ck ckptRow
		if json.Unmarshal(rr.ckpt.Value, &ck) != nil {
			return reasonCkpt
		}
		if len(ck.State) > 0 && !bytes.Equal(ck.State, []byte("null")) {
			c.data.Checkpoints[CheckpointHead] = bytes.Clone(ck.State)
		}
		legacy = ck.LegacyState
	}
	// The run's checkpoint named v1 (5T.8.3), by precedence: HEAD's own v1
	// checkpoint (operations.SaveCheckpoint's opstate:<id>) while the run is
	// open, since it is what the converted kind's Restore reads; else the copy
	// of opv2:state: an opv3:ckpt: row kept as LegacyState (whole row bytes);
	// else opv2:state: itself, for a run whose state was written after that copy
	// or whose checkpoint row is missing. A candidate that loses is carried whole
	// under its own name, never discarded. A finished run's opstate: has nothing
	// to resume and is deleted with it.
	switch {
	case open && rr.state1 != nil:
		c.data.Checkpoints[CheckpointV1] = bytes.Clone(rr.state1.Value)
		if len(legacy) > 0 {
			c.data.Checkpoints[CheckpointLegacyState] = bytes.Clone(legacy)
		}
		if rr.state2 != nil {
			c.data.Checkpoints[CheckpointV2State] = bytes.Clone(rr.state2.Value)
		}
	case len(legacy) > 0:
		c.data.Checkpoints[CheckpointV1] = bytes.Clone(legacy)
		if rr.state2 != nil {
			c.data.Checkpoints[CheckpointV2State] = bytes.Clone(rr.state2.Value)
		}
	case rr.state2 != nil:
		c.data.Checkpoints[CheckpointV1] = bytes.Clone(rr.state2.Value)
	}
	c.v1Extras(rr)
	if rr.ledger != nil {
		c.data.Checkpoints[CheckpointLedger] = bytes.Clone(rr.ledger.Value)
	}
	for _, kv := range rr.snap {
		suffix := strings.TrimPrefix(string(kv.Key), prefixSnap+rr.id+":")
		c.data.Checkpoints[CheckpointSnapPrefix+suffix] = bytes.Clone(kv.Value)
	}
	if len(c.data.Checkpoints) == 0 {
		c.data.Checkpoints = nil
	}
	return ""
}

// rows carries the Fixer's plan rows and the applied marks.
func (c *converted) rows(rr *runRows) string {
	index := map[string]string{}
	ixPrefix := prefixRowIx + rr.id + ":"
	for _, kv := range rr.rowix {
		var ix rowIndex
		if json.Unmarshal(kv.Value, &ix) != nil {
			ix = rowIndex{} // the index is a summary: an unreadable one leaves Class empty
		}
		index[strings.TrimPrefix(string(kv.Key), ixPrefix)] = ix.Class
	}
	rowPrefix := prefixRow + rr.id + ":"
	for _, kv := range rr.row {
		if !json.Valid(kv.Value) {
			return reasonRowBlob
		}
		id := strings.TrimPrefix(string(kv.Key), rowPrefix)
		c.data.Rows = append(c.data.Rows, tasks.Row{ID: id, Class: index[id], State: bytes.Clone(kv.Value)})
	}
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			c.data.Marks = append(c.data.Marks, id)
		}
	}
	rmPrefix := prefixRowMark + rr.id + ":"
	for _, kv := range rr.rowmark {
		add(strings.TrimPrefix(string(kv.Key), rmPrefix))
	}
	mPrefix := prefixMark + rr.id + ":"
	for _, kv := range rr.mark {
		_, key, ok := strings.Cut(strings.TrimPrefix(string(kv.Key), mPrefix), "\x00")
		if !ok {
			return reasonMarkFormat
		}
		add(key)
	}
	return ""
}

// v1Extras carries the v1 result rows and the summary row under their
// checkpoint names, byte for byte, so no byte of a v1 operation's output is
// dropped.
func (c *converted) v1Extras(rr *runRows) {
	for _, kv := range rr.res1 {
		book := strings.TrimPrefix(string(kv.Key), prefixOpRes1+rr.id+":")
		c.data.Checkpoints[CheckpointV1ResultPrefix+book] = bytes.Clone(kv.Value)
	}
	if rr.sum1 != nil {
		c.data.Checkpoints[CheckpointV1Summary] = bytes.Clone(rr.sum1.Value)
	}
	if rr.run != nil && rr.op1 != nil {
		c.data.Checkpoints[CheckpointV1Operation] = bytes.Clone(rr.op1.Value)
	}
}

// v1Status is HEAD's v1 operation statuses: the ones that finished map to the
// state they mean, the ones that never finished (v1 had no resume) are history
// that was cut off.
func v1Status(s string) (st tasks.RunState, why string, ok bool) {
	switch s {
	case "completed":
		return tasks.StateSucceeded, "", true
	case "failed":
		return tasks.StateFailed, "", true
	case "timed_out":
		return tasks.StateTimedOut, "", true
	case "canceled", "cancelled":
		return tasks.StateCanceled, "", true
	case "pending", "queued", "running", "processing", "in_progress", "paused", "interrupted":
		return tasks.StateInterruptedDropped, V1Unfinished, true
	}
	return "", "", false
}

// convertV1 imports a v1 operation that has no v3 record as a terminal run:
// its kind goes through the rename map (an ID the map does not know is kept as
// books.retired.<type>, rule 5), its status through v1Status, its log lines
// become the run's log page, and its input folder rides in the run's params.
// An operation that never finished finishes when its last recorded activity
// does: the latest of its log lines, its start and its creation, all of them
// HEAD's own times.
func convertV1(rr *runRows) (*converted, string) {
	var op op1Row
	if rr.op1 == nil || json.Unmarshal(rr.op1.Value, &op) != nil || op.ID != rr.id || op.Type == "" || op.CreatedAt.IsZero() {
		return nil, reasonV1Op
	}
	kind, err := v1Kind(op.Type)
	if err != nil {
		return nil, reasonKind
	}
	st, why, ok := v1Status(op.Status)
	if !ok {
		return nil, reasonV1Status
	}
	out := &converted{}
	rec := &out.rec
	rec.ID, rec.Kind, rec.State, rec.Reason = tasks.RunID(op.ID), kind, st, why
	rec.Mode, rec.Trigger, rec.Priority = tasks.ModeLive, tasks.TriggerManual, tasks.PriorityNormal
	rec.Actor = tasks.Actor{UserID: op.UserID}
	if op.UserID == "" {
		rec.Actor.System = "books"
	}
	rec.Created = op.CreatedAt.UTC()
	if op.StartedAt != nil {
		rec.Started = op.StartedAt.UTC()
	}
	rec.Done, rec.Total = op.Progress, op.Total
	if op.ErrorMessage != nil {
		rec.Error = *op.ErrorMessage
	}
	if op.ResultData != nil {
		if rec.Result, err = jsonOrString(*op.ResultData); err != nil {
			return nil, reasonV1Op
		}
	}
	rec.Params = json.RawMessage(`{}`)
	if op.FolderPath != nil {
		raw, err := json.Marshal(map[string]string{"folder_path": *op.FolderPath})
		if err != nil {
			return nil, reasonV1Op
		}
		rec.Params = raw
	}
	// A v1 operation is the only record of its ID, so no v3 or v2 row can be
	// imported under it; any such row is a stray the whole run is set aside for.
	for _, fam := range [][]storage.KV{rr.evt, rr.intent, rr.item, rr.log, rr.snap, rr.mark, rr.rowmark, rr.row, rr.rowix, rr.log2, rr.err2} {
		if len(fam) > 0 {
			return nil, reasonV1Stray
		}
	}
	for _, one := range []*storage.KV{rr.prog, rr.ckpt, rr.ledger, rr.state2} {
		if one != nil {
			return nil, reasonV1Stray
		}
	}
	out.data.Checkpoints = map[string][]byte{}
	if reason := out.logs(rr); reason != "" {
		return nil, reason
	}
	if reason := out.unique(); reason != "" {
		return nil, reason
	}
	out.v1Extras(rr)
	if len(out.data.Checkpoints) == 0 {
		out.data.Checkpoints = nil
	}
	fin := time.Time{}
	switch {
	case why == V1Unfinished:
		fin = op.CreatedAt
		if op.StartedAt != nil && op.StartedAt.After(fin) {
			fin = *op.StartedAt
		}
		for _, p := range out.data.Pages {
			if p.Log.At.After(fin) {
				fin = p.Log.At
			}
		}
	case op.CompletedAt != nil && !op.CompletedAt.IsZero():
		fin = *op.CompletedAt
	default:
		return nil, reasonFinished
	}
	rec.Finished = fin.UTC()
	rec.Seq.Log = out.maxSeq(tasks.PageLog)
	return out, ""
}

// v1Kind is the kind of a v1 operation by its type text: the rename map's
// answer when the text is a def ID it knows, else the retired kind (history
// only). A def ID whose kind needs a run's params (the repairs defs) cannot be
// a v1 operation's type and keeps the retired kind too.
func v1Kind(typ string) (tasks.Kind, error) {
	tg, err := kindrename.Resolve(typ)
	if err != nil {
		return "", err
	}
	if rt, err := tg.ForRun(nil); err == nil {
		return rt.Kind, nil
	}
	return kindrename.RetiredKind(typ)
}

// jsonOrString is a v1 text field as a JSON value: itself when it is valid
// JSON, else the text as a JSON string.
func jsonOrString(s string) (json.RawMessage, error) {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s), nil
	}
	return json.Marshal(s)
}

// runExtras carries the v3 record's fields the native run record has no field
// for, as one JSON checkpoint (head-run-extras): the approval (its note exists
// nowhere else), the count of item outcomes HEAD refused past its cap (so an
// imported run still says its item list was truncated) and the pipeline stage,
// idempotency key, successor, trace and span identifiers. A run that has none
// carries nothing.
func (c *converted) runExtras(rr *runRows) string {
	var x runExtrasRow
	if json.Unmarshal(rr.run.Value, &x) != nil {
		return reasonRun
	}
	if x.ItemOutcomes != nil && x.ItemOutcomes.Dropped == 0 {
		x.ItemOutcomes = nil // a run that stored every outcome has nothing to say
	}
	if x.empty() {
		return ""
	}
	raw, err := json.Marshal(x)
	if err != nil {
		return reasonRun
	}
	if c.data.Checkpoints == nil {
		c.data.Checkpoints = map[string][]byte{}
	}
	c.data.Checkpoints[CheckpointRunExtras] = raw
	return ""
}
