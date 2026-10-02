// file: internal/compactprogress/step.go
// version: 1.0.1
// guid: 0e9c7d31-5a4b-4f62-8b1e-7c3d9a2f6e15
// last-edited: 2026-10-02

package compactprogress

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// DefaultInterval is how often a running step is sampled in production. The
// owner asked for "something more" than one line per half hour, and every 10s
// is frequent enough to watch without flooding the op log (~170 lines for the
// 28-minute production compaction). The runner itself has no floor so tests
// can use millisecond intervals; ops pass this constant.
const DefaultInterval = 10 * time.Second

// LogReporter is the slice of the operations Reporter a step needs. Narrow on
// purpose: registry.Reporter (and sdk.Reporter, an alias of it) satisfy it,
// and a test fake is three methods.
type LogReporter interface {
	Log(level slog.Level, message string, attrs ...slog.Attr) error
	SetCurrentItem(label string)
}

// Step is one blocking unit of work (normally one store's Compact) plus the
// means to describe it while it runs.
type Step struct {
	// Label names the step in every line, e.g. "Compacting main database (1/3)".
	Label string
	// Run is the blocking work. It must honour ctx.
	Run func(ctx context.Context) error
	// Stats samples the engine. Nil means the store exposes no counters; the
	// step then reports elapsed time only.
	Stats func() Stats
	// Frame publishes a progress frame (UpdateProgress). It is called ONLY
	// when an engine counter moved since the previous frame -- see Moved.
	// Nil means the caller does not want intermediate frames.
	Frame func(message string)
	// Interval between samples; <= 0 means DefaultInterval.
	Interval time.Duration
}

// Result is what a finished step measured.
type Result struct {
	Elapsed  time.Duration
	Before   Stats
	After    Stats
	HasStats bool
	Err      error
}

var stepLog = logger.New("db-optimize")

// Moved reports whether the engine did real work between two samples.
//
// This is the liveness rule the operations registry depends on. The watchdog
// cancels an op whose progress frames stop for ProgressTimeout, and a frame
// sent on every tick regardless of work would blind it to a genuinely wedged
// compaction (registry/types.go, LivenessManual). So frames go out only when
// Pebble's own counters say work happened: bytes were written by a
// compaction (WrittenBytes -- this one moves DURING a single long
// compaction), a compaction completed (Count), or the debt estimate fell.
// The log line and the current-item label go out on every tick either way;
// neither stamps liveness, so they cannot mask a wedge.
func Moved(prev, cur Stats) bool {
	return cur.WrittenBytes() > prev.WrittenBytes() ||
		cur.Count > prev.Count ||
		cur.EstimatedDebt < prev.EstimatedDebt
}

// RunStep runs s.Run on the calling goroutine while a sampler goroutine
// reports on it every s.Interval. The sampler exits when Run returns or ctx
// is cancelled, whichever is first, and RunStep waits for it before
// returning, so no goroutine outlives the call and the reporter is never used
// concurrently with the completion line.
func RunStep(ctx context.Context, rep LogReporter, s Step) Result {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	var res Result
	res.HasStats = s.Stats != nil
	if res.HasStats {
		res.Before = s.Stats()
	}
	start := time.Now()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sample(ctx, rep, s, interval, start, res.Before, stop)
	}()

	// stop+wait in a closure with a defer so a panicking Run still stops the
	// sampler before the panic propagates.
	func() {
		defer func() {
			close(stop)
			wg.Wait()
		}()
		res.Err = s.Run(ctx)
	}()

	res.Elapsed = time.Since(start)
	if res.HasStats {
		res.After = s.Stats()
	}
	line := FormatDone(s.Label, res)
	level := slog.LevelInfo
	if res.Err != nil {
		level = slog.LevelError
		stepLog.Error("%s", line)
	} else {
		stepLog.Info("%s", line)
	}
	_ = rep.Log(level, line)
	rep.SetCurrentItem("")
	return res
}

func sample(ctx context.Context, rep LogReporter, s Step, interval time.Duration, start time.Time, base Stats, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	prev := base
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		elapsed := time.Since(start)
		var line string
		if s.Stats == nil {
			line = fmt.Sprintf("%s: %s elapsed (this store exposes no compaction counters)", s.Label, formatDuration(elapsed))
			_ = rep.Log(slog.LevelInfo, line)
			rep.SetCurrentItem(line)
			continue
		}
		cur := s.Stats()
		line = FormatTick(s.Label, elapsed, base, cur)
		_ = rep.Log(slog.LevelInfo, line)
		rep.SetCurrentItem(line)
		if s.Frame != nil && Moved(prev, cur) {
			s.Frame(line)
			prev = cur
		}
	}
}

