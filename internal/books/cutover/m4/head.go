// file: internal/books/cutover/m4/head.go
// version: 1.0.1
// guid: 3b7e1c94-d2a6-4f08-95b1-6c0e8a47d213
// last-edited: 2026-11-18

package m4

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/mediacore/storage"
)

// HEAD's ops families (internal/books/store/keyfamilies.go; 05-storage 5S.9.1
// rows 11 to 15), as the step reads them. This package cannot import the store
// whose rows it retires (layering: a cutover step is a leaf), and HEAD's rows
// never change again, so the row types below are frozen copies of the fields
// the step reads; json ignores the rest. TestFrozenRowsMatchTheStore (field by
// field) holds each copy to the store's own type.
const (
	prefixRun     = "opv3:run:"
	prefixProg    = "opv3:prog:"
	prefixEvt     = "opv3:evt:"
	prefixCkpt    = "opv3:ckpt:"
	prefixLedger  = "opv3:ledger:"
	prefixSnap    = "opv3:snap:"
	prefixIntent  = "opv3:intent:"
	prefixItem    = "opv3:item:"
	prefixLog     = "opv3:log:"
	prefixMark    = "opv3:mark:"
	prefixRowMark = "opv3:rowmark:"
	prefixRow     = "opv2:row:"
	prefixRowIx   = "opv2:rowix:"

	// The four opv3:ix: indexes are claimed one by one (books' claims have no
	// claim over opv3:ix: itself), so a Source range over the bare prefix would
	// cross four claim boundaries.
	prefixIxDef    = "opv3:ix:def:"
	prefixIxDone   = "opv3:ix:done:"
	prefixIxParent = "opv3:ix:parent:"
	prefixIxState  = "opv3:ix:state:"
	prefixQueue    = "opv3:q:"
	prefixDef      = "opv3:def:"
	prefixMeta     = "opv3:meta:"
)

// runScoped are the families whose rows belong to one run and are keyed
// <prefix><run id>:... (or <prefix><run id> for the one-row families); the
// run's driver pulls them with the run.
var runScoped = []string{
	prefixProg, prefixEvt, prefixCkpt, prefixLedger, prefixSnap, prefixIntent,
	prefixItem, prefixLog, prefixMark, prefixRowMark, prefixRow, prefixRowIx,
}

// derived are the families deleted by rule: indexes HEAD rebuilt from the run
// record (the native store derives its own), definition rows (registration
// rebuilds c/tasks/def/) and the census row (native Counts is derived).
var derived = []string{prefixIxDef, prefixIxDone, prefixIxParent, prefixIxState, prefixQueue, prefixDef, prefixMeta}

// SourcePrefixes is every family prefix the step's Source covers, in key
// order. F5-18 retires exactly these claims.
func SourcePrefixes() []string {
	all := append([]string{prefixRun}, runScoped...)
	all = append(all, derived...)
	slices.Sort(all)
	return all
}

// SourceRanges is the step's Source: one range per family prefix.
func SourceRanges() []storage.Range {
	var out []storage.Range
	for _, p := range SourcePrefixes() {
		out = append(out, storage.Range{Start: []byte(p), End: prefixEnd(p)})
	}
	return out
}

// prefixEnd is the exclusive end of the keys that start with p.
func prefixEnd(p string) []byte {
	end := bytes.Clone([]byte(p))
	end[len(end)-1]++
	return end
}

// familyOf names the family prefix key belongs to and the run ID it carries.
// ok is false for a key outside the step's Source.
func familyOf(key string) (family, runID string, ok bool) {
	for _, p := range SourcePrefixes() {
		rest, found := strings.CutPrefix(key, p)
		if !found {
			continue
		}
		id, _, _ := strings.Cut(rest, ":")
		return p, id, true
	}
	return "", "", false
}

func isDerived(family string) bool {
	for _, p := range derived {
		if p == family {
			return true
		}
	}
	return false
}

// runRow is opv3:run: (RunRecord, store.iface_ops_v3.go), without the hot half
// (opv3:prog:).
type runRow struct {
	SchemaVersion  int             `json:"v"`
	ID             string          `json:"id"`
	DefID          string          `json:"def_id"`
	State          state.V3        `json:"state"`
	WaitReason     string          `json:"wait_reason"`
	StopReason     string          `json:"stop_reason"`
	InterruptedWhy string          `json:"interrupted_why"`
	Mode           string          `json:"mode"`
	Trigger        string          `json:"trigger"`
	Params         json.RawMessage `json:"params"`
	ParentID       string          `json:"parent_id"`
	Attempt        int             `json:"attempt"`
	FenceEpoch     uint64          `json:"fence_epoch"`
	Actor          headActor       `json:"actor"`
	Result         json.RawMessage `json:"result"`
	Error          string          `json:"error"`
	QueuedAt       *time.Time      `json:"queued_at"`
	StartedAt      *time.Time      `json:"started_at"`
	CompletedAt    *time.Time      `json:"completed_at"`
	Priority       int             `json:"priority"`
	EventSeq       uint64          `json:"evt_seq"`
}

type headActor struct {
	UserID     string `json:"user_id"`
	AuthMethod string `json:"auth_method"`
}

// progRow is opv3:prog: (runHot): the progress half of a run record.
type progRow struct {
	Progress struct {
		Done    int64  `json:"done"`
		Total   *int64 `json:"total"`
		Current string `json:"current"`
	} `json:"progress"`
	LastProgressAt *time.Time `json:"last_progress_at"`
}

// transitionRow is opv3:evt: (Transition).
type transitionRow struct {
	Seq        uint64     `json:"seq"`
	From       state.V3   `json:"from"`
	To         state.V3   `json:"to"`
	Event      string     `json:"event"`
	Reason     string     `json:"reason"`
	Attempt    int        `json:"attempt"`
	FenceEpoch uint64     `json:"fence_epoch"`
	Actor      *headActor `json:"actor"`
	At         time.Time  `json:"at"`
}

// intentRow is opv3:intent: (Intent).
type intentRow struct {
	Seq       uint64          `json:"seq"`
	Subject   string          `json:"subject"`
	Kind      string          `json:"kind"`
	Planned   json.RawMessage `json:"planned"`
	WrittenAt time.Time       `json:"written_at"`
}

// itemRow is opv3:item: (ItemOutcome).
type itemRow struct {
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
	Error   string `json:"error"`
	Chunk   int    `json:"chunk"`
}

// logRow is opv3:log: (LogLine).
type logRow struct {
	At      time.Time         `json:"at"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields"`
}

// ckptRow is opv3:ckpt: (Checkpoint).
type ckptRow struct {
	State       json.RawMessage `json:"state"`
	LegacyState []byte          `json:"legacy_state"`
}

// rowIndex is opv2:rowix: for a repairs plan row: the class a page filters by.
type rowIndex struct {
	Class string `json:"class"`
}
