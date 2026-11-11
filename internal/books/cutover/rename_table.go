// file: internal/books/cutover/rename_table.go
// version: 1.0.0
// guid: 5f0dc15e-6a07-4d86-8d7f-28b5bd6aa771
// last-edited: 2026-11-11

package cutover

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/config"
)

// The rename table (05-config §1.12.3): one row per HEAD setting, generated
// from the frozen Config (head_config.go) in source order, so a HEAD field
// no row covers cannot exist. The explicit rows and the section table are the
// spec's, copied into rename_data.go; this file applies them, derives the
// retired names, the canonical env names and the refuse-or-warn verdicts, and
// refuses to produce a table that is ambiguous or incomplete. The table is
// what M2 (config_step.go) reads and what the golden (testdata/
// rename_table.golden) pins.

// Class is a row's class in the spec's counts (§1.12.3).
type Class string

const (
	ClassKey     Class = "k" // a core or books key M2 stages
	ClassSecret  Class = "s" // a secret M2 moves into the secrets store
	ClassInput   Class = "i" // an input of a later step: its fields stay in the blob for M6 (secrets moved out)
	ClassEnv     Class = "o" // an operator-layer (env/file) field: nothing is stored, nothing migrates
	ClassDropped Class = "x" // no longer read: logged when non-default, never staged
)

// Stored says where HEAD kept the value.
type Stored string

const (
	StoredBlob    Stored = "blob"     // a field of the settings blob
	StoredRow     Stored = "row"      // one of HEAD's four encrypted rows
	StoredEnvFile Stored = "env/file" // operator layer only, never stored
)

// TargetKind is what a row's value becomes.
type TargetKind string

const (
	TargetKey         TargetKind = "key"         // a config key (config.Importer.Stage)
	TargetSecret      TargetKind = "secret"      // a secret (secrets.Importer.ImportSecret)
	TargetInstance    TargetKind = "instance"    // an integrations instance field, left in the blob for M6
	TargetEntry       TargetKind = "entry"       // an entry of a list-valued key, built from several rows
	TargetFold        TargetKind = "fold"        // one of several rows folded into one key's list
	TargetPassthrough TargetKind = "passthrough" // an integrations passthrough path (operator layer only)
)

// Target is one place a row's value goes.
type Target struct {
	Kind    TargetKind
	Path    string // the key or secret name; a description for an instance, entry or fold
	List    string // the list-valued key an entry or fold belongs to
	Convert string // the converter name, "" for identity
	Coined  bool   // the spec coins the name (no owner declares it yet)
	// Dest marks a passthrough destination (a host or URL), whose retired name
	// refuses; Passthrough marks a secret under an integrations path.
	Dest        bool
	Passthrough bool
	// Shared marks a key two rows may both name (HEAD binds both to one
	// setting); any other repeated path fails the generator.
	Shared bool
}

// Row is one generated rename-table row.
type Row struct {
	Head       string // HEAD name; dotted for a sub-key row
	Top        bool   // a top-level field of Config (what the spec's counts cover)
	Stored     Stored
	Class      Class
	BlobField  string // the settings blob's top-level field, "" when not a blob field
	Targets    []Target
	FailClosed bool
	Admin      bool // ProtectAdmin or higher in its owner (core's key tables, HEAD's protected-field classes)
	URL        bool // a URL-valued key: M2 splits userinfo out of it
	Note       string
	Retired    []RetiredEntry
	Container  bool // a struct-typed row: only its leaves carry values
	Removed    bool // a key HEAD removed (headRemovedKeys): a dropped row
	Process    bool // a process-environment name HEAD reads outside viper
}

// Verdict is the §1.3.6 outcome of a retired name that is set.
type Verdict string

const (
	VerdictRefuse Verdict = "refuse"
	VerdictWarn   Verdict = "warn"
	VerdictKept   Verdict = "kept" // an ecosystem binding: not retired
)

// RetiredEntry is one retired name of a row.
type RetiredEntry struct {
	Name      string
	File      bool // an operator-file key (a flat top-level key, nested keys included)
	Canonical config.Path
	Verdict   Verdict
	Refuse    bool // the app states the verdict for a passthrough path (config.RetiredName.Refuse)
	Message   string
}

