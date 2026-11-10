// file: core/internal/taskstore/keys.go
// version: 1.1.0
// guid: c2337f4c-99d8-4f8b-ba15-6aca8b3118e4
// last-edited: 2026-11-10

// Package taskstore is the key layout and the raw staging of core's one task
// store, the c/tasks/ keyspace (05-tasks 5T.6, 5T.6.1). It knows keys, index
// maintenance and the Serialized guard keys, and handles values only as
// bytes: package tasks encodes the typed records (codec.JSON of the core
// type) and holds every rule about what a write means. taskstore never
// imports tasks, so tasks.NativeStores can call into it without a cycle.
//
// Every key is a keys.Join tuple; the table's "/" separates segments:
//
//	run/<run_id>                                  RunRecord, the run's guard key (Serialized)
//	q/<^priority>/<enqueued>/<run_id>             dispatch queue entry (PrioritySystem first)
//	ix/kind/<kind>/<run_id>                       kind index
//	ix/state/<state>/<run_id>                     state index; Counts counts it
//	ix/done/<finished>/<run_id>                   terminal runs by finish time (retention)
//	page/<run_id>/{evt,intent,item,log}/<seq|key> per-run pages
//	ckpt/<run_id>/<name>                          checkpoints
//	chunk/<run_id>/<n>                            chunk ledger
//	row/<run_id>/<row_id>                         Fixer rows
//	mark/<run_id>/<row_id>                        applied row marks
//	debounce/<kind>/<bucket>                      debounce bucket (Serialized)
//	strike/<run_id>/<n>                           watchdog strikes
//	sched/<kind>                                  schedule clock (Serialized)
//	pause                                         the pause marker
//	standdown/<key>                               stand-down markers
//	req/<module>/<kind>/<id>/<taskkind>           Requires stamps
//	req-rev/<taskkind>/<module>/<kind>/<id>       their reverse index
//	def/<kind>                                    definition snapshots (the kernel writes them)
//	idem/<kind>/<key>                             HTTP start idempotency records (Serialized; Client.Start writes them)
//	_seq/strike                                   the strike-number sequence (storage's Keyspace.Sequence key)
//
// The Serialized sub-prefixes are exactly the guard keys run/, debounce/,
// sched/ and idem/ (5T.6.1): every other family is written only in a batch
// that carries its run's guard Expect, so storage refuses a DeleteRange only
// over a guard family and allows it over the rest (05-storage §5S.3.4).
package taskstore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is the storage owner of the c/tasks/ keyspace the kernel registers.
const Owner storage.Owner = "core/tasks"

// Family segments: the first segment of every key.
const (
	famRun       = "run"
	famQueue     = "q"
	famIndex     = "ix"
	famPage      = "page"
	famCkpt      = "ckpt"
	famChunk     = "chunk"
	famRow       = "row"
	famMark      = "mark"
	famDebounce  = "debounce"
	famStrike    = "strike"
	famSched     = "sched"
	famPause     = "pause"
	famStandDown = "standdown"
	famReq       = "req"
	famReqRev    = "req-rev"
	famDef       = "def"
	famIdem      = "idem"

	ixKind  = "kind"
	ixState = "state"
	ixDone  = "done"
)

// Page names one of a run's pages: the segment under page/<run_id>/.
type Page string

// The four pages of a run.
const (
	PageEvents  Page = "evt"
	PageIntents Page = "intent"
	PageItems   Page = "item"
	PageLog     Page = "log"
)

// StorageSpec is the c/tasks/ spec the kernel registers for Owner: a core
// keyspace whose guard families, and only those, are Serialized (5T.6.1).
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: SerializedPrefixes()}
}

// SerializedPrefixes are the relative Serialized sub-prefixes: the guard
// families run/, debounce/, sched/ and idem/.
func SerializedPrefixes() []string {
	return []string{
		string(keys.Prefix(keys.Str(famRun))),
		string(keys.Prefix(keys.Str(famDebounce))),
		string(keys.Prefix(keys.Str(famSched))),
		string(keys.Prefix(keys.Str(famIdem))),
	}
}

