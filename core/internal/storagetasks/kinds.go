// file: core/internal/storagetasks/kinds.go
// version: 1.0.2
// guid: 451daab0-74d7-4793-a071-0a041b865144
// last-edited: 2026-11-18

// Package storagetasks declares the two core task kinds over the store:
// core.storage.compact (a full compaction) and core.storage.census (the exact
// per-partition count, recorded in c/storage/census/). 05-storage §5S.4 "Task
// kinds".
//
// It is not core/storage because storage is L1 and cannot import tasks (03
// §3.5 R4). This package is the bridge: L4, imports storage and tasks, reaches
// the store only through the Surface seam (which *storage.Admin satisfies) and
// is wired by the kernel. It registers nothing itself, so the kinds are dark
// until the kernel's F5-18 wiring.
package storagetasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
)

const (
	// KindCompact is the compaction kind (05-storage §5S.4).
	KindCompact tasks.Kind = "core.storage.compact"
	// KindCensus is the census kind (05-storage §5S.4).
	KindCensus tasks.Kind = "core.storage.census"

	// KeyStorageExclusive is the ConcurrencyKey the store-wide passes share
	// with backup (05-tasks 5T.3.4): one of them runs at a time.
	KeyStorageExclusive tasks.Key = "core.storage.exclusive"

	// CompactScheduleKey is the schedule key of compaction: a
	// tasks.ScheduleConfig in the core.storage section, declared by the
	// kernel's core-section list (storage, at L1, cannot hold the type). This
	// package only passes it to tasks.ScheduleFromConfig.
	CompactScheduleKey = "core.storage.schedules.compact"

	// CompactTimeout is HEAD's maintenance.db-optimize timeout.
	CompactTimeout = 2 * time.Hour

	// CensusTimeout is HEAD's maintenance.db-census-exact timeout. It is also
	// the kind's ProgressTimeout: storage.Admin.Census is one blocking call
	// that reports nothing while it counts, so the only silence the watchdog
	// can be given is the attempt's own limit. A census that wedges is still
	// ended by Timeout; it is not ended earlier. A heartbeat from a side
	// goroutine would stamp liveness without work behind it, which is the
	// masking storage.Moved exists to prevent.
	CensusTimeout = 12 * time.Hour
)

// Surface is the part of *storage.Admin the kinds use. A nil Surface yields
// definitions with no body, which registration refuses.
type Surface interface {
	Compact(ctx context.Context, start, end []byte) error
	CompactionStats() storage.CompactionStats
	Census(ctx context.Context) ([]storage.CensusEntry, error)
}

var _ Surface = (*storage.Admin)(nil)

// CensusSink records a finished census.
type CensusSink interface {
	Put(ctx context.Context, c Census) error
}

// Options tunes the kinds; the zero value is production.
type Options struct {
	// Interval is how often a running compaction is sampled (0 =
	// storage.DefaultInterval).
	Interval time.Duration
	// Now is the clock the census record is stamped with (nil = time.Now).
	Now func() time.Time
}

// CompactParams and CensusParams are empty: both passes are store-wide.
type (
	CompactParams struct{}
	CensusParams  struct{}
)

// CompactResult is what a finished compaction measured. Live table bytes
// are the before/after figure that means something (DiskSpaceUsage still
// counts files Pebble deletes asynchronously).
type CompactResult struct {
	ElapsedMS       int64  `json:"elapsed_ms"`
	HasStats        bool   `json:"has_stats"`
	LiveBytesBefore int64  `json:"live_bytes_before"`
	LiveBytesAfter  int64  `json:"live_bytes_after"`
	WrittenBytes    uint64 `json:"written_bytes"`
}

// CensusResult is the totals of a recorded census.
type CensusResult struct {
	Partitions int    `json:"partitions"`
	Keys       uint64 `json:"keys"`
	Bytes      uint64 `json:"bytes"`
}