// ecosystemEnvNames are the env names kept as ecosystem bindings, not
// retired (05-config §1.12.4): the four OTEL names core's metrics and
// tracing sections bind, and the two OPENAI names the app binds onto the
// ai/openai instance's passthrough paths.
var ecosystemEnvNames = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_METRIC_EXPORT_INTERVAL",
	"OTEL_EXPORTER_OTLP_METRICS_INSECURE",
	"OPENAI_API_KEY",
	"OPENAI_BASE_URL",
}

// RenameTable is the generated table.
type RenameTable struct {
	Rows   []Row
	byHead map[string]*Row
}

// Row returns the row of HEAD name head.
func (t *RenameTable) Row(head string) (*Row, bool) {
	r, ok := t.byHead[head]
	return r, ok
}

var (
	tableOnce sync.Once
	tableVal  *RenameTable
	tableErr  error
)

// Table returns the generated rename table. The generator's inputs are
// compiled in, so a failure is a defect of this package that
// TestRenameTable_Golden catches; Table panics rather than hand out a
// partial table.
func Table() *RenameTable {
	tableOnce.Do(func() { tableVal, tableErr = generateTable() })
	if tableErr != nil {
		panic("cutover: rename table: " + tableErr.Error())
	}
	return tableVal
}

// ---- generation ----

type generator struct {
	rows     []Row
	idx      map[string]int
	explicit map[string]explicitRow
	used     map[string]bool
	subs     map[string]subRow
	errs     []string
}

func (g *generator) fail(format string, args ...any) {
	g.errs = append(g.errs, fmt.Sprintf(format, args...))
}

func (g *generator) has(head string) bool { _, ok := g.idx[head]; return ok }

// add appends a row; a second row of one HEAD name fails the generator.
func (g *generator) add(r Row) {
	if g.has(r.Head) {
		g.fail("row %s is generated twice", r.Head)
		return
	}
	g.idx[r.Head] = len(g.rows)
	g.rows = append(g.rows, r)
}

func generateTable() (*RenameTable, error) {
	g := &generator{
		idx:      map[string]int{},
		explicit: map[string]explicitRow{},
		used:     map[string]bool{},
		subs:     map[string]subRow{},
	}
	for _, e := range explicitRows {
		if _, dup := g.explicit[e.head]; dup {
			g.fail("explicit row %s is listed twice", e.head)
		}
		g.explicit[e.head] = e
	}
	for _, s := range subRows {
		if _, dup := g.subs[s.head]; dup {
			g.fail("sub-key row %s is listed twice", s.head)
		}
		g.subs[s.head] = s
	}

	ct := reflect.TypeFor[headConfig]()
	for i := 0; i < ct.NumField(); i++ {
		g.topField(ct.Field(i))
	}
	for _, e := range explicitRows {
		if !g.used[e.head] {
			g.fail("explicit row %s names no field of the frozen Config", e.head)
		}
	}
	for _, s := range subRows {
		if !g.used[s.head] {
			g.fail("sub-key row %s names no leaf of the frozen Config", s.head)
		}
	}
	g.virtualRows()
	g.removedRows()
	g.processRows()
	g.checkTargets()
	if len(g.errs) == 0 {
		g.attachRetired()
	}
	if len(g.errs) > 0 {
		return nil, fmt.Errorf("%d problem(s): %s", len(g.errs), strings.Join(g.errs, "; "))
	}
	t := &RenameTable{Rows: g.rows, byHead: make(map[string]*Row, len(g.rows))}
	for i := range t.Rows {
		t.byHead[t.Rows[i].Head] = &t.Rows[i]
	}
	return t, nil
}

// fieldName is a HEAD field's name: the json tag, or the mapstructure tag
// where json is "-" (the operator-layer-only fields).
func fieldName(sf reflect.StructField) (name string, envOnly bool) {
	j, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
	if j != "" && j != "-" {
		return j, false
	}
	m, _, _ := strings.Cut(sf.Tag.Get("mapstructure"), ",")
	if j == "-" && m != "" {
		return m, true
	}
	return "", false
}

