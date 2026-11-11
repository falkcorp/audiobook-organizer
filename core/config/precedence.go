// file: core/config/precedence.go
// version: 1.0.0
// guid: 8ae4f77d-1d09-4950-94d0-22ad7cb0ec0e
// last-edited: 2026-11-10

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// The operator file layer (05-config §1.3.6) and the one exception to the
// operator lock, FailClosed (§1.3.5).

// Top-level keys of the canonical operator file.
const (
	fileCore    = "core"
	fileModules = "modules"
)

// healthFailClosedOverride is raised on core.config while a FailClosed key's
// stored value beats a weaker operator value (05-config §1.3.5, 05-health).
const healthFailClosedOverride = "config_fail_closed_override"

// maxAliasDepth bounds YAML alias chains when a value is converted to JSON.
const maxAliasDepth = 64

// yamlFile is the operator YAML file: a mapping nested by path segments,
// top-level keys core, modules and integrations (05-config §1.3.6). The
// values it supplies are parsed once, on first read, so Boot and Load agree;
// holds and redacted read the file again, because they describe the file on
// disk for a backup. Core never writes it.
type yamlFile struct {
	path     string
	readFile func(string) ([]byte, error)
	retired  map[string]bool // retired top-level keys: never read (retired.go)
	log      *slog.Logger

	loaded bool
	root   *yaml.Node // the top-level mapping; nil for an empty file
	err    error
	warned map[Path]bool
}

func newYAMLFile(path string, readFile func(string) ([]byte, error), retired map[string]bool, log *slog.Logger) *yamlFile {
	return &yamlFile{path: path, readFile: readFile, retired: retired, log: log, warned: map[Path]bool{}}
}

// by is a file value's lock reason (05-config §1.3.3: "file <name>").
func (f *yamlFile) by(p Path) string { return "file " + f.path + " key " + string(p) }

