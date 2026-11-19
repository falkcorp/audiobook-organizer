// file: internal/books/cutover/m6/plan.go
// version: 1.0.0
// guid: 4a8e0c1b-7d63-4f2a-9b85-2c1e6f0d3a47
// last-edited: 2026-11-19

package m6

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// Plan is the preview of the step: what it would do over legacy, as a Report, with
// nothing written anywhere. It never goes through the runner, which records a plan,
// a level stamp, shard cursors and a state for any step it runs; a step the runner
// ran in a "dry" mode would leave the store with a finished walk and its source
// rows still present, and the real boot after it would never step a row.
//
// It walks the source ranges in key order and runs the step's own Step over each
// row it has not already seen consumed, exactly as the runner does for a
// RewritesInPlace step, but against importers that accept everything and a writer
// that keeps nothing. So the report is the real run's, entry for entry, with one
// limit: Plan cannot see what core's importers would answer, so an instance, job,
// root list or rule that core already holds a different row for, or refuses, is
// reported as imported here and quarantined by the real run. On a store whose
// native side is empty (every store M6 runs against at cutover) the two agree.
//
// cfg needs the reads (Legacy is ignored in favour of legacy; SecretNames,
// SecretValue and Effective are called); its importers and Report are ignored.
// Plan is for the dry run F7-09 wires to the storage CLI, beside the runner's
// scratch-copy rehearsal (05-storage 5S.3.6), which is the one that exercises the
// importers.
func Plan(ctx context.Context, legacy storage.Reader, cfg Config) (*Report, error) {
	if legacy == nil {
		return nil, errors.New("m6: Plan needs books' legacy range")
	}
	cfg.Legacy = func() storage.Reader { return legacy }
	if err := cfg.checkReads(); err != nil {
		return nil, err
	}
	rep := &Report{}
	cfg.Report = rep
	cfg.Integrations, cfg.AI, cfg.Notify, cfg.Config, cfg.Secrets = discardIntegrations{}, discardAI{}, discardNotify{}, discardConfig{}, discardSecrets{}
	m := newMigration(cfg, "plan")
	w := &planWrites{consumed: map[string]bool{}}
	for _, rg := range SourceRanges() {
		for kv, err := range legacy.Scan(ctx, storage.ScanOptions{Start: rg.Start, End: rg.End}) {
			if err != nil {
				return nil, fmt.Errorf("m6: plan: scan %q: %w", rg.Start, err)
			}
			if w.consumed[string(kv.Key)] {
				continue
			}
			if err := m.step(ctx, kv.Clone(), w); err != nil {
				return nil, fmt.Errorf("m6: plan: %w", err)
			}
		}
	}
	return rep, nil
}

// planWrites is the writer of a Plan: it keeps nothing but the keys the step has
// consumed, which the walk must not visit again (the runner skips them the same
// way: a pulled-forward key is in the shard's written set).
type planWrites struct{ consumed map[string]bool }

func (p *planWrites) Batch() *storage.Batch { return nil }
func (p *planWrites) Count(string)          {}
func (p *planWrites) Set([]byte, []byte) error {
	return nil
}
func (p *planWrites) Delete(key []byte) error {
	p.consumed[string(key)] = true
	return nil
}
func (p *planWrites) Quarantine(key, _ []byte, _ string) error {
	p.consumed[string(key)] = true
	return nil
}

// The importers of a Plan accept everything and stage nothing.
type (
	discardIntegrations struct{}
	discardAI           struct{}
	discardNotify       struct{}
	discardConfig       struct{}
	discardSecrets      struct{}
)

func (discardIntegrations) ImportInstance(context.Context, *storage.Batch, integrations.StoredInstance) error {
	return nil
}
func (discardAI) ImportJob(context.Context, *storage.Batch, ai.StoredJob) error { return nil }
func (discardNotify) ImportRule(context.Context, *storage.Batch, notify.Rule) error {
	return nil
}
func (discardConfig) Stage(*storage.Batch, config.Path, json.RawMessage, string) error { return nil }
func (discardConfig) Default(config.Path) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (discardSecrets) ImportSecret(context.Context, *storage.Batch, secrets.Write) error { return nil }
func (discardSecrets) ImportUnreadable(context.Context, *storage.Batch, secrets.Name, string, string) error {
	return nil
}
