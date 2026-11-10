// file: scripts/r2corpus/shaped/scan.go
// version: 1.0.0
// guid: 79b25d7e-9ce8-448c-baed-d6affa20dbf9
// last-edited: 2026-11-10

//go:build r2corpus_shaped

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// family is one row of p1_counts.json: a key prefix and the owner's measured
// keys, raw key bytes and raw value bytes under it (counts only, never values;
// 09 §9.7 P1).
type family struct {
	Prefix        string `json:"prefix"`
	Keys          int64  `json:"keys"`
	RawKeyBytes   int64  `json:"raw_key_bytes"`
	RawValueBytes int64  `json:"raw_value_bytes"`
}

type countsFile struct {
	Families []family `json:"families"`
}

func loadCounts(path string) ([]family, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c countsFile
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(c.Families) == 0 {
		return nil, fmt.Errorf("%s: no families", path)
	}
	seen := map[string]bool{}
	for _, f := range c.Families {
		switch {
		case f.Prefix == "":
			return nil, fmt.Errorf("%s: a family with no prefix", path)
		case seen[f.Prefix]:
			return nil, fmt.Errorf("%s: prefix %q listed twice", path, f.Prefix)
		case f.Keys < 0 || f.RawKeyBytes < 0 || f.RawValueBytes < 0:
			return nil, fmt.Errorf("%s: prefix %q has a negative count", path, f.Prefix)
		case f.Keys == 0 && (f.RawKeyBytes != 0 || f.RawValueBytes != 0):
			return nil, fmt.Errorf("%s: prefix %q has bytes but no keys", path, f.Prefix)
		}
		seen[f.Prefix] = true
	}
	return c.Families, nil
}

// classifier maps a key to the family the P1 census counted it under: the
// longest registered prefix (the registry's nesting rule: a parent's figures
// exclude every registered child). The counts file's own prefixes join the
// registry's, so a prefix the registry lacks still counts its own keys.
type classifier struct {
	prefixes []string // longest first
}

func newClassifier(fams []family) *classifier {
	set := map[string]bool{}
	for _, f := range store.KeyFamilies() {
		set[f.Prefix] = true
	}
	for _, f := range fams {
		set[f.Prefix] = true
	}
	c := &classifier{}
	for p := range set {
		c.prefixes = append(c.prefixes, p)
	}
	sort.Slice(c.prefixes, func(i, j int) bool {
		if len(c.prefixes[i]) != len(c.prefixes[j]) {
			return len(c.prefixes[i]) > len(c.prefixes[j])
		}
		return c.prefixes[i] < c.prefixes[j]
	})
	return c
}

// of returns the family prefix of key, or "" when no prefix covers it.
func (c *classifier) of(key []byte) string {
	k := string(key)
	for _, p := range c.prefixes {
		if strings.HasPrefix(k, p) {
			return p
		}
	}
	return ""
}

// maxTemplates caps the template rows kept per family: generated rows cycle
// through them, so a few dozen cover a family's shapes.
const maxTemplates = 64

type row struct {
	key, value []byte
}

// census is what one pass over a store finds for the counts families.
type census struct {
	keys, keyBytes, valueBytes map[string]int64
	// brokenJSON counts values that open like a JSON object or array but do
	// not parse: a generated row that no longer decodes as its template did.
	brokenJSON map[string]int64
	templates  map[string][]row
	// other counts the keys no counts family covers; they are never touched.
	otherKeys int64
}

func newCensus() *census {
	return &census{
		keys: map[string]int64{}, keyBytes: map[string]int64{}, valueBytes: map[string]int64{}, brokenJSON: map[string]int64{},
		templates: map[string][]row{},
	}
}

// scanPebble reads every key of the store at path read-only and counts it under
// its family; wantTemplates keeps the first maxTemplates rows of each family
// for the generator.
func scanPebble(path string, cl *classifier, counted map[string]bool, wantTemplates bool) (*census, error) {
	db, err := pebble.Open(path, &pebble.Options{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close()
	it, err := db.NewIter(nil)
	if err != nil {
		return nil, err
	}
	c := newCensus()
	for ok := it.First(); ok; ok = it.Next() {
		fam := cl.of(it.Key())
		if fam == "" || !counted[fam] {
			c.otherKeys++
			continue
		}
		c.keys[fam]++
		c.keyBytes[fam] += int64(len(it.Key()))
		c.valueBytes[fam] += int64(len(it.Value()))
		if v := it.Value(); len(v) > 0 && (v[0] == '{' || v[0] == '[') && !json.Valid(v) {
			c.brokenJSON[fam]++
		}
		if wantTemplates && len(c.templates[fam]) < maxTemplates {
			c.templates[fam] = append(c.templates[fam], row{
				key:   append([]byte(nil), it.Key()...),
				value: append([]byte(nil), it.Value()...),
			})
		}
	}
	if err := it.Error(); err != nil {
		_ = it.Close()
		return nil, err
	}
	return c, it.Close()
}

func countedSet(fams []family) map[string]bool {
	m := make(map[string]bool, len(fams))
	for _, f := range fams {
		m[f.Prefix] = true
	}
	return m
}

type scanFamily struct {
	Prefix        string `json:"prefix"`
	Keys          int64  `json:"keys"`
	RawKeyBytes   int64  `json:"raw_key_bytes"`
	RawValueBytes int64  `json:"raw_value_bytes"`
	// InvalidJSON counts values that start like JSON and do not parse.
	InvalidJSON int64 `json:"invalid_json"`
}

type scanReport struct {
	Families  []scanFamily `json:"families"`
	OtherKeys int64        `json:"other_keys"`
}

// scanStore is -mode scan: what a store holds, per counts family.
func scanStore(path string, fams []family) (*scanReport, error) {
	c, err := scanPebble(path, newClassifier(fams), countedSet(fams), false)
	if err != nil {
		return nil, err
	}
	rep := &scanReport{OtherKeys: c.otherKeys}
	for _, f := range fams {
		rep.Families = append(rep.Families, scanFamily{
			Prefix: f.Prefix, Keys: c.keys[f.Prefix],
			RawKeyBytes: c.keyBytes[f.Prefix], RawValueBytes: c.valueBytes[f.Prefix],
			InvalidJSON: c.brokenJSON[f.Prefix],
		})
	}
	return rep, nil
}