// parse reads and parses the file. An unreadable or unparsable file, a
// second document, or a top level that is not a mapping fails boot
// (05-config §1.5).
func (f *yamlFile) parse() (*yaml.Node, error) {
	b, err := f.readFile(f.path)
	if err != nil {
		return nil, fmt.Errorf("config: read the operator file %q: %w", f.path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // an empty file supplies nothing
		}
		return nil, fmt.Errorf("config: parse the operator file %q: %w", f.path, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: the operator file %q holds more than one YAML document", f.path)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil, nil
		}
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.ShortTag() == "!!null" {
		return nil, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config: the operator file %q is not a YAML mapping (top-level keys core, modules, integrations)", f.path)
	}
	return root, nil
}

// load is parse, once.
func (f *yamlFile) load() (*yaml.Node, error) {
	if !f.loaded {
		f.loaded = true
		f.root, f.err = f.parse()
	}
	return f.root, f.err
}

// topKeys lists the file's top-level keys (for the retired-name check).
func (f *yamlFile) topKeys() ([]string, error) {
	root, err := f.load()
	if err != nil || root == nil {
		return nil, err
	}
	var out []string
	for i := 0; i+1 < len(root.Content); i += 2 {
		out = append(out, root.Content[i].Value)
	}
	return out, nil
}

// fileWalk is one walk of the file against a query.
type fileWalk struct {
	q        sourceQuery
	paths    map[Path]bool
	prefixes map[Path]bool
	retired  map[string]bool
	// place is called with every path the store can place and its value
	// node; unknown with every path it cannot, and with each retired
	// top-level key (retired true).
	place   func(p Path, n *yaml.Node) error
	unknown func(p Path, n *yaml.Node, retired bool)
}

func newFileWalk(q sourceQuery, retired map[string]bool) *fileWalk {
	w := &fileWalk{q: q, paths: map[Path]bool{}, prefixes: map[Path]bool{}, retired: retired}
	add := func(p Path) {
		for a := p.Parent(); a != ""; a = a.Parent() {
			w.prefixes[a] = true
		}
	}
	for _, p := range q.paths {
		w.paths[p] = true
		add(p)
	}
	for _, m := range q.maps {
		w.prefixes[m] = true
		add(m)
	}
	for parent := range q.entryRels {
		w.prefixes[parent] = true
		add(parent)
	}
	return w
}

// templateLevel reports how p sits under a map section or an entry list's
// parent: key when p is "<root>.<id>.<rel>" for a known rel, prefix when it
// is "<root>.<id>" or lies between that and a rel.
func (w *fileWalk) templateLevel(p Path) (key, prefix bool) {
	check := func(root Path, rels []string, ok func(Path) bool) {
		rest, found := strings.CutPrefix(string(p), string(root)+pathSep)
		if !found {
			return
		}
		id, rel, _ := strings.Cut(rest, pathSep)
		if !validSegment(id) {
			return
		}
		if rel == "" {
			prefix = true
			return
		}
		for _, r := range rels {
			switch {
			case r == rel && (ok == nil || ok(p)):
				key = true
			case strings.HasPrefix(r, rel+pathSep):
				prefix = true
			}
		}
	}
	for _, m := range w.q.maps {
		check(m, w.q.mapRels[m], nil)
	}
	for parent, rels := range w.q.entryRels {
		check(parent, rels, w.q.entrySecret)
	}
	return key, prefix
}

// walk visits the top-level mapping.
func (w *fileWalk) walk(root *yaml.Node) error {
	if root == nil {
		return nil
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return errors.New("config: the operator file has a top-level key that is not a scalar")
		}
		name := k.Value
		if seen[name] {
			return fmt.Errorf("config: the operator file sets the top-level key %q twice", name)
		}
		seen[name] = true
		var err error
		switch {
		case w.retired[name]:
			w.unknown(Path(name), v, true)
		case name == fileCore || name == fileModules:
			err = w.tree(Path(name), v)
		case name == integrationsRoot:
			err = w.passthrough(Path(name), v)
		default:
			w.unknown(Path(name), v, false)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// children calls fn with every key of the mapping n and its value; a
// non-scalar or repeated key fails.
func children(p Path, n *yaml.Node, fn func(Path, *yaml.Node) error) error {
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.ShortTag() == "!!merge" {
			return fmt.Errorf("config: the operator file's key %s holds a key that is not a plain scalar", p)
		}
		if seen[k.Value] {
			return fmt.Errorf("config: the operator file sets %s twice", p.Join(k.Value))
		}
		seen[k.Value] = true
		if err := fn(p.Join(k.Value), v); err != nil {
			return err
		}
	}
	return nil
}

// isNull reports whether n is YAML null: an absent value supplies nothing.
func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
}

// tree walks a core or modules subtree by path segments.
func (w *fileWalk) tree(p Path, n *yaml.Node) error {
	key, prefix := w.paths[p] || w.q.entrySecret(p), w.prefixes[p]
	if tk, tp := w.templateLevel(p); tk || tp {
		key, prefix = key || tk, prefix || tp
	}
	switch {
	case key:
		return w.place(p, n)
	case prefix:
		if isNull(n) {
			return nil
		}
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("config: the operator file's key %s takes a mapping of its keys, not a value", p)
		}
		return children(p, n, w.tree)
	default:
		w.unknown(p, n, false)
		return nil
	}
}

// passthrough walks the integrations subtree: a path Options.Passthrough
// accepts is placed whole; a mapping above one is walked.
func (w *fileWalk) passthrough(p Path, n *yaml.Node) error {
	switch {
	case w.q.passthrough(p):
		return w.place(p, n)
	case isNull(n):
		return nil
	case n.Kind == yaml.MappingNode:
		return children(p, n, w.passthrough)
	default:
		w.unknown(p, n, false)
		return nil
	}
}

// read returns every value the file supplies for q. A YAML null supplies
// nothing (no value, no lock), as an empty env value. An unknown key, or a
// path no registered section owns, warns once at Load and is not returned;
// at Boot module keys are not registered yet, so nothing warns.
func (f *yamlFile) read(q sourceQuery) ([]sourced, error) {
	root, err := f.load()
	if err != nil {
		return nil, err
	}
	var out []sourced
	w := newFileWalk(q, f.retired)
	w.place = func(p Path, n *yaml.Node) error {
		if q.want != nil && !q.want[p] || isNull(n) {
			return nil
		}
		raw, err := nodeJSON(n, 0)
		if err != nil {
			return fmt.Errorf("config: %s: %w", f.by(p), invalid(&FieldError{Path: p, Err: err}))
		}
		out = append(out, sourced{path: p, raw: raw, by: f.by(p)})
		return nil
	}
	w.unknown = func(p Path, _ *yaml.Node, retired bool) {
		if retired || !q.strict || q.want != nil || f.warned[p] {
			return
		}
		f.warned[p] = true
		f.log.Warn("config: the operator file sets a key no registered section owns; it is ignored", "file", f.path, "key", p)
	}
	if q.want != nil {
		// A wanted path no section owns yet is read straight from the tree
		// (the kernel's step 0 reads core.paths.state_dir and
		// core.storage.path before they register).
		for _, p := range slices.Sorted(maps.Keys(q.want)) {
			if n := lookupNode(root, p); n != nil && !isNull(n) {
				raw, err := nodeJSON(n, 0)
				if err != nil {
					return nil, fmt.Errorf("config: %s: %w", f.by(p), invalid(&FieldError{Path: p, Err: err}))
				}
				out = append(out, sourced{path: p, raw: raw, by: f.by(p)})
			}
		}
		return out, nil
	}
	if err := w.walk(root); err != nil {
		return nil, err
	}
	return out, nil
}