// RunKey is run/<id>, the run's guard key.
func RunKey(id string) []byte { return keys.Join(keys.Str(famRun), keys.Str(id)) }

// RunPrefix is the run/ family.
func RunPrefix() []byte { return keys.Prefix(keys.Str(famRun)) }

// QueueKey is q/<^priority>/<enqueued>/<id>. The priority segment is the
// complement of priority, so a higher priority (PrioritySystem) sorts first;
// within a priority, the earlier enqueue time sorts first.
func QueueKey(priority uint8, enqueued time.Time, id string) []byte {
	return keys.Join(keys.Str(famQueue), keys.Bytes([]byte{^priority}), keys.Time(enqueued), keys.Str(id))
}

// QueuePrefix is the q/ family.
func QueuePrefix() []byte { return keys.Prefix(keys.Str(famQueue)) }

// KindIndexKey is ix/kind/<kind>/<id>.
func KindIndexKey(kind, id string) []byte {
	return keys.Join(keys.Str(famIndex), keys.Str(ixKind), keys.Str(kind), keys.Str(id))
}

// KindIndexPrefix is ix/kind/<kind>/.
func KindIndexPrefix(kind string) []byte {
	return keys.Prefix(keys.Str(famIndex), keys.Str(ixKind), keys.Str(kind))
}

// StateIndexKey is ix/state/<state>/<id>.
func StateIndexKey(state, id string) []byte {
	return keys.Join(keys.Str(famIndex), keys.Str(ixState), keys.Str(state), keys.Str(id))
}

// StateIndexPrefix is ix/state/<state>/, or ix/state/ for an empty state.
func StateIndexPrefix(state string) []byte {
	if state == "" {
		return keys.Prefix(keys.Str(famIndex), keys.Str(ixState))
	}
	return keys.Prefix(keys.Str(famIndex), keys.Str(ixState), keys.Str(state))
}

// DoneIndexKey is ix/done/<finished>/<id>.
func DoneIndexKey(finished time.Time, id string) []byte {
	return keys.Join(keys.Str(famIndex), keys.Str(ixDone), keys.Time(finished), keys.Str(id))
}

// DoneIndexPrefix is ix/done/.
func DoneIndexPrefix() []byte { return keys.Prefix(keys.Str(famIndex), keys.Str(ixDone)) }

// PageKey is page/<id>/<page>/<cursor>; cursor is SeqSeg(seq) for events,
// intents and log lines, and keys.Str(key) for item outcomes.
func PageKey(id string, p Page, cursor keys.Seg) []byte {
	return keys.Join(keys.Str(famPage), keys.Str(id), keys.Str(string(p)), cursor)
}

// PagePrefix is page/<id>/<page>/.
func PagePrefix(id string, p Page) []byte {
	return keys.Prefix(keys.Str(famPage), keys.Str(id), keys.Str(string(p)))
}

// SeqSeg is the segment of a page sequence number: 8 bytes big-endian, so
// the page reads in seq order.
func SeqSeg(seq uint64) keys.Seg { return keys.U64(seq) }

// CheckpointKey is ckpt/<id>/<name>.
func CheckpointKey(id, name string) []byte {
	return keys.Join(keys.Str(famCkpt), keys.Str(id), keys.Str(name))
}

// CheckpointPrefix is ckpt/<id>/.
func CheckpointPrefix(id string) []byte { return keys.Prefix(keys.Str(famCkpt), keys.Str(id)) }

// ChunkKey is chunk/<id>/<n>.
func ChunkKey(id string, n uint64) []byte {
	return keys.Join(keys.Str(famChunk), keys.Str(id), keys.U64(n))
}

// ChunkPrefix is chunk/<id>/.
func ChunkPrefix(id string) []byte { return keys.Prefix(keys.Str(famChunk), keys.Str(id)) }

// RowKey is row/<id>/<rowID>.
func RowKey(id, rowID string) []byte {
	return keys.Join(keys.Str(famRow), keys.Str(id), keys.Str(rowID))
}

