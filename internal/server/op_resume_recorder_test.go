// file: internal/server/op_resume_recorder_test.go
// version: 1.0.0
// guid: 853b9efd-030c-4304-8ee8-b08403f21f79
// last-edited: 2026-09-11

package server

import (
	"encoding/json"
	"log/slog"
	"maps"
	"sync"
	"testing"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// resumeRecorder is a Reporter for the interrupt-then-resume tests. It records
// every checkpoint as the JSON the production reporter would persist, counts
// progress calls so a test can cancel the run after N of them, and answers
// ReporterOpID so ops that key rows on their op id get a real key. Only the
// methods the ops under test call are implemented; the embedded interface
// supplies the rest and panics loudly if a test starts depending on one.
type resumeRecorder struct {
	opsregistry.Reporter
	opID string

	mu         sync.Mutex
	states     [][]byte
	logs       []string
	progress   []progressCall
	onProgress func(nth int)
}

type progressCall struct {
	current, total int
}

func (r *resumeRecorder) OpID() string { return r.opID }

func (r *resumeRecorder) Checkpoint(state any) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.states = append(r.states, data)
	r.mu.Unlock()
	return nil
}

func (r *resumeRecorder) UpdateProgress(current, total int, _ string) error {
	r.mu.Lock()
	r.progress = append(r.progress, progressCall{current, total})
	nth := len(r.progress)
	cb := r.onProgress
	r.mu.Unlock()
	if cb != nil {
		cb(nth)
	}
	return nil
}

func (r *resumeRecorder) Log(_ slog.Level, message string, _ ...slog.Attr) error {
	r.mu.Lock()
	r.logs = append(r.logs, message)
	r.mu.Unlock()
	return nil
}

func (r *resumeRecorder) Logger() *slog.Logger  { return slog.Default() }
func (r *resumeRecorder) SetCurrentItem(string) {}
func (r *resumeRecorder) IsCanceled() bool      { return false }

// lastState decodes the most recent checkpoint into out. It fails the test if
// none was written: an op under ResumeRestart that never checkpoints is the
// exact defect these tests exist to catch.
func (r *resumeRecorder) lastState(t *testing.T, out any) []byte {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		t.Fatal("no checkpoint was written; a ResumeRestart op that never checkpoints restarts from zero")
	}
	last := r.states[len(r.states)-1]
	if err := json.Unmarshal(last, out); err != nil {
		t.Fatalf("decode last checkpoint: %v", err)
	}
	return last
}

func (r *resumeRecorder) lastProgress(t *testing.T) progressCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress) == 0 {
		t.Fatal("op reported no progress at all")
	}
	return r.progress[len(r.progress)-1]
}

func (r *resumeRecorder) firstProgress(t *testing.T) progressCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress) == 0 {
		t.Fatal("op reported no progress at all")
	}
	return r.progress[0]
}

func (r *resumeRecorder) logLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logs...)
}

// overlayCheckpoint reproduces what resumeRestart does to a re-queued row:
// the checkpoint's keys are overlaid on the original params (maps.Copy, so a
// key present in the checkpoint wins and one absent keeps the base value).
// The registry helper is unexported and lives in a package this test must
// not reach into, so the merge rule is restated here in the same terms.
func overlayCheckpoint(t *testing.T, base, checkpoint []byte) json.RawMessage {
	t.Helper()
	merged := map[string]any{}
	if err := json.Unmarshal(base, &merged); err != nil {
		t.Fatalf("decode base params: %v", err)
	}
	over := map[string]any{}
	if err := json.Unmarshal(checkpoint, &over); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	maps.Copy(merged, over)
	out, err := json.Marshal(merged)
	if err != nil {
		t.Fatalf("encode merged params: %v", err)
	}
	return out
}

// idSet is the set view the assertions compare on; order is irrelevant once
// workers run in parallel.
func idSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