// topField generates the row of one top-level field and, for a struct-typed
// field, the rows of its leaves.
func (g *generator) topField(sf reflect.StructField) {
	name, envOnly := fieldName(sf)
	if name == "" {
		g.fail("Config field %s has neither a json nor a mapstructure name", sf.Name)
		return
	}
	isStruct := sf.Type.Kind() == reflect.Struct
	var row Row
	var leaf func(head, rel string)
	if e, ok := g.explicit[name]; ok {
		g.used[name] = true
		row = Row{Head: name, Top: true, Class: e.class, Stored: e.stored, Targets: slices.Clone(e.targets),
			FailClosed: e.failClosed, Admin: e.admin, URL: e.url, Note: e.note, Container: isStruct}
		if isStruct {
			leaf = g.explicitLeaf(name)
		}
	} else {
		sec, lf, whole, ok, err := assignSection(name)
		if err != nil {
			g.fail("%v", err)
			return
		}
		if !ok {
			g.fail("HEAD field %s matches no explicit row and no section", name)
			return
		}
		row = Row{Head: name, Top: true, Container: isStruct, URL: urlRows[name]}
		base := "modules.books." + sec + "." + lf
		path := base
		if whole {
			base = "modules.books." + sec
			path = base
		}
		if isStruct {
			path += ".*"
		}
		kind := TargetKey
		if classOverride[name] == ClassSecret {
			kind = TargetSecret
		}
		row.Targets = []Target{{Kind: kind, Path: path}}
		leaf = func(head, rel string) { g.sectionLeaf(name, head, rel, base) }
	}
	if row.Class == "" {
		row.Class = ClassKey
		if envOnly {
			row.Class = ClassEnv
		}
	}
	if c, ok := classOverride[name]; ok {
		row.Class = c
	}
	if row.Stored == "" {
		row.Stored = StoredBlob
		if envOnly {
			row.Stored = StoredEnvFile
		}
	}
	if row.Stored == StoredBlob {
		row.BlobField = name
	}
	row.Admin = adminClass(name, row.Admin, row.Targets)
	g.add(row)
	switch {
	case name == "scheduled":
		g.scheduledRows(sf.Type)
	case isStruct && leaf != nil && row.Class != ClassDropped:
		g.leaves(sf.Type, name, name, leaf)
	}
}

// leaves calls leaf for every leaf below struct type t: a non-struct field, or
// a struct field a sub-key row names itself. rel is the leaf's name below
// the top-level field.
func (g *generator) leaves(t reflect.Type, top, prefix string, leaf func(head, rel string)) {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		name, _ := fieldName(sf)
		if name == "" {
			continue
		}
		head := prefix + "." + name
		_, named := g.subs[head]
		if sf.Type.Kind() == reflect.Struct && !named {
			g.leaves(sf.Type, top, head, leaf)
			continue
		}
		leaf(head, strings.TrimPrefix(head, top+"."))
	}
}

// sectionLeaf is the leaf rule of a section-assigned struct: the generic key
// below the section path, unless a sub-key row says otherwise.
func (g *generator) sectionLeaf(top, head, rel, base string) {
	if s, ok := g.subs[head]; ok {
		g.used[head] = true
		g.addSub(top, head, s)
		return
	}
	targets := []Target{{Kind: TargetKey, Path: base + "." + rel}}
	g.add(Row{Head: head, Class: ClassKey, Stored: StoredBlob, BlobField: top, Admin: adminClass(head, false, targets), URL: urlRows[head], Targets: targets})
}

// explicitLeaf is the leaf rule of an explicit struct row: a sub-key row for
// every leaf, except maintenance's remainder (every other maintenance.<field>
// is the books key of the same name).
func (g *generator) explicitLeaf(top string) func(head, rel string) {
	return func(head, rel string) {
		if s, ok := g.subs[head]; ok {
			g.used[head] = true
			g.addSub(top, head, s)
			return
		}
		if top == "maintenance" {
			targets := []Target{{Kind: TargetKey, Path: "modules.books.maintenance." + rel}}
			g.add(Row{Head: head, Class: ClassKey, Stored: StoredBlob, BlobField: top, Admin: adminClass(head, false, targets), Targets: targets})
			return
		}
		g.fail("leaf %s of explicit row %s has no sub-key row", head, top)
	}
}

func (g *generator) addSub(top, head string, s subRow) {
	g.add(Row{Head: head, Class: s.class, Stored: StoredBlob, BlobField: top, Targets: slices.Clone(s.targets), Note: s.note,
		Admin: adminClass(head, s.admin, s.targets), URL: urlRows[head]})
}

