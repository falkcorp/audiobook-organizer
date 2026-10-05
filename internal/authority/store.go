// file: internal/authority/store.go
// version: 1.0.0
// guid: 8214ae7d-59fb-46c8-9d73-9301ffdd21eb
// last-edited: 2026-10-04

package authority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Reader is the read surface the lookups need. database.RawKVStore
// satisfies it, and so does the production indexedStore.
type Reader interface {
	GetRaw(key string) ([]byte, error)
}

// Scanner adds the paged prefix scan a plan needs to find stale rows.
type Scanner interface {
	Reader
	ScanPrefixPage(prefix, after string, limit int) (pairs []database.KVPair, next string, err error)
}

// Writer is the surface an apply needs. It is a subset of
// database.RawKVStore: no new store method, so the production decorator
// forwards it with no capability assertion.
type Writer interface {
	Scanner
	SetRaw(key string, value []byte) error
	DeleteRawBatch(keys []string) error
}

const (
	scanPageSize    = 1000
	deleteBatchSize = 1000
)

// KeyValue is one planned write.
type KeyValue struct {
	Key   string
	Value []byte
}

// PrefixCounts is what a plan does under one prefix.
type PrefixCounts struct {
	Write     int `json:"write"`
	Unchanged int `json:"unchanged"`
	Stale     int `json:"stale"`
}

// Plan is the set of writes and deletes that turns the stored rebuildable
// keyspace into a Result. A dry run computes it and stops.
type Plan struct {
	Writes []KeyValue
	// Stale are rebuildable keys no source produced any more. Never an
	// override key.
	Stale  []string
	Counts map[string]*PrefixCounts
}

// target is one key the Result wants, with how to render its value against
// what is stored.
type target struct {
	key, prefix string
	render      func(existing []byte) (value []byte, changed bool, err error)
}

