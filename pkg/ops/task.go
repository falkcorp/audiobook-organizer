// file: pkg/ops/task.go
// version: 1.0.0
// guid: 14401434-b3b4-4aa6-a6f4-eaaec371a84e
// last-edited: 2026-10-13

package ops

import (
	"encoding/json"
)

// TaskSpec is a single function. P is the params struct (NoParams when there
// are none); R is the result the UI shows (NoResult when there is none).
//
// A Task keeps its own liveness: it calls rc.Progress().Set or Tick at least
// every Timeout/6, or declares Cancel: Uninterruptible(reason, budget).
type TaskSpec[P any, R any] struct {
	Common
	Run func(rc *RC, p P) (R, error)
}

// NoParams is the empty params type.
type NoParams struct{}

// NoResult is the empty result type.
type NoResult struct{}

// Task builds a Task definition.
func Task[P any, R any](id string, s TaskSpec[P, R]) Definition {
	b := newSpec(id, KindTask, s.Common)
	if s.Run == nil {
		b.problem("Task has no Run")
	}
	b.task = func(rc *RC, params json.RawMessage) (json.RawMessage, error) {
		p, err := decodeParams[P](id, params)
		if err != nil {
			return nil, err
		}
		r, err := s.Run(rc, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(r)
	}
	return b
}
