// file: internal/authority/snapshot.go
// version: 1.0.0
// guid: 678e6da9-151e-45a7-a8d7-d77a71ac98ce
// last-edited: 2026-10-04

package authority

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Lookup is the read API every consumer depends on. It is deliberately
// narrow and error-free so a caller can ask it once per credit:
//
//   - at import: authorcredit.Resolve (file importer, scanner, iTunes,
//     metafetch apply), the narrator splitter, and CleanGate's publisher
//     refusal;
//   - in backfill and repair: the combined-author fixer, the junk-author
//     fixers, narrator cleanup and split, the credits backfill, and
//     publisher normalisation (merging publisher spellings, pulling
//     publishers out of author fields).
//
// Every answer is evidence only. A nil Person / Publisher or a false
// IsKnownPerson means "the index knows nothing", never "not a person" or
// "not a publisher", and no method creates anything.
//
// *Snapshot is the implementation for bulk callers (one load, then map
// reads; no store access per call). Empty() is the implementation for a
// caller whose feature flag is off.
type Lookup interface {
	// Person returns the entry a name folds to, overrides applied: roles
	// (author / narrator / cast_author) with counts and tiers, sources,
	// contributor ASINs. nil when unknown.
	Person(name string) *Entry
	// IsKnownPerson reports whether the entry is evidence for role (see
	// Entry.Qualifies: cast_author never counts as author).
	IsKnownPerson(name string, role Role) bool
	// Publisher returns the publisher a name folds to, overrides applied,
	// with Canonical set to the spelling a consumer should write. nil when
	// unknown.
	Publisher(name string) *PublisherEntry
	// CanonicalPublisher returns the canonical display for a publisher
	// spelling, and false when the name is not a known publisher (unknown or
	// blocked by an override).
	CanonicalPublisher(name string) (string, bool)
}

// Snapshot is an in-memory copy of the persons, publishers and overrides,
// loaded with one paged scan per prefix. It is immutable after load and safe
// for concurrent use. Entries are merged once at load, so a lookup is two
// map reads.
type Snapshot struct {
	persons    map[string]*Entry
	publishers map[string]*PublisherEntry
}

var (
	_ Lookup = (*Snapshot)(nil)
	_ Lookup = emptyLookup{}
)

// LoadSnapshot reads ref_person:, ref_pub: and ref_ovr: into memory. Memory
// is the size of the index (tens of thousands of small rows), not of the
// library; reads are paged. A row that does not decode fails the load: a
// fixer must not run on an index with silent holes.
func LoadSnapshot(ctx context.Context, kv Scanner) (*Snapshot, error) {
	persons := map[string]*Person{}
	pubs := map[string]*Publisher{}
	povr := map[string]*PersonOverride{}
	bovr := map[string]*PublisherOverride{}
	if err := scanJSON(ctx, kv, PersonPrefix, persons); err != nil {
		return nil, err
	}
	if err := scanJSON(ctx, kv, PublisherPrefix, pubs); err != nil {
		return nil, err
	}
	if err := scanJSON(ctx, kv, OverridePrefix+overridePersonKind+":", povr); err != nil {
		return nil, err
	}
	if err := scanJSON(ctx, kv, OverridePrefix+overridePubKind+":", bovr); err != nil {
		return nil, err
	}
	s := &Snapshot{persons: make(map[string]*Entry, len(persons)+len(povr)), publishers: make(map[string]*PublisherEntry, len(pubs)+len(bovr))}
	for f := range union(persons, povr) {
		s.persons[f] = mergePerson(f, persons[f], povr[f])
	}
	for f := range union(pubs, bovr) {
		s.publishers[f] = mergePublisher(f, pubs[f], bovr[f])
	}
	return s, nil
}

// scanJSON decodes every value under prefix into dst, keyed by the key's
// remainder (the fold).
func scanJSON[T any](ctx context.Context, kv Scanner, prefix string, dst map[string]*T) error {
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pairs, next, err := kv.ScanPrefixPage(prefix, after, scanPageSize)
		if err != nil {
			return fmt.Errorf("authority snapshot: scan %s: %w", prefix, err)
		}
		for _, p := range pairs {
			var v T
			if err := json.Unmarshal(p.Value, &v); err != nil {
				return fmt.Errorf("authority snapshot: decode %s: %w", p.Key, err)
			}
			dst[strings.TrimPrefix(p.Key, prefix)] = &v
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

func union[A, B any](a map[string]A, b map[string]B) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// Persons and Publishers count the snapshot's rows.
func (s *Snapshot) Persons() int    { return len(s.persons) }
func (s *Snapshot) Publishers() int { return len(s.publishers) }

// Person implements Lookup. The returned entry is shared: do not modify it.
func (s *Snapshot) Person(name string) *Entry {
	if s == nil {
		return nil
	}
	return s.persons[Fold(name)]
}

// IsKnownPerson implements Lookup.
func (s *Snapshot) IsKnownPerson(name string, role Role) bool {
	return s.Person(name).Qualifies(role)
}

// Publisher implements Lookup. The returned entry is shared: do not modify
// it.
func (s *Snapshot) Publisher(name string) *PublisherEntry {
	if s == nil {
		return nil
	}
	return s.publishers[Fold(name)]
}

// CanonicalPublisher implements Lookup.
func (s *Snapshot) CanonicalPublisher(name string) (string, bool) {
	return canonicalOf(s.Publisher(name))
}

func canonicalOf(e *PublisherEntry) (string, bool) {
	if e == nil || e.Blocked || e.Canonical == "" {
		return "", false
	}
	return e.Canonical, true
}

// Empty returns a Lookup that knows nothing: every answer is "no evidence".
// A consumer whose feature flag is off uses it, so its code path is the same
// with the flag on or off.
func Empty() Lookup { return emptyLookup{} }

type emptyLookup struct{}

func (emptyLookup) Person(string) *Entry                     { return nil }
func (emptyLookup) IsKnownPerson(string, Role) bool          { return false }
func (emptyLookup) Publisher(string) *PublisherEntry         { return nil }
func (emptyLookup) CanonicalPublisher(string) (string, bool) { return "", false }