// FormatTick renders one in-flight sample, e.g.
//
//	Compacting main database (1/3): 4m10s elapsed, 18.2 GiB compacted (73.0 MiB/s),
//	3 compactions in progress (1.1 GiB), est. debt 6.4 GiB, L0 12 files,
//	41.3 GiB on disk, ~1,234 tombstones
//
// "compacted" is bytes written by compactions since the step began, including
// output of compactions still running. It is shown as bytes and a rate, never
// as a percentage: a full compaction can rewrite data more than once on its
// way down the levels, so no counter Pebble exposes is a trustworthy denominator.
func FormatTick(label string, elapsed time.Duration, base, cur Stats) string {
	written := delta(cur.WrittenBytes(), base.WrittenBytes())
	parts := []string{
		fmt.Sprintf("%s elapsed", formatDuration(elapsed)),
		fmt.Sprintf("%s compacted (%s)", HumanBytes(written), rate(written, elapsed)),
	}
	if cur.NumInProgress > 0 {
		parts = append(parts, fmt.Sprintf("%d %s in progress (%s)",
			cur.NumInProgress, plural(cur.NumInProgress, "compaction", "compactions"), HumanBytes(nonNeg(cur.InProgressBytes))))
	} else {
		parts = append(parts, "no compaction running")
	}
	if done := cur.Count - base.Count; done > 0 {
		parts = append(parts, fmt.Sprintf("%d finished", done))
	}
	parts = append(parts,
		fmt.Sprintf("est. debt %s", HumanBytes(cur.EstimatedDebt)),
		fmt.Sprintf("L0 %d %s", cur.L0Files, plural(cur.L0Files, "file", "files")),
		fmt.Sprintf("%s on disk", HumanBytes(cur.DiskSpaceUsage)),
		fmt.Sprintf("~%s tombstones", groupDigits(cur.TombstoneCount)),
	)
	return label + ": " + strings.Join(parts, ", ")
}

// FormatDone renders the completion line for a step.
//
// Live table size leads because it is the before/after figure that means
// something: DiskSpaceUsage still counts superseded files that Pebble deletes
// asynchronously, so right after Compact returns it can read HIGHER than when
// the step began. It is shown separately with the obsolete bytes it includes.
func FormatDone(label string, r Result) string {
	var b strings.Builder
	b.WriteString(label)
	if r.Err != nil {
		fmt.Fprintf(&b, ": failed after %s: %v", formatDuration(r.Elapsed), r.Err)
	} else {
		fmt.Fprintf(&b, ": done in %s", formatDuration(r.Elapsed))
	}
	if !r.HasStats {
		return b.String()
	}
	fmt.Fprintf(&b, "; live tables %s -> %s (%s)",
		HumanBytes(nonNeg(r.Before.LiveTableSize)), HumanBytes(nonNeg(r.After.LiveTableSize)),
		signedBytes(r.After.LiveTableSize-r.Before.LiveTableSize))
	fmt.Fprintf(&b, ", on disk %s -> %s", HumanBytes(r.Before.DiskSpaceUsage), HumanBytes(r.After.DiskSpaceUsage))
	if r.After.ObsoleteSize > 0 {
		fmt.Fprintf(&b, " (incl. %s obsolete, deleted asynchronously)", HumanBytes(r.After.ObsoleteSize))
	}
	fmt.Fprintf(&b, ", ~%s -> ~%s tombstones", groupDigits(r.Before.TombstoneCount), groupDigits(r.After.TombstoneCount))
	written := delta(r.After.WrittenBytes(), r.Before.WrittenBytes())
	fmt.Fprintf(&b, ", %s rewritten (%s)", HumanBytes(written), rate(written, r.Elapsed))
	return b.String()
}

// HumanBytes renders a byte count with binary units.
func HumanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func signedBytes(d int64) string {
	if d < 0 {
		return "-" + HumanBytes(uint64(-d))
	}
	return "+" + HumanBytes(uint64(d))
}

func rate(bytes uint64, elapsed time.Duration) string {
	if elapsed < time.Second {
		return "rate n/a"
	}
	return HumanBytes(uint64(float64(bytes)/elapsed.Seconds())) + "/s"
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func delta(cur, base uint64) uint64 {
	if cur < base {
		return 0
	}
	return cur - base
}

func nonNeg(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func groupDigits(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
