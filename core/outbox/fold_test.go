// file: core/outbox/fold_test.go
// version: 1.0.0
// guid: c4099b21-894a-4934-ba3b-b83bfa20d2b1
// last-edited: 2026-11-12

package outbox_test

import (
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// A start that folds into a run that already exists (the kind's default dedupe
// rule: a queued or running run with byte-identical params) queues nothing, and
// the delivery is still acked. The relay says so: one info line per folded
// delivery naming the run, and none for the delivery that started it, so a
// consumer whose runs keep folding is visible instead of a quiet loss of the
// work that arrived after the first.
func TestOutbox_FoldedRunStartIsAckedAndLogged(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	rs := &responder{}
	logs := &logCapture{}
	r := newRig(t, rigOpts{log: logs.logger(), sweep: sweepFast, register: respondInto, defs: []tasks.Definition{rs.blockingDef(release)}})

	r.stageGotham("batmobile", Dispatch{Villain: "Penguin", Block: 1})
	eventually(t, "the first run to start", func() bool { return len(r.runsOf(runKind)) == 1 })
	r.stageGotham("batmobile", Dispatch{Villain: "Penguin", Block: 1})
	eventually(t, "the second delivery to be acked", func() bool { return r.messages(signalName) == 0 })

	stays(t, "no second run", 300*time.Millisecond, func() bool { return len(r.runsOf(runKind)) == 1 })
	out := logs.text()
	if n := strings.Count(out, "outbox: run start folded into an existing run"); n != 1 {
		t.Fatalf("%d fold lines, want 1:\n%s", n, out)
	}
	runs := r.runsOf(runKind)
	for _, want := range []string{"consumer=" + consumerC, "topic=" + signalName, "seq=2", "run=" + string(runs[0].ID)} {
		if !strings.Contains(out, want) {
			t.Errorf("the fold line lacks %q:\n%s", want, out)
		}
	}
}
