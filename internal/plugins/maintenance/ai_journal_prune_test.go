// file: internal/plugins/maintenance/ai_journal_prune_test.go
// version: 1.0.0
// guid: 27ded651-bee8-45c3-a464-9159918a1f1f
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type journalPruneDeps struct {
	fakeDeps
	days int
}

func (d journalPruneDeps) AIJournalRetentionDays() int { return d.days }

// seedJournal writes one whisper entry dated well past any retention window,
// one fresh one, and one aijournal:batch: key the prune does not own.
func seedJournal(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	old, _ := json.Marshal(resultjournal.Entry{At: time.Now().AddDate(-1, 0, 0), Result: json.RawMessage(`{}`)})
	fresh, _ := json.Marshal(resultjournal.Entry{At: time.Now(), Result: json.RawMessage(`{}`)})
	for k, v := range map[string][]byte{
		"aijournal:whisper:old":   old,
		"aijournal:whisper:fresh": fresh,
		"aijournal:batch:b1":      old,
	} {
		if err := s.SetRaw(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestPruneAIJournal_UsesConfiguredRetention(t *testing.T) {
	s := seedJournal(t)
	p := New(journalPruneDeps{fakeDeps: fakeDeps{store: s}, days: 30})
	rep := &resultReporter{}
	if err := p.runPruneAIJournal(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	res, ok := rep.result.(PruneAIJournalResult)
	if !ok {
		t.Fatalf("result = %T, want PruneAIJournalResult", rep.result)
	}
	if res.RetentionDays != 30 || res.Deleted != 1 || res.Skipped {
		t.Fatalf("result = %+v, want 30 days, 1 deleted", res)
	}
	if v, _ := s.GetRaw("aijournal:whisper:old"); v != nil {
		t.Error("expired whisper entry survived")
	}
	for _, k := range []string{"aijournal:whisper:fresh", "aijournal:batch:b1"} {
		if v, _ := s.GetRaw(k); v == nil {
			t.Errorf("%s deleted", k)
		}
	}
}

func TestPruneAIJournal_ZeroRetentionKeepsForever(t *testing.T) {
	s := seedJournal(t)
	p := New(journalPruneDeps{fakeDeps: fakeDeps{store: s}, days: 0})
	rep := &resultReporter{}
	if err := p.runPruneAIJournal(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if res, _ := rep.result.(PruneAIJournalResult); !res.Skipped || res.Deleted != 0 {
		t.Fatalf("result = %+v, want skipped with nothing deleted", rep.result)
	}
	if n, _ := s.CountPrefix("aijournal:"); n != 3 {
		t.Fatalf("%d aijournal entries left, want all 3", n)
	}
}

func TestPruneAIJournal_ParamOverridesConfig(t *testing.T) {
	s := seedJournal(t)
	p := New(journalPruneDeps{fakeDeps: fakeDeps{store: s}, days: 0})
	rep := &resultReporter{}
	if err := p.runPruneAIJournal(context.Background(), json.RawMessage(`{"retention_days":7}`), rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if res, _ := rep.result.(PruneAIJournalResult); res.RetentionDays != 7 || res.Deleted != 1 {
		t.Fatalf("result = %+v, want 7 days, 1 deleted", rep.result)
	}
	if err := p.runPruneAIJournal(context.Background(), json.RawMessage(`{"retention_days":-1}`), rep); err == nil {
		t.Fatal("negative retention_days accepted")
	}
}

func TestPruneAIJournal_CancelledContext(t *testing.T) {
	s := seedJournal(t)
	p := New(journalPruneDeps{fakeDeps: fakeDeps{store: s}, days: 30})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.runPruneAIJournal(ctx, nil, &resultReporter{}); err == nil {
		t.Fatal("cancelled run returned nil")
	}
	if v, _ := s.GetRaw("aijournal:whisper:old"); v == nil {
		t.Fatal("cancelled-before-start run deleted an entry")
	}
}

func TestPruneAIJournalDef_Shape(t *testing.T) {
	def := New(fakeDeps{}).pruneAIJournalDef()
	if def.ID != "maintenance.prune-ai-journal" {
		t.Errorf("ID = %q", def.ID)
	}
	if def.ConcurrencyKey != PruneAIJournalDefID {
		t.Errorf("ConcurrencyKey = %q", def.ConcurrencyKey)
	}
	if def.ResumePolicy != sdk.ResumeDrop || !def.Cancellable {
		t.Errorf("ResumePolicy=%v Cancellable=%v; want drop + cancellable", def.ResumePolicy, def.Cancellable)
	}
	reg := &capRegistry{}
	if err := New(nil).Register(reg); err != nil {
		t.Fatal(err)
	}
	if !reg.ids[PruneAIJournalDefID] {
		t.Fatal("prune-ai-journal is not registered by the maintenance plugin")
	}
}

type capRegistry struct{ ids map[string]bool }

func (c *capRegistry) RegisterOp(def sdk.OperationDef) error {
	if c.ids == nil {
		c.ids = map[string]bool{}
	}
	c.ids[def.ID] = true
	return nil
}

func (c *capRegistry) EnqueueOp(context.Context, string, any, ...sdk.EnqueueOption) (string, error) {
	return "", nil
}