// scheduledRows generates one row per entry of the scheduled block.
func (g *generator) scheduledRows(t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		name, _ := fieldName(sf)
		if name == "" || sf.Type.Kind() != reflect.Struct {
			continue
		}
		conv := ConvertScheduleStandard
		switch {
		case scheduledIntervalOnly[name]:
			conv = ConvertScheduleInterval
		case name == "library_scan_full":
			conv = ConvertScheduleFullScan
		}
		r := Row{Head: "scheduled." + name, Class: ClassKey, Stored: StoredBlob, BlobField: "scheduled",
			Targets: []Target{{Kind: TargetKey, Path: "modules.books.scheduler.schedules." + name, Convert: conv}}}
		if extra, ok := scheduledExtraTarget[name]; ok {
			r.Targets = append(r.Targets, extra)
			r.Note = "a second target: the same ScheduleConfig also seeds the core kind core.storage.compact's key"
		}
		g.add(r)
	}
}

// virtualRows are the sub-key rows of maps and slices' members.
func (g *generator) virtualRows() {
	for _, v := range virtualRows {
		g.add(Row{Head: v.head, Class: v.class, Stored: StoredBlob, BlobField: virtualTop(v.head), Targets: slices.Clone(v.targets),
			Note: v.note, Admin: v.admin || strings.HasPrefix(v.head, "plugins.<id>")})
	}
}

func virtualTop(head string) string {
	for i := 0; i < len(head); i++ {
		if head[i] == '.' || head[i] == '[' {
			return head[:i]
		}
	}
	return head
}

// removedRows are HEAD's removed keys as dropped rows with HEAD's message.
func (g *generator) removedRows() {
	for _, rk := range headRemovedKeys {
		g.add(Row{Head: rk.key, Removed: true, Class: ClassDropped, Stored: StoredBlob, BlobField: virtualTop(rk.key), Note: "removed: " + rk.message})
	}
}

// processRows are the two ABK_* process-environment names.
func (g *generator) processRows() {
	for _, p := range processNames {
		g.add(Row{Head: p.name, Process: true, Class: ClassEnv, Stored: StoredEnvFile, Admin: true,
			Targets: []Target{{Kind: TargetKey, Path: p.canonical}}, Note: "a process-environment name HEAD reads outside viper"})
	}
}

// checkTargets fails a repeated key or secret path (but a Shared pair) and an
// unknown converter.
func (g *generator) checkTargets() {
	seen := map[string]*Target{}
	for i := range g.rows {
		for j := range g.rows[i].Targets {
			t := &g.rows[i].Targets[j]
			if t.Convert != "" && !knownConvert(t.Convert) {
				g.fail("row %s: unknown converter %q", g.rows[i].Head, t.Convert)
			}
			if t.Kind != TargetKey && t.Kind != TargetSecret && t.Kind != TargetPassthrough {
				continue
			}
			p := strings.TrimSuffix(t.Path, ".*")
			if !isConcretePath(p) || strings.HasSuffix(t.Path, ".*") {
				continue
			}
			key := string(t.Kind) + ":" + p
			if t.Kind == TargetPassthrough {
				key = "key:" + p
			}
			if prev, dup := seen[key]; dup {
				if !(prev.Shared && t.Shared) {
					g.fail("row %s: %s path %s is already a target of another row", g.rows[i].Head, t.Kind, t.Path)
				}
				continue
			}
			seen[key] = t
		}
	}
}

// ---- section assignment ----

// assignSection applies the spec's section table: exact name (a struct a
// section takes whole), exact key, then longest prefix; two matches of one
// rank are an error.
func assignSection(name string) (sec, leaf string, whole, ok bool, err error) {
	var ex, ek []section
	for _, s := range sectionTable {
		if slices.Contains(s.whole, name) {
			ex = append(ex, s)
		}
		if slices.Contains(s.keys, name) {
			ek = append(ek, s)
		}
	}
	switch {
	case len(ex) == 1:
		return ex[0].name, name, true, true, nil
	case len(ex) > 1:
		return "", "", false, false, fmt.Errorf("section table: %s is the whole-struct name of %d sections", name, len(ex))
	case len(ek) == 1:
		return ek[0].name, stripPrefix(name, ek[0].strip), false, true, nil
	case len(ek) > 1:
		return "", "", false, false, fmt.Errorf("section table: %s is an exact key of %d sections", name, len(ek))
	}
	best, bestLen, ties := section{}, -1, 0
	for _, s := range sectionTable {
		for _, p := range s.prefixes {
			if !strings.HasPrefix(name, p) {
				continue
			}
			switch {
			case len(p) > bestLen:
				best, bestLen, ties = s, len(p), 1
			case len(p) == bestLen && s.name != best.name:
				ties++
			}
		}
	}
	if ties > 1 {
		return "", "", false, false, fmt.Errorf("section table: %s matches %d prefixes of equal length", name, ties)
	}
	if bestLen < 0 {
		return "", "", false, false, nil
	}
	return best.name, stripPrefix(name, best.strip), false, true, nil
}

