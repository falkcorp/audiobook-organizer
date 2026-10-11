// file: internal/config/generation_test.go
// version: 1.0.0
// guid: d858cf4c-5ceb-4497-892a-3cefc92f03c9
// last-edited: 2026-10-11

package config

import "testing"

// Generation is how a consumer that derives state from the config (the AI
// metrics model-label set) learns that Mutate ran. The contract is: it moves
// on every Mutate, and a reader that sees the new value then sees the write.
func TestGenerationMovesOnMutate(t *testing.T) {
	prev := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = prev }) })

	before := Generation()
	if Generation() != before {
		t.Fatal("Generation moved without a Mutate")
	}
	Mutate(func(c *Config) { c.FilenameParseModel = "generation-test-model" })
	after := Generation()
	if after == before {
		t.Fatal("Generation did not move on Mutate")
	}
	if got := Snapshot().FilenameParseModel; got != "generation-test-model" {
		t.Errorf("Snapshot after the generation moved = %q, want the write", got)
	}

	func() {
		defer func() { _ = recover() }()
		Mutate(func(c *Config) { panic("fn failed after changing nothing") })
	}()
	if Generation() == after {
		t.Error("Generation did not move when fn panicked; fn may have written before it did")
	}
}
