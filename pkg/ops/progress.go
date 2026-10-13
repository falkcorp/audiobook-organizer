// file: pkg/ops/progress.go
// version: 1.0.0
// guid: 9d5f77ea-78d1-4e75-bdef-9e92f10feb81
// last-edited: 2026-10-13

package ops

import "time"

// Progress is a run's structured progress handle (rc.Progress()). A Batch or
// Fixer gets its numbers from the runner; a Task sets them itself.
type Progress struct {
	host Host
}

// Set reports a Task's position. Tasks only; a Batch's numbers come from the
// chunk ledger and a Set from inside an item is ignored by the runner.
func (p *Progress) Set(done int64, total Total) {
	if p == nil || p.host == nil {
		return
	}
	p.host.Progress(done, total, "")
}

// Tick stamps liveness without a number.
func (p *Progress) Tick() {
	if p == nil || p.host == nil {
		return
	}
	p.host.Touch()
}

// Note sets the free-text status line.
func (p *Progress) Note(msg string) {
	if p == nil || p.host == nil {
		return
	}
	p.host.Progress(-1, Total{}, msg)
}

// Total is an item count that may be unknown. Progress shows a rate and no
// percentage for an unknown total instead of a fake 0/0.
type Total struct {
	n     int64
	known bool
}

// Known is a counted total.
func Known(n int64) Total { return Total{n: n, known: true} }

// Unknown is a total the source cannot count cheaply.
func Unknown() Total { return Total{} }

// Value returns the total and whether it is known.
func (t Total) Value() (int64, bool) { return t.n, t.known }

// ProgressSnapshot is what the API and SSE carry. Its JSON shape is the one
// stored on the run record (database.ProgressSnapshot); a test pins that the
// two encode identically.
type ProgressSnapshot struct {
	Phase     string           `json:"phase"`
	Phases    []string         `json:"phases"`
	Done      int64            `json:"done"`
	Total     *int64           `json:"total"`
	Unit      string           `json:"unit"`
	Failed    int64            `json:"failed"`
	Counters  map[string]int64 `json:"counters"`
	Chunks    *ChunkProgress   `json:"chunks"`
	Workers   []WorkerProgress `json:"workers"`
	RatePerS  float64          `json:"rate_per_s"`
	ETA       *time.Duration   `json:"eta"`
	Current   string           `json:"current"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// ChunkProgress is the chunk part of a ProgressSnapshot (Batch/Fixer only).
type ChunkProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"` // for AppendOnly: chunks cut so far
	Size  int `json:"size"`  // ChunkSize in effect
}

// WorkerProgress is one worker's row: what it holds and when it last moved.
type WorkerProgress struct {
	Worker    int       `json:"worker"`
	Chunk     int       `json:"chunk"` // -1 when idle
	Item      string    `json:"item"`
	ItemIndex int       `json:"item_index"`
	ChunkSize int       `json:"chunk_size"`
	LeasedAt  time.Time `json:"leased_at"`
	Heartbeat time.Time `json:"heartbeat"`
	Stuck     bool      `json:"stuck"`
}

// Summary is what Finish receives: the counters folded by the chunk ledger.
// Only completed chunks are in it, so it is the same number a resume starts
// from.
type Summary struct {
	Done, Total, Failed, Skipped int64
	Counters                     map[string]int64
	ChunksDone, ChunksTotal      int
	Canceled                     bool
}

// Counter returns an op-defined counter (rc.Count) or skipped:<reason>.
func (s Summary) Counter(name string) int64 { return s.Counters[name] }
