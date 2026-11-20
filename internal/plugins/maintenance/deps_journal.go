// file: internal/plugins/maintenance/deps_journal.go
// version: 0.1.0
// guid: 7d2b9c46-1e5a-4f83-a6d0-3b8e5c1f9a27
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// gatedResultJournal is the whisper result journal with its writes stamped.
// Opening the journal is one gated call, but the journal outlives it: the
// transport appends (Complete) and removes (Invalidate) entries for the rest of
// the run, so a journal handed back bare would write after the run was stopped,
// fenced or previewed. Lookup reads and passes through.
//
// The shared table has no row for the journal's own methods, and adding one
// edits the table every group shares, so each write is stamped under the row of
// the call that opened the journal (method: g4 OpenWhisperJournal, g6
// HealWhisperJournal), which is ops.ResAIJournal in both. The recorded write
// name is therefore the opener's.
type gatedResultJournal struct {
	j      transcribe.ResultJournal
	gate   booksGate
	method string
}

func (g gatedResultJournal) Lookup(contentKey string) (json.RawMessage, bool, error) {
	return g.j.Lookup(contentKey)
}

func (g gatedResultJournal) Complete(contentKey, endpoint, model string, result any) error {
	return g.gate.do(runless(), g.method, kindRef(ops.ResAIJournal), func(context.Context) error {
		return g.j.Complete(contentKey, endpoint, model, result)
	})
}

func (g gatedResultJournal) Invalidate(contentKeys []string) error {
	return g.gate.do(runless(), g.method, kindRef(ops.ResAIJournal), func(context.Context) error {
		return g.j.Invalidate(contentKeys)
	})
}

var _ transcribe.ResultJournal = gatedResultJournal{}