// CompactTask returns core.storage.compact over s. A nil s yields a definition
// whose Run is nil, which tasks.Problems reports, so registration fails closed.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks when the tasks flip F5 registers core kinds (F5-18); this package registers nothing
func CompactTask(s Surface, o Options) tasks.Definition {
	var run func(rc *tasks.RC, _ CompactParams) (CompactResult, error)
	if s != nil {
		run = func(rc *tasks.RC, _ CompactParams) (CompactResult, error) {
			return compact(rc, s, o.Interval)
		}
	}
	return tasks.Task(KindCompact, tasks.TaskSpec[CompactParams, CompactResult]{
		Title: "Compact the main store",
		Help: "Compacts the whole key range of the main store, reclaiming the space deleted and overwritten rows still hold. " +
			"One pass at a time with backup and the census.",
		Effects: tasks.Writes(tasks.Res("core", "storage")),
		// core:tasks.manage, not core:system.manage: the scheduler starts
		// this kind as core's system actor, and the executor strips every
		// AuthRequired scope (core:system.manage among them) from a system
		// actor's set (SEC2-15), so a kind that needed it would never fire
		// on its schedule. Same choice as core.outbox.relay.
		Perms:          tasks.Permissions{Start: rbac.Need("core:tasks.manage")},
		Resume:         tasks.ResumeDrop,
		Priority:       tasks.PriorityLow,
		Timeout:        CompactTimeout,
		ConcurrencyKey: KeyStorageExclusive,
		Schedule:       tasks.ScheduleFromConfig(CompactScheduleKey).Live(),
		Run:            run,
	})
}

func compact(rc *tasks.RC, s Surface, interval time.Duration) (CompactResult, error) {
	rep := runReporter{rc: rc}
	res := storage.RunStep(rc.Context(), rep, storage.Step{
		Label: "Compacting main database",
		Run:   func(ctx context.Context) error { return s.Compact(ctx, nil, nil) },
		Stats: s.CompactionStats,
		// Frame is the one call that stamps liveness. RunStep sends it only
		// when an engine counter moved (storage.Moved), so a wedged
		// compaction goes quiet and the watchdog sees it.
		Frame:    rc.Progress().Note,
		Interval: interval,
		Log:      rc.Log(),
	})
	out := CompactResult{
		ElapsedMS: res.Elapsed.Milliseconds(),
		HasStats:  res.HasStats,
	}
	if res.HasStats {
		out.LiveBytesBefore = res.Before.LiveTableSize
		out.LiveBytesAfter = res.After.LiveTableSize
		out.WrittenBytes = max(res.After.WrittenBytes(), res.Before.WrittenBytes()) - res.Before.WrittenBytes()
	}
	if res.Err != nil {
		return out, fmt.Errorf("storagetasks: compact: %w", res.Err)
	}
	return out, nil
}

// runReporter adapts a run to storage.LogReporter. Log goes to the run's
// logger, which does not stamp liveness. SetCurrentItem is dropped: the
// run's current item is set by the frame, and a label every tick would
// publish progress the engine did not make.
type runReporter struct{ rc *tasks.RC }

func (r runReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	r.rc.Log().LogAttrs(r.rc.Context(), level, message, attrs...)
	return nil
}

func (runReporter) SetCurrentItem(string) {}

// CensusTask returns core.storage.census over s, recording into sink. A nil s
// or sink yields a definition with no body, which registration refuses.
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks when the tasks flip F5 registers core kinds (F5-18); this package registers nothing
func CensusTask(s Surface, sink CensusSink, o Options) tasks.Definition {
	var run func(rc *tasks.RC, _ CensusParams) (CensusResult, error)
	if s != nil && sink != nil {
		now := o.Now
		if now == nil {
			now = time.Now
		}
		run = func(rc *tasks.RC, _ CensusParams) (CensusResult, error) {
			return census(rc, s, sink, now)
		}
	}
	return tasks.Task(KindCensus, tasks.TaskSpec[CensusParams, CensusResult]{
		Title: "Count the store exactly",
		Help: "Counts every key and byte of the store by partition and records the result as the latest census. " +
			"One pass at a time with compaction and backup.",
		Effects: tasks.Writes(tasks.Res("core", "storage")),
		// Operator-run only (no Schedule), so it takes the storage
		// administration scope; a system actor never starts it.
		Perms:           tasks.Permissions{Start: rbac.Need("core:system.manage")},
		Resume:          tasks.ResumeFromZero,
		Timeout:         CensusTimeout,
		ProgressTimeout: CensusTimeout,
		ConcurrencyKey:  KeyStorageExclusive,
		Run:             run,
	})
}

