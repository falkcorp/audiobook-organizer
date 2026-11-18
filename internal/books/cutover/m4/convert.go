// file: internal/books/cutover/m4/convert.go
// version: 1.0.0
// guid: c41d9e07-5a3b-48f2-b6c8-0d7e2f91a354
// last-edited: 2026-11-18

package m4

import (
	"bytes"
	"encoding/json"
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
	reasonRowBlob    = "a plan row of the run is not JSON"
	reasonOrphan     = "the row belongs to no run record"
	reasonOfRunQuar  = "the row belongs to a run that is quarantined"
	reasonMarkFormat = "a mark key of the run has no name separator"
)

// runRows is everything HEAD stores for one run, read by prefix.
type runRows struct {
	id string
	// run is the driver row; prog, ckpt and ledger are the one-row families.
	run, prog, ckpt, ledger *storage.KV
	evt, intent, item, log  []storage.KV
	snap, mark, rowmark     []storage.KV
	row, rowix              []storage.KV
}

// all lists every row of the run, driver included, for deletion or quarantine.
func (r *runRows) all() []storage.KV {
	var out []storage.KV
	for _, one := range []*storage.KV{r.run, r.prog, r.ckpt, r.ledger} {
		if one != nil {
			out = append(out, *one)
		}
	}
	for _, many := range [][]storage.KV{r.evt, r.intent, r.item, r.log, r.snap, r.mark, r.rowmark, r.row, r.rowix} {
		out = append(out, many...)
	}
	return out
}

// converted is a run ready to import.
type converted struct {
	rec  tasks.RunRecord
	data tasks.RunData
}

// convert builds the importer's record and data for a run, or the reason the
// run is quarantined. It reads nothing but rr.
func convert(rr *runRows) (*converted, string) {
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
	if err := out.checkpoints(rr); err != "" {
		return nil, err
	}
	if err := out.rows(rr); err != "" {
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
	for _, kv := range rr.item {
		var it itemRow
		if json.Unmarshal(kv.Value, &it) != nil {
			return reasonPage
		}
		c.data.Pages = append(c.data.Pages, tasks.PageEntry{Page: tasks.PageItems, Key: it.Key, Item: tasks.ItemOutcome{Key: it.Key, Outcome: it.Outcome, Error: it.Error, Chunk: it.Chunk}})
	}
	for i, kv := range rr.log {
		var l logRow
		if json.Unmarshal(kv.Value, &l) != nil {
			return reasonPage
		}
		seq := logSeq(string(kv.Key))
		if seq == 0 {
			seq = uint64(i + 1)
		}
		c.data.Pages = append(c.data.Pages, tasks.PageEntry{Page: tasks.PageLog, Seq: seq, Log: tasks.LogLine{At: l.At.UTC(), Level: l.Level, Message: l.Message, Fields: l.Fields}})
	}
	return ""
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
func (c *converted) checkpoints(rr *runRows) string {
	c.data.Checkpoints = map[string][]byte{}
	if rr.ckpt != nil {
		var ck ckptRow
		if json.Unmarshal(rr.ckpt.Value, &ck) != nil {
			return reasonCkpt
		}
		if len(ck.State) > 0 && !bytes.Equal(ck.State, []byte("null")) {
			c.data.Checkpoints[CheckpointHead] = bytes.Clone(ck.State)
		}
		if len(ck.LegacyState) > 0 {
			c.data.Checkpoints[CheckpointV1] = bytes.Clone(ck.LegacyState)
		}
	}
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
		_ = json.Unmarshal(kv.Value, &ix) // the index is a summary: an unreadable one leaves Class empty
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