// lookupNode follows p's segments through nested mappings.
func lookupNode(root *yaml.Node, p Path) *yaml.Node {
	n := root
	for seg := range strings.SplitSeq(string(p), pathSep) {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == seg {
				next = n.Content[i+1]
			}
		}
		n = next
	}
	return n
}

// holds reports whether the file on disk supplies a value at a path drop
// accepts. Failing closed, a key config cannot place (an unknown or retired
// key, which may be a stale credential) counts as held.
func (f *yamlFile) holds(q sourceQuery, drop func(Path) bool) (bool, error) {
	root, err := f.parse()
	if err != nil {
		return false, err
	}
	held := false
	w := newFileWalk(q, f.retired)
	w.place = func(p Path, n *yaml.Node) error {
		held = held || (!isNull(n) && drop(p))
		return nil
	}
	w.unknown = func(Path, *yaml.Node, bool) { held = true }
	if err := w.walk(root); err != nil {
		return false, err
	}
	return held, nil
}

// redacted renders the file on disk as YAML without the values at paths
// drop accepts, without every key config cannot place (failing closed, as
// holds), and without comments (a commented-out credential is still one).
func (f *yamlFile) redacted(q sourceQuery, drop func(Path) bool) ([]byte, error) {
	root, err := f.parse()
	if err != nil {
		return nil, err
	}
	if root == nil {
		return []byte{}, nil
	}
	gone := map[*yaml.Node]bool{}
	w := newFileWalk(q, f.retired)
	w.place = func(p Path, n *yaml.Node) error {
		if drop(p) {
			gone[n] = true
		}
		return nil
	}
	w.unknown = func(_ Path, n *yaml.Node, _ bool) { gone[n] = true }
	if err := w.walk(root); err != nil {
		return nil, err
	}
	prune(root, gone)
	return yaml.Marshal(root)
}

// prune removes from n every mapping pair whose value is in gone, then every
// mapping left empty by that, and every comment.
func prune(n *yaml.Node, gone map[*yaml.Node]bool) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	if n.Kind != yaml.MappingNode {
		for _, c := range n.Content {
			prune(c, gone)
		}
		return
	}
	kept := n.Content[:0]
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if gone[v] {
			continue
		}
		hadPairs := v.Kind == yaml.MappingNode && len(v.Content) > 0
		prune(k, gone)
		prune(v, gone)
		if hadPairs && len(v.Content) == 0 {
			continue
		}
		kept = append(kept, k, v)
	}
	n.Content = kept
}

// nodeJSON converts a YAML value to JSON by its resolved tags, losslessly:
// a string stays the string written (an unquoted date is not reformatted),
// numbers and booleans keep their type, so strict decoding sees what the
// operator wrote (05-config §1.3.6). Its errors never quote the value.
func nodeJSON(n *yaml.Node, depth int) (json.RawMessage, error) {
	v, err := nodeValue(n, depth)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, errors.New("the value has no JSON form")
	}
	return b, nil
}

// nodeValue is nodeJSON's walk.
func nodeValue(n *yaml.Node, depth int) (any, error) {
	if depth > maxAliasDepth {
		return nil, errors.New("the value nests or aliases too deeply")
	}
	switch n.Kind {
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, errors.New("an alias names no anchor")
		}
		return nodeValue(n.Alias, depth+1)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := nodeValue(c, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.ShortTag() == "!!merge" {
				return nil, errors.New("a mapping key is not a plain scalar (merge keys are not supported)")
			}
			if _, dup := out[k.Value]; dup {
				return nil, errors.New("a mapping sets one key twice")
			}
			v, err := nodeValue(n.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			out[k.Value] = v
		}
		return out, nil
	case yaml.ScalarNode:
		return scalarValue(n)
	}
	return nil, errors.New("an unsupported YAML node")
}