// RowPrefix is row/<id>/.
func RowPrefix(id string) []byte { return keys.Prefix(keys.Str(famRow), keys.Str(id)) }

// RowIDPrefix is the start of every row/<id>/<rowID> whose rowID begins with
// idPrefix: the escaped bytes of idPrefix without the segment terminator.
// keys.Join escapes byte by byte, so a row ID that begins with idPrefix has
// an encoding that begins with the encoding of idPrefix.
func RowIDPrefix(id, idPrefix string) []byte {
	k := keys.Join(keys.Str(famRow), keys.Str(id), keys.Str(idPrefix))
	return k[:len(k)-2] // drop the terminator 0x00 0x01
}

// MarkKey is mark/<id>/<rowID>.
func MarkKey(id, rowID string) []byte {
	return keys.Join(keys.Str(famMark), keys.Str(id), keys.Str(rowID))
}

// MarkPrefix is mark/<id>/.
func MarkPrefix(id string) []byte { return keys.Prefix(keys.Str(famMark), keys.Str(id)) }

// DebounceKey is debounce/<kind>/<bucket>.
func DebounceKey(kind, bucket string) []byte {
	return keys.Join(keys.Str(famDebounce), keys.Str(kind), keys.Str(bucket))
}

// DebouncePrefix is debounce/<kind>/, or debounce/ for an empty kind.
func DebouncePrefix(kind string) []byte {
	if kind == "" {
		return keys.Prefix(keys.Str(famDebounce))
	}
	return keys.Prefix(keys.Str(famDebounce), keys.Str(kind))
}

// StrikeKey is strike/<id>/<n>.
func StrikeKey(id string, n uint64) []byte {
	return keys.Join(keys.Str(famStrike), keys.Str(id), keys.U64(n))
}

// StrikePrefix is strike/<id>/.
func StrikePrefix(id string) []byte { return keys.Prefix(keys.Str(famStrike), keys.Str(id)) }

// SchedKey is sched/<kind>, a kind's schedule clock.
func SchedKey(kind string) []byte { return keys.Join(keys.Str(famSched), keys.Str(kind)) }

// PauseKey is pause, the executor's pause marker.
func PauseKey() []byte { return keys.Join(keys.Str(famPause)) }

// StandDownKey is standdown/<key>.
func StandDownKey(key string) []byte { return keys.Join(keys.Str(famStandDown), keys.Str(key)) }

// StandDownPrefix is standdown/.
func StandDownPrefix() []byte { return keys.Prefix(keys.Str(famStandDown)) }

// Subject is the entity a Requires stamp is about (entity.Ref's fields;
// taskstore stays below the entity package's users).
type Subject struct{ Module, Kind, ID string }

// ReqKey is req/<module>/<kind>/<id>/<taskkind>.
func ReqKey(s Subject, taskKind string) []byte {
	return keys.Join(keys.Str(famReq), keys.Str(s.Module), keys.Str(s.Kind), keys.Str(s.ID), keys.Str(taskKind))
}

// ReqRevKey is req-rev/<taskkind>/<module>/<kind>/<id>.
func ReqRevKey(taskKind string, s Subject) []byte {
	return keys.Join(keys.Str(famReqRev), keys.Str(taskKind), keys.Str(s.Module), keys.Str(s.Kind), keys.Str(s.ID))
}

// ReqRevPrefix is req-rev/<taskkind>/.
func ReqRevPrefix(taskKind string) []byte {
	return keys.Prefix(keys.Str(famReqRev), keys.Str(taskKind))
}

// DefKey is def/<kind>, a definition snapshot (5T.3.11). Its writer is the
// kernel's registration step (5T.4 step 1), not this package's. The kind
// lives in the key only: the stored value must not embed it, because
// Importer.RenameKind moves the snapshot to def/<to> as bytes, unread.
func DefKey(kind string) []byte { return keys.Join(keys.Str(famDef), keys.Str(kind)) }