// PlanApply compares a Result with the store. Reads run on `workers`
// goroutines (bounded; at least 1). Timestamps are not content: a row whose
// only difference is FirstSeen/LastSeen is unchanged, so a second apply of
// the same sources writes nothing.
func PlanApply(ctx context.Context, kv Scanner, res *Result, now time.Time, workers int) (*Plan, error) {
	targets := buildTargets(res, now)
	plan := &Plan{Counts: map[string]*PrefixCounts{}}
	for _, p := range RebuildablePrefixes() {
		plan.Counts[p] = &PrefixCounts{}
	}

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, workers))
	for _, t := range targets {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			existing, err := kv.GetRaw(t.key)
			if err != nil {
				return fmt.Errorf("read %s: %w", t.key, err)
			}
			val, changed, err := t.render(existing)
			if err != nil {
				return fmt.Errorf("render %s: %w", t.key, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if changed {
				plan.Writes = append(plan.Writes, KeyValue{Key: t.key, Value: val})
				plan.Counts[t.prefix].Write++
			} else {
				plan.Counts[t.prefix].Unchanged++
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	slices.SortFunc(plan.Writes, func(a, b KeyValue) int { return strings.Compare(a.Key, b.Key) })

	want := make(map[string]bool, len(targets))
	for _, t := range targets {
		want[t.key] = true
	}
	for _, prefix := range RebuildablePrefixes() {
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			pairs, next, err := kv.ScanPrefixPage(prefix, after, scanPageSize)
			if err != nil {
				return nil, fmt.Errorf("scan %s: %w", prefix, err)
			}
			for _, p := range pairs {
				if !want[p.Key] {
					plan.Stale = append(plan.Stale, p.Key)
					plan.Counts[prefix].Stale++
				}
			}
			if next == "" {
				break
			}
			after = next
		}
	}
	return plan, nil
}

func buildTargets(res *Result, now time.Time) []target {
	out := make([]target, 0, len(res.Persons)+len(res.Publishers)+len(res.ASINs)+len(res.Ledger))
	for f, p := range res.Persons {
		out = append(out, target{key: PersonPrefix + f, prefix: PersonPrefix, render: func(existing []byte) ([]byte, bool, error) {
			return renderTimestamped(p, existing, now,
				func(v *Person) (time.Time, time.Time) { return v.FirstSeen, v.LastSeen },
				func(v *Person, first, last time.Time) { v.FirstSeen, v.LastSeen = first, last })
		}})
	}
	for f, p := range res.Publishers {
		out = append(out, target{key: PublisherPrefix + f, prefix: PublisherPrefix, render: func(existing []byte) ([]byte, bool, error) {
			return renderTimestamped(p, existing, now,
				func(v *Publisher) (time.Time, time.Time) { return v.FirstSeen, v.LastSeen },
				func(v *Publisher, first, last time.Time) { v.FirstSeen, v.LastSeen = first, last })
		}})
	}
	for a, ref := range res.ASINs {
		out = append(out, target{key: personASINKeyPrefix + a, prefix: ASINPrefix, render: func(existing []byte) ([]byte, bool, error) {
			val, err := json.Marshal(ref)
			if err != nil {
				return nil, false, err
			}
			return val, !bytes.Equal(val, existing), nil
		}})
	}
	for k, digest := range res.Ledger {
		out = append(out, target{key: k, prefix: SourcePrefix, render: func(existing []byte) ([]byte, bool, error) {
			val := []byte(digest)
			return val, !bytes.Equal(val, existing), nil
		}})
	}
	return out
}

// renderTimestamped renders v for a key holding existing. Content is
// compared with both timestamps zeroed; when it is unchanged nothing is
// written. A changed row keeps the stored FirstSeen and gets LastSeen = now.
// A stored value that does not decode is rewritten.
func renderTimestamped[T any](v *T, existing []byte, now time.Time,
	get func(*T) (time.Time, time.Time), set func(*T, time.Time, time.Time)) ([]byte, bool, error) {
	cur := *v
	set(&cur, time.Time{}, time.Time{})
	canon, err := json.Marshal(&cur)
	if err != nil {
		return nil, false, err
	}
	first := now
	if existing != nil {
		var old T
		if json.Unmarshal(existing, &old) == nil {
			oldFirst, _ := get(&old)
			set(&old, time.Time{}, time.Time{})
			if prev, err := json.Marshal(&old); err == nil && bytes.Equal(prev, canon) {
				return existing, false, nil
			}
			if !oldFirst.IsZero() {
				first = oldFirst
			}
		}
	}
	set(&cur, first, now)
	val, err := json.Marshal(&cur)
	return val, true, err
}

// ApplyResult counts what an apply did.
type ApplyResult struct {
	Written int `json:"written"`
	Deleted int `json:"deleted"`
}

// Apply writes a plan: every write first (on `workers` goroutines, so
// Pebble can group the synced commits), then the stale deletes, so a crash
// part-way leaves a superset of the new index, never a hole. Only
// rebuildable keys are ever deleted; an override key in Stale is refused.
func Apply(ctx context.Context, kv Writer, plan *Plan, workers int) (ApplyResult, error) {
	var res ApplyResult
	for _, k := range plan.Stale {
		if !isRebuildableKey(k) {
			return res, fmt.Errorf("authority apply: refusing to delete non-rebuildable key %q", k)
		}
	}
	for _, w := range plan.Writes {
		if !isRebuildableKey(w.Key) {
			return res, fmt.Errorf("authority apply: refusing to write non-rebuildable key %q", w.Key)
		}
	}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, workers))
	for _, w := range plan.Writes {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			if err := kv.SetRaw(w.Key, w.Value); err != nil {
				return fmt.Errorf("write %s: %w", w.Key, err)
			}
			mu.Lock()
			res.Written++
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return res, err
	}
	for i := 0; i < len(plan.Stale); i += deleteBatchSize {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		chunk := plan.Stale[i:min(i+deleteBatchSize, len(plan.Stale))]
		if err := kv.DeleteRawBatch(chunk); err != nil {
			return res, fmt.Errorf("delete stale rows: %w", err)
		}
		res.Deleted += len(chunk)
	}
	return res, nil
}

