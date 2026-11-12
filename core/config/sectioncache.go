// file: core/config/sectioncache.go
// version: 1.0.1
// guid: e92e6553-a421-4c45-84ef-2bc4c13c0d65
// last-edited: 2026-11-12

package config

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
)

// A section's build (buildSection) is a pure function of its sectionSpec: the
// reflected schema, the key walk, the declared defaults, and the per-key
// vendor keywords, resolved schemas and canonical defaults read nothing of the
// store, its AppID or any runtime value. It is also the expensive part of
// Register (a schema reflect, a Resolve and a Marshal per key; 154ms for the
// kernel's core sections under -race), and the kernel builds a core config
// store on every boot, so a test that boots two hundred kernels built the
// same sections two hundred times (F2-16 follow-up).
//
// Each distinct spec is therefore built once per process, into a template no
// store ever sees, and every Register takes its own instance of that template.
// An instance is a fresh *section with fresh *keyInfo values (the store-owned,
// mutable parts: bind, eff and pend on the section; the sec, list and
// entrySecrets links and every byte slice, map and slice on a key) that shares
// with the template only what nothing writes after buildSection returns: the
// reflected schema trees and the resolved validators.
//
// Why those are never written after the build, checked by reading every
// assignment to and every reader of a built section (the field table in
// sectioncache_test.go fails when a field is added without being classified):
//
//   - section.schema and keyInfo.schema (a sub-tree of it) and the entry
//     secrets' schemas are written only by buildSection, decorate and
//     decorateEntrySecrets, all before the build returns. After it they are
//     read by json.Marshal (Store.Schema, whose Schema.MarshalJSON copies the
//     struct and never writes to it), by keyInfo.schemaAllows (Enum) and by
//     entrySchema/subSchema (Properties, AdditionalProperties). None of them
//     calls Resolve, which is what mutates a schema; the validators are built
//     from CloneSchemas copies, so they share no node with these trees.
//   - keyInfo.resolved is a *jsonschema.Resolved, read through Validate only.
//     Validate allocates its own state per call and writes to no schema, so any
//     number of stores may validate with one concurrently.
//   - section.claimPaths is assigned once, by buildSection, and read by
//     checkOverlap and the overlap index, which only range over it.
//   - The one writer of a published section is Store.register (sec.bind) and
//     Store.install (sec.eff, sec.pend): both write the instance, never the
//     template.
//
// Everything else a store could write or hand to a caller (json.RawMessage
// defaults and schemas reach the caller as KeyState.Schema and Effective,
// reservedIDs and bindNames are read in place) is copied per instance, so the
// bytes a caller holds are private to its store, as they were before the cache.

// maxSectionTemplates bounds the cache. The set of distinct specs is static
// for a binary: the core sections and each module's, each with defaults fixed
// in source. It is not unbounded in principle, though: a spec whose defaults
// are computed at boot (core.paths takes the state directory the boot chose,
// which a test that boots in a fresh temporary directory changes every time)
// is a new spec on each distinct value. Past the bound a spec is built
// uncached and stays out of the cache, so a pathological caller costs what it
// always cost and the cache never grows without limit. A thousand and twenty
// four templates is two orders of magnitude over a real app's sections.
const maxSectionTemplates = 1024

// sharedTemplates is the process-wide cache Store.register builds through.
var sharedTemplates = newTemplateCache(maxSectionTemplates)

// errTemplatePanicked is what a caller that lost the single-flight race to a
// build that panicked sees; the panic itself reaches only the goroutine that
// ran it.
var errTemplatePanicked = errors.New("config: a concurrent build of this section panicked")

// specKey identifies a spec by content. It is comparable, and two specs share
// one only when their canonical bytes are equal, so no hash can collide.
type specKey struct {
	typ   reflect.Type
	canon string
}

// templateEntry is one single-flight build: the first caller builds under
// once, every other caller of the key waits on it.
type templateEntry struct {
	once sync.Once
	tpl  *section
	err  error
}

// templateCache holds the built, frozen sections of the specs it has seen.
type templateCache struct {
	max    int
	mu     sync.Mutex
	m      map[specKey]*templateEntry
	builds atomic.Int64 // buildSection runs the cache made, for tests
}

func newTemplateCache(max int) *templateCache {
	return &templateCache{max: max, m: map[specKey]*templateEntry{}}
}

// section returns sp built as buildSection builds it: a *section the caller
// owns. A failed build is not cached (it is deterministic, so a retry fails the
// same way, and a failure leaves nothing to share).
func (c *templateCache) section(sp *sectionSpec) (*section, error) {
	key, ok := keyOfSpec(sp)
	if !ok {
		return buildSection(sp)
	}
	c.mu.Lock()
	e := c.m[key]
	if e == nil {
		if len(c.m) >= c.max {
			c.mu.Unlock()
			return buildSection(sp)
		}
		e = &templateEntry{}
		c.m[key] = e
	}
	c.mu.Unlock()

	e.once.Do(func() {
		defer func() {
			if e.tpl == nil && e.err == nil {
				e.err = errTemplatePanicked
			}
		}()
		c.builds.Add(1)
		tpl, err := buildSection(sp)
		if err != nil {
			e.err = err
			return
		}
		// The template is anyone's and no one's: it does not keep the first
		// caller's spec. Each instance carries its own caller's.
		tpl.spec = nil
		e.tpl = tpl
	})
	if e.err != nil {
		c.mu.Lock()
		if c.m[key] == e {
			delete(c.m, key)
		}
		c.mu.Unlock()
		return nil, e.err
	}
	return e.tpl.instantiate(sp), nil
}