func census(rc *tasks.RC, s Surface, sink CensusSink, now func() time.Time) (CensusResult, error) {
	ctx := rc.Context()
	entries, err := s.Census(ctx)
	if err != nil {
		return CensusResult{}, fmt.Errorf("storagetasks: census: %w", err)
	}
	// A canceled run that still got its entries back must not record them:
	// the last good census stays the latest.
	if err := ctx.Err(); err != nil {
		return CensusResult{}, err
	}
	rec := Census{At: now().UTC(), Partitions: make([]Partition, 0, len(entries))}
	for _, e := range entries {
		p := Partition{Owner: string(e.Owner), Start: e.Start, End: e.End, Keys: e.Keys, Bytes: e.Bytes}
		if e.Mode != storage.ClaimInvalid {
			p.Mode = e.Mode.String()
		}
		rec.Partitions = append(rec.Partitions, p)
		rec.Keys += e.Keys
		rec.Bytes += e.Bytes
	}
	if err := sink.Put(ctx, rec); err != nil {
		return CensusResult{}, fmt.Errorf("storagetasks: record census: %w", err)
	}
	return CensusResult{Partitions: len(rec.Partitions), Keys: rec.Keys, Bytes: rec.Bytes}, nil
}

// Census is the recorded result of one census pass: c/storage/census/latest.
type Census struct {
	At         time.Time   `json:"at"`
	Keys       uint64      `json:"keys"`
	Bytes      uint64      `json:"bytes"`
	Partitions []Partition `json:"partitions"`
}

// Partition is one census row: a keyspace or a claimed range. Mode is empty
// for a keyspace or unowned keys.
type Partition struct {
	Owner string `json:"owner,omitempty"`
	Mode  string `json:"mode,omitempty"`
	Start []byte `json:"start"`
	End   []byte `json:"end"`
	Keys  uint64 `json:"keys"`
	Bytes uint64 `json:"bytes"`
}

// censusKey is the one record, relative to c/storage/ (notation §5S.3.3).
// Each pass overwrites it: the census is the latest count, not a history.
func censusKey() []byte { return keys.Join(keys.Str("census"), keys.Str("latest")) }

func censusCodec() codec.Codec[Census] { return codec.JSON[Census](1) }

// CensusStore keeps the census in storage's own keyspace c/storage/. A
// census result is cheap to recompute, so the write is NoSync (05-storage
// §5S.4): a crash may lose the latest record and the next pass rewrites it.
type CensusStore struct{ ks storage.Keyspace }

// NewCensusStore returns the store over ks, core storage's own keyspace.
func NewCensusStore(ks storage.Keyspace) *CensusStore { return &CensusStore{ks: ks} }

// Put overwrites the latest census.
func (c *CensusStore) Put(ctx context.Context, rec Census) (err error) {
	raw, err := censusCodec().Encode(rec)
	if err != nil {
		return err
	}
	b := c.ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }() // releases the batch when Set fails; a no-op after Commit
	if err := b.Set(censusKey(), raw); err != nil {
		return err
	}
	return b.Commit(ctx, storage.NoSync)
}

// Latest reads the last recorded census; ok is false when none was recorded.
func (c *CensusStore) Latest(ctx context.Context) (rec Census, ok bool, err error) {
	raw, err := c.ks.Get(ctx, censusKey())
	if errors.Is(err, storage.ErrNotFound) {
		return rec, false, nil
	}
	if err != nil {
		return rec, false, err
	}
	rec, err = censusCodec().Decode(raw)
	if err != nil {
		return rec, false, fmt.Errorf("storagetasks: census record unreadable: %w", err)
	}
	return rec, true, nil
}