func stripPrefix(name, strip string) string {
	if strip != "" && strings.HasPrefix(name, strip) && len(name) > len(strip) {
		return name[len(strip):]
	}
	return name
}

// adminClass is a row's ProtectAdmin-or-higher condition: for a core target
// the explicit flag (core's own declaration), for a books key or anything
// else HEAD's protected-field class too (§1.3.7 "keeps that class").
func adminClass(head string, explicit bool, targets []Target) bool {
	for _, t := range targets {
		if p := t.ConfigPath(); strings.HasPrefix(string(p), "core.") {
			return explicit
		}
	}
	return explicit || headProtectedName(head)
}

// headProtectedName reports whether HEAD's configFieldRules classes name (or a
// key above it) as a sign-in, executable, database or server-path field.
func headProtectedName(name string) bool {
	for _, p := range headProtected {
		if name == p || strings.HasPrefix(name, p+".") {
			return true
		}
	}
	return false
}

// ---- paths ----

// isConcretePath reports whether p names one key or secret (not a
// description, a wildcard or a placeholder). Segments are
// [a-z][a-z0-9_-]*, so an integrations secret name with a hyphen is one.
func isConcretePath(p string) bool {
	if p == "" || strings.ContainsAny(p, "*<>[] ,") {
		return false
	}
	for _, seg := range strings.Split(p, ".") {
		if seg == "" || seg[0] < 'a' || seg[0] > 'z' {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}

// ConfigPath is the canonical path of target t: the key or secret, the
// list-valued key of an entry or fold, the prefix of a `.*` container, and ""
// where the value becomes an instance field (a later step's) or nothing.
func (t Target) ConfigPath() config.Path {
	switch t.Kind {
	case TargetKey, TargetSecret, TargetPassthrough:
		p := strings.TrimSuffix(t.Path, ".*")
		if isConcretePath(p) {
			return config.Path(p)
		}
	case TargetEntry, TargetFold:
		if isConcretePath(t.List) {
			return config.Path(t.List)
		}
	}
	return ""
}

// CanonicalEnv derives the canonical env name of a path (05-config §1.3.6):
// ABO_<PATH> upper-cased with "." and "-" as "_", the leading MODULES_
// dropped (modules.books.scan.on_startup -> ABO_BOOKS_SCAN_ON_STARTUP).
func CanonicalEnv(p config.Path) string {
	s := strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(string(p)))
	s = strings.TrimPrefix(s, "MODULES_")
	return "ABO_" + s
}

// ---- retired names ----

// attachRetired derives each row's retired names and verdicts.
func (g *generator) attachRetired() {
	// Bindings by row.
	bound := map[string][]string{}
	for _, b := range headBindings {
		row := g.bindingRow(b.Field)
		if row == "" {
			g.fail("BindEnv key %s names no row of the table", b.Field)
			continue
		}
		bound[row] = append(bound[row], b.Env...)
	}
	eco := map[string]bool{}
	for _, n := range ecosystemEnvNames {
		eco[n] = true
	}
	type nameKey struct {
		name string
		file bool
	}
	seen := map[nameKey]string{}
	push := func(r *Row, e RetiredEntry) {
		k := nameKey{e.Name, e.File}
		if prev, dup := seen[k]; dup {
			if prev != r.Head {
				g.fail("retired name %s (file=%v) belongs to rows %s and %s", e.Name, e.File, prev, r.Head)
			}
			return
		}
		seen[k] = r.Head
		r.Retired = append(r.Retired, e)
	}
	for i := range g.rows {
		r := &g.rows[i]
		if strings.ContainsAny(r.Head, "[]<>") {
			continue // a virtual row: its container row carries the names
		}
		canon := r.canonical()
		msg := ""
		if strings.HasPrefix(r.Note, "removed: ") {
			msg = strings.TrimPrefix(r.Note, "removed: ")
		}
		refuse := r.refuses(g.rows)
		verdict := VerdictWarn
		if refuse {
			verdict = VerdictRefuse
		}
		rf := r.passthroughRefuse()
		if (r.Top || r.Removed) && !strings.Contains(r.Head, ".") {
			push(r, RetiredEntry{Name: r.Head, File: true, Canonical: canon, Verdict: verdict, Refuse: rf, Message: msg})
		}
		envs := []string{strings.ToUpper(r.Head)} // AutomaticEnv: the upper-cased key (an ABK_ name is its own)
		envs = append(envs, bound[r.Head]...)
		for _, env := range envs {
			if eco[env] {
				if !slices.ContainsFunc(r.Retired, func(e RetiredEntry) bool { return e.Name == env && !e.File }) {
					r.Retired = append(r.Retired, RetiredEntry{Name: env, Verdict: VerdictKept, Canonical: canon})
				}
				continue
			}
			push(r, RetiredEntry{Name: env, Canonical: canon, Verdict: verdict, Refuse: rf, Message: msg})
		}
	}
}

// bindingRow resolves a BindEnv key to the row it feeds: the row of that
// name, the row an alias names, or the longest row it sits under (a scheduled
// entry's, or a dropped container's).
func (g *generator) bindingRow(field string) string {
	if g.has(field) {
		return field
	}
	if a, ok := bindAlias[field]; ok {
		return a
	}
	best := ""
	for h := range g.idx {
		if strings.HasPrefix(field, h+".") && len(h) > len(best) {
			best = h
		}
	}
	return best
}

// canonical is the Canonical path of a row's retired names.
func (r *Row) canonical() config.Path {
	for _, t := range r.Targets {
		if p := t.ConfigPath(); p != "" {
			return p
		}
	}
	return ""
}

// passthroughRefuse reports whether a row's retired name carries Refuse: a
// secret or a destination under an integrations passthrough path, whose class
// config cannot see.
func (r *Row) passthroughRefuse() bool {
	for _, t := range r.Targets {
		if t.Kind == TargetPassthrough && t.Dest || t.Kind == TargetSecret && t.Passthrough {
			return true
		}
	}
	return false
}

// ownRefuses is the row's own refuse condition (§1.3.6): a secret, a
// ProtectAdmin-or-higher key, a passthrough secret or destination.
func (r *Row) ownRefuses() bool {
	return r.Class == ClassSecret || r.Admin || r.passthroughRefuse()
}

// refuses is the row's verdict, as config will judge it from the key the name
// became (retired.go retiredJudge): a name with no canonical key only warns;
// otherwise the key's own class decides, and a container's file key also
// refuses when a key below it, under the same canonical path, does.
func (r *Row) refuses(all []Row) bool {
	canon := r.canonical()
	if canon == "" && !r.passthroughRefuse() {
		return false
	}
	if r.ownRefuses() {
		return true
	}
	for i := range all {
		c := &all[i]
		if c == r || !isBelow(c.Head, r.Head) || !c.ownRefuses() {
			continue
		}
		if cc := c.canonical(); cc == canon || strings.HasPrefix(string(cc), string(canon)+".") {
			return true
		}
	}
	return false
}

// isBelow reports whether child is a sub-key row of top.
func isBelow(child, top string) bool {
	return len(child) > len(top) && strings.HasPrefix(child, top) && (child[len(top)] == '.' || child[len(top)] == '[')
}

// ---- outputs ----

// RetiredConfigNames is the list app.Options.RetiredConfigNames takes
// (F3-07 wires it): every retired env name and flat file key of the table,
// ecosystem bindings excluded. Refuse is set where the app states the verdict
// for a passthrough path (a secret or a destination); config decides every
// other one from the key's own class.
func RetiredConfigNames() []config.RetiredName {
	var out []config.RetiredName
	for _, r := range Table().Rows {
		for _, e := range r.Retired {
			if e.Verdict == VerdictKept {
				continue
			}
			out = append(out, config.RetiredName{Name: e.Name, File: e.File, Canonical: e.Canonical, Refuse: e.Refuse, Message: e.Message})
		}
	}
	return out
}

// CanonicalEnvNames is every canonical env name the deploy files may set
// (F3-07's TestDeployFilesUseCanonicalNamesOnly): the canonical name of every
// concrete key, secret and passthrough target, the two process names' new
// names, and the ecosystem bindings. Sorted, without duplicates.
func CanonicalEnvNames() []string {
	set := map[string]bool{}
	for _, n := range ecosystemEnvNames {
		set[n] = true
	}
	for _, r := range Table().Rows {
		if r.Class == ClassDropped {
			continue
		}
		for _, t := range r.Targets {
			if p := t.ConfigPath(); p != "" {
				set[CanonicalEnv(p)] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
