// file: internal/server/handlers/opsv3/view.go
// version: 1.2.0
// guid: d646f79e-8b01-401b-a907-b56f76273e12
// last-edited: 2026-10-26

package opsv3

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// ChunksView is a batch run's chunk progress.
type ChunksView struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// ProgressView is a run's typed progress. Chunks and Workers are filled for a
// Batch or a Fixer and are null / empty for a Task. Total is null while it is
// unknown (never 0 standing in for unknown).
type ProgressView struct {
	Done    int64                     `json:"done"`
	Total   *int64                    `json:"total"`
	Failed  int64                     `json:"failed"`
	Phase   string                    `json:"phase,omitempty"`
	Chunks  *ChunksView               `json:"chunks"`
	Workers []database.WorkerProgress `json:"workers"`
}

// RunView is one run as the API shows it.
type RunView struct {
	ID         string       `json:"id"`
	DefID      string       `json:"def_id"`
	State      string       `json:"state"`
	Outcome    string       `json:"outcome"`
	Mode       string       `json:"mode"`
	StartedAt  *time.Time   `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at"`
	Progress   ProgressView `json:"progress"`
	// DisplayState is the state to show. It is State except for a run the
	// registry reports as a zombie while its record still reads running (the
	// record can lag the registry by one write): that run displays
	// "stopping". State stays the stored state, so a row always matches the
	// ?state= filter and the /counts group that listed it.
	DisplayState string `json:"display_state"`
	// Zombie is true for a run whose goroutine outlived the abandon grace and
	// has not returned. It displays "stopping" (the slot and the exclusive
	// key are still held), never a terminal state.
	Zombie    bool   `json:"zombie"`
	Error     string `json:"error"`
	StartedBy string `json:"started_by"`
}

// withUserText is v with its free text read through userText: the error, and the
// label of the item each worker is on (a worker's Item is Label(current item),
// which can name a user). The workers are copied, because the slice is the
// record's own and is shared with every other reader of it. Applying it twice
// with the same function changes nothing further.
func (v RunView) withUserText(userText func(string) string) RunView {
	v.Error = userText(v.Error)
	if len(v.Progress.Workers) == 0 {
		return v
	}
	ws := make([]database.WorkerProgress, len(v.Progress.Workers))
	for i, w := range v.Progress.Workers {
		w.Item = userText(w.Item)
		ws[i] = w
	}
	v.Progress.Workers = ws
	return v
}

// outcomeOf is the outcome a settled state stands for; "" for a run that has
// not ended or is waiting on a person. It is one-to-one with the state, which
// is why a count by outcome links to the listing by state.
func outcomeOf(s state.V3) string {
	switch s {
	case state.V3Succeeded, state.V3Failed, state.V3Canceled, state.V3TimedOut, state.V3Dropped, state.V3Interrupted:
		return string(s)
	}
	return ""
}

// toView renders a record. zombies is the set of run ids the registry reports
// as stopping-with-a-live-goroutine; it sets Zombie and DisplayState because
// the record can lag the registry by one write, and leaves State alone.
// userText is applied to the run's error text and its workers' current-item
// labels, which can name a user (opaccess.Reader.UserText); a view that is published to many callers passes
// the identity and redacts per caller when it is sent.
func toView(rec database.RunRecord, zombies map[string]bool, userText func(string) string) RunView {
	v := RunView{
		ID: rec.ID, DefID: rec.DefID, State: string(rec.State), DisplayState: string(rec.State), Outcome: outcomeOf(rec.State),
		Mode: rec.Mode, StartedAt: rec.StartedAt, FinishedAt: rec.CompletedAt,
		Zombie: rec.Zombie, Error: rec.Error, StartedBy: rec.Actor.UserID,
		Progress: ProgressView{
			Done: rec.Progress.Done, Total: rec.Progress.Total, Failed: rec.Progress.Failed,
			Phase: rec.Progress.Phase, Workers: []database.WorkerProgress{},
		},
	}
	if c := rec.Progress.Chunks; c != nil {
		v.Progress.Chunks = &ChunksView{Done: c.Done, Total: c.Total}
	}
	if len(rec.Progress.Workers) > 0 {
		v.Progress.Workers = rec.Progress.Workers
	}
	v = v.withUserText(userText)
	if zombies[rec.ID] {
		v.Zombie = true
		v.DisplayState = string(state.V3Stopping)
		v.Outcome = ""
	}
	return v
}

// zombieSet is the ids of the runs the registry reports as zombies.
func (h *Handler) zombieSet() map[string]bool {
	if h.d.Zombies == nil {
		return nil
	}
	zs := h.d.Zombies.Zombies()
	if len(zs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(zs))
	for _, z := range zs {
		out[z.OpID] = true
	}
	return out
}

func (h *Handler) views(c *gin.Context, recs []database.RunRecord) []RunView {
	userText := h.d.RepairsAccess.For(c).UserText
	zs := h.zombieSet()
	out := make([]RunView, 0, len(recs))
	for _, r := range recs {
		out = append(out, toView(r, zs, userText))
	}
	return out
}