func isRebuildableKey(k string) bool {
	for _, p := range RebuildablePrefixes() {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// PersonOverride is an owner decision about a person (ref_ovr:person:).
// Roles[r] true makes r known at tier O; false blocks r whatever the sources
// say. A rebuild never touches it.
type PersonOverride struct {
	Name  string        `json:"name"`
	Roles map[Role]bool `json:"roles"`
	ASINs []string      `json:"asins,omitempty"`
	Note  string        `json:"note,omitempty"`
	SetAt time.Time     `json:"set_at"`
}

// PublisherOverride is an owner decision about a publisher (ref_ovr:pub:).
//
// Known false says the name is NOT a publisher. Canonical, when set, is the
// display name this spelling normalises to ("Podium Audio" for "Podium
// Publishing"): publisher normalisation merges spellings whose folds differ,
// which the fold alone cannot do.
type PublisherOverride struct {
	Name      string    `json:"name"`
	Known     bool      `json:"known"`
	Canonical string    `json:"canonical,omitempty"`
	Note      string    `json:"note,omitempty"`
	SetAt     time.Time `json:"set_at"`
}

// ErrEmptyName is returned for a name that folds to nothing.
var ErrEmptyName = errors.New("authority: name folds to nothing")

// PutPersonOverride stores a person override.
func PutPersonOverride(w interface{ SetRaw(string, []byte) error }, o PersonOverride) error {
	f := Fold(o.Name)
	if f == "" {
		return ErrEmptyName
	}
	val, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return w.SetRaw(personOverrideKey(f), val)
}

// PutPublisherOverride stores a publisher override.
func PutPublisherOverride(w interface{ SetRaw(string, []byte) error }, o PublisherOverride) error {
	f := Fold(o.Name)
	if f == "" {
		return ErrEmptyName
	}
	val, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return w.SetRaw(pubOverrideKey(f), val)
}

// Entry is a person as a consumer sees it: the rebuilt row with any owner
// override applied.
type Entry struct {
	Fold    string            `json:"fold"`
	Display string            `json:"display"`
	ASINs   []string          `json:"asins,omitempty"`
	Roles   map[Role]RoleStat `json:"roles"`
	// Blocked roles were switched off by an owner override.
	Blocked      []Role   `json:"blocked,omitempty"`
	Sources      []string `json:"sources"`
	Tier         Tier     `json:"tier"`
	HomonymASINs bool     `json:"homonym_asins,omitempty"`
	Overridden   bool     `json:"overridden,omitempty"`
}

// PublisherEntry is a publisher as a consumer sees it.
type PublisherEntry struct {
	Publisher
	// Canonical is the display name a consumer should write for this
	// spelling: the override's Canonical when set, otherwise Display.
	Canonical  string `json:"canonical"`
	Overridden bool   `json:"overridden,omitempty"`
	// Blocked: an owner override says this is NOT a publisher.
	Blocked bool `json:"blocked,omitempty"`
}

// Index reads the authority lists.
type Index struct{ r Reader }

// NewIndex returns an Index over r.
func NewIndex(r Reader) *Index { return &Index{r: r} }

func getJSON[T any](r Reader, key string) (*T, error) {
	raw, err := r.GetRaw(key)
	if err != nil {
		return nil, fmt.Errorf("authority: read %s: %w", key, err)
	}
	if raw == nil {
		return nil, nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("authority: decode %s: %w", key, err)
	}
	return &v, nil
}

// Lookup returns the person a name folds to, or nil when the index knows
// nothing about it. nil means "no evidence", never "not a person".
func (x *Index) Lookup(name string) (*Entry, error) {
	f := Fold(name)
	if f == "" {
		return nil, nil
	}
	base, err := getJSON[Person](x.r, PersonPrefix+f)
	if err != nil {
		return nil, err
	}
	ovr, err := getJSON[PersonOverride](x.r, personOverrideKey(f))
	if err != nil {
		return nil, err
	}
	return mergePerson(f, base, ovr), nil
}

func mergePerson(f string, base *Person, ovr *PersonOverride) *Entry {
	if base == nil && ovr == nil {
		return nil
	}
	e := &Entry{Fold: f, Roles: map[Role]RoleStat{}}
	if base != nil {
		e.Display, e.ASINs, e.Sources, e.HomonymASINs = base.Display, slices.Clone(base.ASINs), slices.Clone(base.Sources), base.HomonymASINs
		for r, st := range base.Roles {
			cp := st
			cp.ByTier = map[Tier]int{}
			for t, n := range st.ByTier {
				cp.ByTier[t] = n
			}
			e.Roles[r] = cp
		}
	}
	if ovr != nil {
		e.Overridden = true
		if strings.TrimSpace(ovr.Name) != "" {
			e.Display = strings.TrimSpace(ovr.Name)
		}
		for _, a := range ovr.ASINs {
			if n := normalizeASIN(a); n != "" && !slices.Contains(e.ASINs, n) {
				e.ASINs = append(e.ASINs, n)
			}
		}
		slices.Sort(e.ASINs)
		if !slices.Contains(e.Sources, SourceOverride) {
			e.Sources = append(e.Sources, SourceOverride)
			slices.Sort(e.Sources)
		}
		for _, r := range sortedRoles(ovr.Roles) {
			if !ovr.Roles[r] {
				delete(e.Roles, r)
				e.Blocked = append(e.Blocked, r)
				continue
			}
			st := e.Roles[r]
			if st.ByTier == nil {
				st.ByTier = map[Tier]int{}
			}
			st.ByTier[TierO]++
			st.Count++
			st.Tier = TierO
			e.Roles[r] = st
		}
	}
	e.Tier = ""
	for _, st := range e.Roles {
		e.Tier = better(e.Tier, st.Tier)
	}
	return e
}

func sortedRoles(m map[Role]bool) []Role {
	out := make([]Role, 0, len(m))
	for r := range m {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// Qualifies reports whether the entry is evidence that the person holds
// role: author per AuthorEvidenceRule (cast_author never counts), narrator
// for any structured observation, cast_author for any observation. A role an
// override blocked never qualifies.
func (e *Entry) Qualifies(role Role) bool {
	if e == nil || slices.Contains(e.Blocked, role) {
		return false
	}
	st, ok := e.Roles[role]
	if !ok {
		return false
	}
	switch role {
	case RoleAuthor:
		return AuthorEvidenceRule(st)
	case RoleNarrator:
		return narratorEvidenceRule(st)
	case RoleCastAuthor:
		return st.Count >= 1
	}
	return false
}

// IsKnownPerson reports whether the index holds evidence that name is a
// person credited as role. false means "no evidence", not "not a person".
func (x *Index) IsKnownPerson(name string, role Role) (bool, error) {
	e, err := x.Lookup(name)
	if err != nil {
		return false, err
	}
	return e.Qualifies(role), nil
}

// LookupPublisher returns the publisher a name folds to, or nil.
func (x *Index) LookupPublisher(name string) (*PublisherEntry, error) {
	f := Fold(name)
	if f == "" {
		return nil, nil
	}
	base, err := getJSON[Publisher](x.r, PublisherPrefix+f)
	if err != nil {
		return nil, err
	}
	ovr, err := getJSON[PublisherOverride](x.r, pubOverrideKey(f))
	if err != nil {
		return nil, err
	}
	return mergePublisher(f, base, ovr), nil
}

func mergePublisher(f string, base *Publisher, ovr *PublisherOverride) *PublisherEntry {
	if base == nil && ovr == nil {
		return nil
	}
	e := &PublisherEntry{}
	if base != nil {
		e.Publisher = *base
		e.Sources = slices.Clone(base.Sources)
	} else {
		e.Fold, e.Display = f, strings.TrimSpace(ovr.Name)
	}
	e.Canonical = e.Display
	if ovr != nil {
		e.Overridden = true
		e.Blocked = !ovr.Known
		if ovr.Known {
			e.Tier = TierO
		}
		if c := strings.TrimSpace(ovr.Canonical); c != "" {
			e.Canonical = c
		}
		if !slices.Contains(e.Sources, SourceOverride) {
			e.Sources = append(e.Sources, SourceOverride)
			slices.Sort(e.Sources)
		}
	}
	return e
}

// LookupASIN returns every fold a contributor ASIN was credited under, or
// nil.
func (x *Index) LookupASIN(asin string) (*ASINRef, error) {
	k := PersonASINKey(asin)
	if k == "" {
		return nil, nil
	}
	return getJSON[ASINRef](x.r, k)
}

// CanonicalPublisher is the single-lookup form of Lookup.CanonicalPublisher
// (a store read per call; bulk callers load a Snapshot instead).
func (x *Index) CanonicalPublisher(name string) (string, bool, error) {
	e, err := x.LookupPublisher(name)
	if err != nil {
		return "", false, err
	}
	c, ok := canonicalOf(e)
	return c, ok, nil
}