// size is the number of cached templates.
func (c *templateCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// keyOfSpec derives sp's cache key from every input buildSection reads: the
// path, the section's Go type, the marshaled defaults, both permissions, and
// the Docs, ProtectionWaivers, EnvBinding and FailClosed options. ok is false
// for a spec the cache must not hold: one Register did not make (no type;
// buildSection refuses it) or whose defaults do not marshal (buildSection
// reports that).
//
// The spec holds no function value the build calls: the schema is
// jsonschemax.ForType of the type with storeOverrides, neither of which reads
// any process state, so the type in the key stands for the schema. The option
// closures a Section records are not read here either: Section.spec applies
// them, and what they wrote into the spec is what the key reads. A new field
// on sectionSpec that buildSection reads must be added here; the table in
// sectioncache_internal_test.go fails until it is classified.
//
// The canonical bytes length-prefix every string, so no two different specs
// encode alike, and sort every map, so one spec always encodes alike.
func keyOfSpec(sp *sectionSpec) (specKey, bool) {
	if sp.typ == nil {
		return specKey{}, false
	}
	defaults, err := json.Marshal(sp.defaults)
	if err != nil {
		return specKey{}, false
	}
	var b []byte
	str := func(s string) {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	num := func(n int) { b = binary.AppendUvarint(b, uint64(n)) }
	pathMap := func(m map[Path]string) {
		num(len(m))
		for _, p := range slices.Sorted(maps.Keys(m)) {
			str(string(p))
			str(m[p])
		}
	}
	str(string(sp.path))
	str(string(defaults))
	str(sp.permRead)
	str(sp.permWrite)
	pathMap(sp.docs)
	pathMap(sp.waivers)
	num(len(sp.envBindings))
	for _, eb := range sp.envBindings {
		str(string(eb.path))
		str(eb.name)
	}
	num(len(sp.failClosed))
	for _, fc := range sp.failClosed {
		str(string(fc.path))
		num(len(fc.order))
		for _, o := range fc.order {
			str(string(o))
		}
	}
	return specKey{typ: sp.typ, canon: string(b)}, true
}

// instantiate returns a section of its own that shares t's schema trees and
// validators and nothing else. t is not changed; sp is the caller's spec,
// equal in content to the one t was built from. A key that points at another
// (a derived entry-secret key and its list) points at the copy.
func (t *section) instantiate(sp *sectionSpec) *section {
	s := &section{
		path:      t.path,
		spec:      sp,
		isMap:     t.isMap,
		permRead:  t.permRead,
		permWrite: t.permWrite,
		schema:    t.schema, // shared: frozen once built
		defaults:  slices.Clone(t.defaults),
		elemZero:  slices.Clone(t.elemZero),
		// shared: the claimed paths are strings in a slice nothing writes
		// after the build (computeClaims; every reader only ranges over it).
		claimPaths: t.claimPaths,
	}
	copies := make(map[*keyInfo]*keyInfo, len(t.keys))
	var cp func(k *keyInfo) *keyInfo
	cp = func(k *keyInfo) *keyInfo {
		if k == nil {
			return nil
		}
		if n, ok := copies[k]; ok {
			return n
		}
		n := new(keyInfo)
		*n = *k
		copies[k] = n // before the links below: a list and its derived keys point at each other
		n.sec = s
		n.rel = slices.Clone(k.rel)
		n.index = slices.Clone(k.index)
		n.schemaRaw = slices.Clone(k.schemaRaw)
		n.def = slices.Clone(k.def)
		if k.failOrder != nil {
			n.failOrder = make([]json.RawMessage, len(k.failOrder))
			for i, o := range k.failOrder {
				n.failOrder[i] = slices.Clone(o)
			}
		}
		n.reservedIDs = maps.Clone(k.reservedIDs)
		n.bindNames = slices.Clone(k.bindNames)
		n.list = cp(k.list)
		if k.entrySecrets != nil {
			n.entrySecrets = make([]*keyInfo, len(k.entrySecrets))
			for i, d := range k.entrySecrets {
				n.entrySecrets[i] = cp(d)
			}
		}
		return n
	}
	s.keys = make([]*keyInfo, len(t.keys))
	s.byRel = make(map[string]*keyInfo, len(t.byRel))
	for i, k := range t.keys {
		s.keys[i] = cp(k)
	}
	for rel, k := range t.byRel {
		s.byRel[rel] = cp(k)
	}
	if t.entryLists != nil {
		s.entryLists = make([]*keyInfo, len(t.entryLists))
		for i, lk := range t.entryLists {
			s.entryLists[i] = cp(lk)
		}
	}
	return s
}
