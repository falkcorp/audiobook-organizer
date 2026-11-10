// file: core/internal/storagemigrate/chunk.go
// version: 1.0.0
// guid: a463dd58-35aa-4d88-86e3-7d8bb405b11c
// last-edited: 2026-11-10

package storagemigrate

import (
	"bytes"
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// chunk is one chunk's datamigrate.Sink: the StepWriter's calls land in its
// batch, which commits with the shard's cursor. Every rule a call breaks
// poisons the chunk (err), so nothing of it commits even when Step ignores
// the returned error.
type chunk struct {
	run     *shardRun
	shard   *shardState
	b       *storage.Batch
	ctx     context.Context
	current []byte           // the item being stepped
	counts  map[string]int64 // folded into the stats after the commit
	err     error            // the first rule broken
}

var _ datamigrate.Sink = (*chunk)(nil)

// poison records err as the rule the chunk broke, unless it broke one
// already: the first stays.
func (c *chunk) poison(err error) {
	if c.err == nil {
		c.err = err
	}
}

// fail poisons the chunk with err and returns the chunk's first error.
func (c *chunk) fail(err error) error {
	c.poison(err)
	return c.err
}

// admit is runner rule 5 for a write to key: a key outside the source, or
// in a part of a shard its worker has already stepped, is never revisited
// this run, so the write is admitted. A write into a shard's unvisited part
// is refused unless the migration declares RewritesInPlace; then it is
// recorded in that shard's written set, and the shard skips the key when
// it reaches it.
func (c *chunk) admit(key []byte) error {
	if c.err != nil {
		return c.err
	}
	n := shardOf(c.run.mg, c.run.bounds, key)
	if n < 0 {
		return nil
	}
	s := c.run.shards[n]
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.visited(key) {
		return nil
	}
	if !c.run.mg.m.RewritesInPlace {
		return c.fail(fmt.Errorf("%w: %q (shard %d) by %s, which does not declare RewritesInPlace", datamigrate.ErrUnvisitedWrite, key, n, c.run.mg.m.ID))
	}
	s.written[string(key)] = struct{}{}
	return nil
}

func (c *chunk) Set(key, value []byte) error {
	if err := c.admit(key); err != nil {
		return err
	}
	return c.b.Set(key, value)
}

func (c *chunk) Delete(key []byte) error {
	if err := c.admit(key); err != nil {
		return err
	}
	return c.b.Delete(key)
}

func (c *chunk) SetIfAbsent(key, value []byte) error {
	if err := c.admit(key); err != nil {
		return err
	}
	if err := c.b.Expect(c.run.src, key, nil); err != nil {
		return err
	}
	return c.b.Set(key, value)
}

func (c *chunk) SetIfUnchanged(key, expect, value []byte) error {
	if err := c.admit(key); err != nil {
		return err
	}
	if err := c.b.Expect(c.run.src, key, expect); err != nil {
		return err
	}
	return c.b.Set(key, value)
}

func (c *chunk) DeleteIfUnchanged(key, expect []byte) error {
	if err := c.admit(key); err != nil {
		return err
	}
	if err := c.b.Expect(c.run.src, key, expect); err != nil {
		return err
	}
	return c.b.Delete(key)
}

// Hold records key with reason in the chunk's batch (runner rule 8), so
// the held list commits with the chunk; refused in a Cutover step, whose
// retired range must drain.
func (c *chunk) Hold(key []byte, reason string) {
	if c.err != nil {
		return
	}
	if c.run.mg.m.Cutover {
		c.poison(fmt.Errorf("%w: %q", datamigrate.ErrHoldInCutover, key))
		return
	}
	rec := heldRecord{Reason: reason, Shard: c.shard.n, At: c.run.r.opts.Clock().UTC()}
	if err := stage(c.b, c.run.sks, heldKey(c.run.mg.m.ID, key), heldCodec(), rec); err != nil {
		c.poison(err)
		return
	}
	c.counts[datamigrate.OutcomeHeld]++
}

func (c *chunk) Count(outcome string) {
	if outcome != "" {
		c.counts[outcome]++
	}
}

// Quarantine stages the quarantine record (the kernel's writer, F3-04's
// format) and the source key's delete into the chunk's batch, so both
// commit with the chunk and its cursor or neither does. Cutover steps only;
// without a writer the chunk fails (datamigrate.ErrQuarantineNotWired) and
// the source row stays.
func (c *chunk) Quarantine(key, raw []byte, reason string) error {
	m := c.run.mg.m
	switch {
	case c.err != nil:
		return c.err
	case !m.Cutover:
		return c.fail(fmt.Errorf("%w: Quarantine in %s", datamigrate.ErrCutoverOnly, m.ID))
	case c.run.r.opts.Quarantine == nil:
		return c.fail(fmt.Errorf("%w: %s would quarantine %q", datamigrate.ErrQuarantineNotWired, m.ID, key))
	}
	if err := c.admit(key); err != nil {
		return err
	}
	abs := c.run.mg.absolute(key)
	sensitive := false
	for _, p := range m.Sensitive {
		sensitive = sensitive || bytes.HasPrefix(abs, []byte(p))
	}
	if err := c.run.r.opts.Quarantine(c.ctx, c.b, m.ID, abs, raw, reason, sensitive); err != nil {
		return c.fail(fmt.Errorf("storagemigrate: %s: quarantine of %q: %w", m.ID, key, err))
	}
	if err := c.b.Delete(key); err != nil {
		return c.fail(err)
	}
	c.counts[datamigrate.OutcomeQuarantined]++
	return nil
}

// Batch is the chunk's batch for an importer (Cutover only; any other
// step's chunk fails at commit). Writes an importer stages through its own
// handle are outside rule 5's check: a Cutover step's Source is retired
// claims, which no importer writes.
func (c *chunk) Batch() *storage.Batch {
	if !c.run.mg.m.Cutover {
		c.poison(fmt.Errorf("%w: Batch in %s", datamigrate.ErrCutoverOnly, c.run.mg.m.ID))
	}
	return c.b
}

// absolute is a source-handle key as an absolute store key.
func (mg *migration) absolute(key []byte) []byte {
	if mg.legacy {
		return bytes.Clone(key)
	}
	return append(bytes.Clone(mg.prefix), key...)
}