// IdemKey is idem/<kind>/<key>, an HTTP start idempotency record (5T.6).
// Its writer is Client.Start, not this package's.
func IdemKey(kind, key string) []byte {
	return keys.Join(keys.Str(famIdem), keys.Str(kind), keys.Str(key))
}

// IdemPrefix is idem/<kind>/, or idem/ for an empty kind.
func IdemPrefix(kind string) []byte {
	if kind == "" {
		return keys.Prefix(keys.Str(famIdem))
	}
	return keys.Prefix(keys.Str(famIdem), keys.Str(kind))
}

// lastSeg returns the last segment of a key, or nil when the key does not
// split.
func lastSeg(key []byte) keys.Seg {
	segs, err := keys.Split(key)
	if err != nil || len(segs) == 0 {
		return nil
	}
	return segs[len(segs)-1]
}

// ErrMalformedKey: a key under a family does not have that family's shape.
var ErrMalformedKey = errors.New("taskstore: malformed key")

// parse splits key and checks that it is a key of family fam (with sub, the
// second segment, when sub is not empty) of exactly n segments.
func parse(key []byte, n int, fam, sub string) ([]keys.Seg, error) {
	segs, err := keys.Split(key)
	if err != nil || len(segs) != n || string(segs[0]) != fam || (sub != "" && string(segs[1]) != sub) {
		return nil, fmt.Errorf("%w: %q is not a %s/%s key", ErrMalformedKey, key, fam, sub)
	}
	return segs, nil
}

// ParseKindIndexKey is KindIndexKey's inverse.
func ParseKindIndexKey(key []byte) (kind, id string, err error) {
	segs, err := parse(key, 4, famIndex, ixKind)
	if err != nil {
		return "", "", err
	}
	return string(segs[2]), string(segs[3]), nil
}

// ParseStateIndexKey is StateIndexKey's inverse.
func ParseStateIndexKey(key []byte) (state, id string, err error) {
	segs, err := parse(key, 4, famIndex, ixState)
	if err != nil {
		return "", "", err
	}
	return string(segs[2]), string(segs[3]), nil
}

// ParseDoneIndexKey is DoneIndexKey's inverse; finished is in UTC.
func ParseDoneIndexKey(key []byte) (finished time.Time, id string, err error) {
	segs, err := parse(key, 4, famIndex, ixDone)
	if err != nil {
		return time.Time{}, "", err
	}
	if len(segs[2]) != 8 {
		return time.Time{}, "", fmt.Errorf("%w: done-index time of %q", ErrMalformedKey, key)
	}
	return time.Unix(0, int64(binary.BigEndian.Uint64(segs[2])^(1<<63))).UTC(), string(segs[3]), nil
}

// ParsePageKey is PageKey's inverse: cursor is the raw last segment
// (ParseSeqSeg reads a sequence number from it; for item outcomes it is the
// item key).
func ParsePageKey(key []byte) (id string, p Page, cursor keys.Seg, err error) {
	segs, err := parse(key, 4, famPage, "")
	if err != nil {
		return "", "", nil, err
	}
	return string(segs[1]), Page(segs[2]), segs[3], nil
}

// ParseSeqSeg is SeqSeg's inverse.
func ParseSeqSeg(seg keys.Seg) (uint64, error) {
	if len(seg) != 8 {
		return 0, fmt.Errorf("%w: a sequence segment is 8 bytes, not %d", ErrMalformedKey, len(seg))
	}
	return binary.BigEndian.Uint64(seg), nil
}

// ParseReqRevKey is ReqRevKey's inverse.
func ParseReqRevKey(key []byte) (taskKind string, s Subject, err error) {
	segs, err := parse(key, 5, famReqRev, "")
	if err != nil {
		return "", Subject{}, err
	}
	return string(segs[1]), Subject{Module: string(segs[2]), Kind: string(segs[3]), ID: string(segs[4])}, nil
}

// ParseIdemKey is IdemKey's inverse.
func ParseIdemKey(key []byte) (kind, idemKey string, err error) {
	segs, err := parse(key, 3, famIdem, "")
	if err != nil {
		return "", "", err
	}
	return string(segs[1]), string(segs[2]), nil
}