// scalarValue is one scalar by its resolved tag.
func scalarValue(n *yaml.Node) (any, error) {
	switch n.ShortTag() {
	case "!!str", "!!timestamp":
		return n.Value, nil
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return nil, errors.New("not a boolean")
		}
		return b, nil
	case "!!int":
		var i int64
		if err := n.Decode(&i); err == nil {
			return i, nil
		}
		var u uint64
		if err := n.Decode(&u); err == nil {
			return u, nil
		}
		return nil, errors.New("an integer out of range")
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, errors.New("not a finite number")
		}
		// Kept as written when it is a JSON number, so 1.10 is not 1.1.
		if json.Valid([]byte(n.Value)) {
			return json.Number(n.Value), nil
		}
		return f, nil
	}
	return nil, fmt.Errorf("the YAML tag %s is not supported", n.ShortTag())
}

// failRank is raw's place in k's FailClosed order: 0 is the most restrictive;
// len(order) for a value the order does not list (never, after Register).
func (k *keyInfo) failRank(raw json.RawMessage) int {
	for i, o := range k.failOrder {
		if bytes.Equal(o, raw) {
			return i
		}
	}
	return len(k.failOrder)
}

// storedFor is c's stored value in v: the boot record for a boot key, the DB
// layer otherwise; never for a ProtectOperator or secret key.
func storedFor(c ckey, v view) (json.RawMessage, Source, bool) {
	k := c.k
	switch {
	case k.secret || k.protect == ProtectOperator:
		return nil, 0, false
	case k.reload == ReloadBoot:
		r, ok := v.boot[c.p]
		return r, SourceBootRecord, ok
	case k.reload == ReloadRestart:
		r, ok := v.restart[c.p]
		return r, SourceOverride, ok
	}
	r, ok := v.live[c.p]
	return r, SourceOverride, ok
}

// failClosedWins is the fail-closed exception to the operator lock (05-config
// §1.3.5): for a FailClosed key whose operator value is o, a stored value
// that is more restrictive by the declared order is effective. by keeps the
// operator source, for KeyState.LockedBy and the report.
func failClosedWins(c ckey, v view, o opValue) (keyVal, bool) {
	if c.k.failOrder == nil {
		return keyVal{}, false
	}
	stored, src, ok := storedFor(c, v)
	if !ok || c.k.failRank(stored) >= c.k.failRank(o.raw) {
		return keyVal{}, false
	}
	return keyVal{raw: stored, src: src, by: o.by}, true
}

// failClosedApply reports whether Apply may store raw (canonical, or unset)
// on a FailClosed key whose operator value is o: an unset, or a value at
// least as restrictive as o's (05-config §1.3.5). A value that does not
// decode passes here and is refused by the decode stage.
func failClosedApply(k *keyInfo, p Path, raw json.RawMessage, unset bool, o opValue) bool {
	if k.failOrder == nil {
		return false
	}
	if unset {
		return true
	}
	c, err := k.canonical(p, raw)
	if err != nil {
		return true
	}
	return k.failRank(c) <= k.failRank(o.raw)
}

// syncFailClosed recomputes which FailClosed keys' stored value beats their
// operator value in the effective builds: AttachReport.FailClosed, health
// config_fail_closed_override, the gauge, and one warning per key per boot.
func (s *Store) syncFailClosed() {
	wins := map[Path]string{}
	for _, sec := range s.sections {
		if sec.eff == nil {
			continue
		}
		for _, c := range sec.eff.keys {
			if c.k.failOrder == nil {
				continue
			}
			op, locked := s.op[c.p]
			if kv, ok := sec.eff.vals[c.p]; locked && ok && !kv.src.operator() {
				wins[c.p] = op.by
			}
		}
	}
	s.fcWins = wins
	s.met.failClosed.Store(int64(len(wins)))
	if len(wins) == 0 {
		delete(s.health, healthFailClosedOverride)
		return
	}
	s.health[healthFailClosedOverride] = fmt.Sprintf("%d FailClosed keys keep a stored value stricter than the operator's", len(wins))
	for _, p := range slices.Sorted(maps.Keys(wins)) {
		if s.fcLogged[p] {
			continue
		}
		s.fcLogged[p] = true
		s.log.Warn("config: a stored value is stricter than the operator value of a fail-closed key; the stored value is effective",
			"key", p, "operator", wins[p])
	}
}
