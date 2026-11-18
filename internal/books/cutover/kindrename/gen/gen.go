// file: internal/books/cutover/kindrename/gen/gen.go
// version: 1.0.0
// guid: a3f60d12-8b7e-45c9-b0d4-6e21c9f5a873
// last-edited: 2026-11-18

// Package gen builds the kind-rename map (05-tasks §5T.8.2) from its inputs
// and renders map_gen.go. kindrename's TestKindRenameMap_UpToDate calls
// Generate and compares the bytes with the checked-in file; ./cmd is the
// program `go generate` runs.
//
// Inputs, read at the generator's commit:
//
//   - internal/server/testdata/op_ids.golden: every canonical ID and
//     FormerIDs alias HEAD ever registered;
//   - kindrename/testdata/head_registry.golden: the booted registry's
//     canonical IDs and alias table (written and checked by
//     internal/server's TestKindRenameInputs_RegistryDump);
//   - internal/opscatalog/tombstones.go: the Tombstones keys;
//   - internal/scheduler/maintenance.go: the taskV2DefIDs pairs.
//
// The two Go files are parsed, not imported: the generator is a leaf program,
// and importing opscatalog or scheduler would pull the registry and the
// store into a tool that must run before either builds.
package gen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/mediacore/tasks"
)

// The generated file's version header. The header is part of the bytes the
// up-to-date test compares, so a change to the map changes these by hand
// (bump HeaderVersion and HeaderEdited with it).
const (
	HeaderVersion = "1.0.0"
	HeaderGUID    = "e29b4f7a-30c1-4d68-9a15-c8d7f0a2b146"
	HeaderEdited  = "2026-11-18"
)

// Relative paths of the inputs and the output, from the repository root.
const (
	LedgerPath     = "internal/server/testdata/op_ids.golden"
	RegistryPath   = "internal/books/cutover/kindrename/testdata/head_registry.golden"
	TombstonesPath = "internal/opscatalog/tombstones.go"
	SchedulerPath  = "internal/scheduler/maintenance.go"
	OutputPath     = "internal/books/cutover/kindrename/map_gen.go"
)

// Inputs is everything the map is built from.
type Inputs struct {
	Ledger     []string          // every ID in op_ids.golden
	Registered []string          // the registry's canonical IDs
	Aliases    map[string]string // FormerIDs alias -> canonical ID
	Tombstones []string          // opscatalog.Tombstones keys
	TaskDefIDs map[string]string // scheduler task name -> HEAD def ID (taskV2DefIDs)
}

// Tables is the generated map.
type Tables struct {
	Defs      map[string]kindrename.Target
	Scheduler map[string]kindrename.SchedulerTarget
}

// FindRoot walks up from dir to the repository root (go.mod beside
// internal/books).
func FindRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "internal", "books")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("kindrename/gen: no repository root above %s", dir)
		}
		dir = parent
	}
}

// ReadInputs reads the inputs under the repository root.
func ReadInputs(root string) (Inputs, error) {
	var in Inputs
	ledger, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(LedgerPath)))
	if err != nil {
		return in, err
	}
	in.Ledger = parseLines(string(ledger))
	reg, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(RegistryPath)))
	if err != nil {
		return in, err
	}
	in.Aliases = map[string]string{}
	for _, l := range parseLines(string(reg)) {
		f := strings.Fields(l)
		switch {
		case len(f) == 2 && f[0] == "id":
			in.Registered = append(in.Registered, f[1])
		case len(f) == 3 && f[0] == "alias":
			in.Aliases[f[1]] = f[2]
		default:
			return in, fmt.Errorf("%s: malformed line %q", RegistryPath, l)
		}
	}
	tomb, err := mapLiteral(filepath.Join(root, filepath.FromSlash(TombstonesPath)), "Tombstones")
	if err != nil {
		return in, err
	}
	for k := range tomb {
		in.Tombstones = append(in.Tombstones, k)
	}
	if in.TaskDefIDs, err = mapLiteral(filepath.Join(root, filepath.FromSlash(SchedulerPath)), "taskV2DefIDs"); err != nil {
		return in, err
	}
	return in, nil
}

// parseLines is a golden's lines without "#" comments and blanks.
func parseLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// mapLiteral parses path and returns the string pairs of the package-level
// map[string]string composite literal named name.
func mapLiteral(path, name string) (map[string]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name != name || i >= len(vs.Values) {
					continue
				}
				cl, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("%s: %s is not a composite literal", path, name)
				}
				out := map[string]string{}
				for _, el := range cl.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						return nil, fmt.Errorf("%s: %s has a non key-value element", path, name)
					}
					k, kerr := stringLit(kv.Key)
					v, verr := stringLit(kv.Value)
					if kerr != nil || verr != nil {
						return nil, fmt.Errorf("%s: %s holds a non-literal pair; the generator reads string literals only", path, name)
					}
					out[k] = v
				}
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("%s: no package-level var %s", path, name)
}

func stringLit(e ast.Expr) (string, error) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", fmt.Errorf("not a string literal")
	}
	return strconv.Unquote(bl.Value)
}

// Build applies rules 1-6 to every ID the inputs name and derives the
// scheduler table. It refuses an input set the rules cannot classify, so a
// ledger line nobody decided about stops the generator.
func Build(in Inputs) (Tables, error) {
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	registered := toSet(in.Registered)
	tomb := toSet(in.Tombstones)
	core := kindrename.CoreKinds()
	flip := toSet(kindrename.FlipRetired())

	for old, canon := range in.Aliases {
		if registered[old] {
			bad("alias %q shadows a registered ID", old)
		}
		if !registered[canon] {
			bad("alias %q points at %q, which is not registered", old, canon)
		}
	}
	for id := range tomb {
		if registered[id] || in.Aliases[id] != "" {
			bad("tombstoned ID %q is registered or an alias", id)
		}
	}
	for id := range core {
		if !registered[id] {
			bad("T4 table names %q, which is not a registered def ID", id)
		}
	}
	for id := range flip {
		if !registered[id] {
			bad("FlipRetired names %q, which is not a registered def ID", id)
		}
	}

	// canonical resolves a registered ID by rules 1, 2 and 4 (and the flip's
	// explicit rule-5 IDs), the answer an alias of it takes (rule 3).
	canonical := func(id string) (kindrename.Target, error) {
		switch {
		case core[id] != "":
			return kindrename.Target{Kind: core[id], Rule: kindrename.RuleCore}, nil
		case id == kindrename.RepairsPlanID:
			return kindrename.Target{Rule: kindrename.RuleRepairs, Marker: kindrename.MarkerRepairsPlan}, nil
		case id == kindrename.RepairsApplyID:
			return kindrename.Target{Rule: kindrename.RuleRepairs, Marker: kindrename.MarkerRepairsApply}, nil
		case flip[id]:
			k, err := kindrename.RetiredKind(id)
			return kindrename.Target{Kind: k, Rule: kindrename.RuleRetired}, err
		case id == kindrename.LibraryScanID:
			return kindrename.Target{Kind: kindrename.ScanKind, Rule: kindrename.RuleBooks, Marker: kindrename.MarkerLibraryScan}, nil
		default:
			k, err := tasks.ParseKind("books." + id)
			return kindrename.Target{Kind: k, Rule: kindrename.RuleBooks}, err
		}
	}

	ids := map[string]bool{}
	for _, id := range in.Ledger {
		ids[id] = true
	}
	for id := range registered {
		ids[id] = true
	}
	for id := range in.Aliases {
		ids[id] = true
	}
	for id := range tomb {
		ids[id] = true
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)

	defs := map[string]kindrename.Target{}
	for _, id := range sorted {
		var (
			t   kindrename.Target
			err error
		)
		switch {
		case registered[id]:
			t, err = canonical(id)
		case in.Aliases[id] != "":
			if t, err = canonical(in.Aliases[id]); err == nil {
				t.Rule = kindrename.RuleAlias
			}
		case tomb[id]:
			var k tasks.Kind
			k, err = kindrename.RetiredKind(id)
			t = kindrename.Target{Kind: k, Rule: kindrename.RuleRetired}
		default:
			bad("ledger ID %q is neither registered, an alias nor tombstoned; decide it (a FormerIDs alias or an opscatalog.Tombstones entry) before the map can be generated", id)
			continue
		}
		if err != nil {
			bad("%q: %v", id, err)
			continue
		}
		defs[id] = t
	}

	sched := map[string]kindrename.SchedulerTarget{}
	names := make([]string, 0, len(in.TaskDefIDs))
	for n := range in.TaskDefIDs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, task := range names {
		def := in.TaskDefIDs[task]
		if task == kindrename.BatchPollerTask || task == kindrename.TranscodeTask {
			bad("taskV2DefIDs names %q, which the spec says fires no def", task)
			continue
		}
		t, ok := defs[def]
		switch {
		case !ok:
			bad("scheduler task %q fires %q, which is not a known def ID", task, def)
		case t.Rule == kindrename.RuleRetired:
			bad("scheduler task %q fires %q, which maps to the retired kind %q", task, def, t.Kind)
		case t.Marker == kindrename.MarkerRepairsPlan || t.Marker == kindrename.MarkerRepairsApply:
			bad("scheduler task %q fires %q, whose kind depends on a fixer_id", task, def)
		case task == kindrename.FullSweepTask:
			sched[task] = kindrename.SchedulerTarget{Kind: kindrename.FullSweepKind}
		default:
			sched[task] = kindrename.SchedulerTarget{Kind: t.Kind}
		}
	}
	if _, ok := in.TaskDefIDs[kindrename.FullSweepTask]; !ok {
		bad("taskV2DefIDs no longer names %q; the full-sweep exception has nothing to apply to", kindrename.FullSweepTask)
	}
	sched[kindrename.BatchPollerTask] = kindrename.SchedulerTarget{Kind: kindrename.BatchPollKind}
	sched[kindrename.TranscodeTask] = kindrename.SchedulerTarget{Dropped: true}

	if len(problems) > 0 {
		slices.Sort(problems)
		return Tables{}, fmt.Errorf("kindrename/gen: %d problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
	return Tables{Defs: defs, Scheduler: sched}, nil
}

func toSet(s []string) map[string]bool {
	out := make(map[string]bool, len(s))
	for _, v := range s {
		out[v] = true
	}
	return out
}

// Render is map_gen.go's source for t.
func Render(t Tables) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "// file: internal/books/cutover/kindrename/map_gen.go\n// version: %s\n// guid: %s\n// last-edited: %s\n\n", HeaderVersion, HeaderGUID, HeaderEdited)
	b.WriteString("// Code generated by internal/books/cutover/kindrename/gen; DO NOT EDIT.\n// Regenerate: go generate ./internal/books/cutover/kindrename/\n\n")
	b.WriteString("package kindrename\n\n")
	b.WriteString("// defTargets is the kind-rename map over HEAD's def IDs (05-tasks §5T.8.2):\n// every ID in the op-ID ledger and the booted registry, by rules 1-5.\n")
	b.WriteString("var defTargets = map[string]Target{\n")
	ids := make([]string, 0, len(t.Defs))
	for id := range t.Defs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		d := t.Defs[id]
		fmt.Fprintf(&b, "\t%q: {", id)
		var parts []string
		if d.Kind != "" {
			parts = append(parts, fmt.Sprintf("Kind: %q", string(d.Kind)))
		}
		parts = append(parts, "Rule: "+ruleName(d.Rule))
		if d.Marker != kindrename.MarkerNone {
			parts = append(parts, "Marker: "+markerName(d.Marker))
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("},\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("// schedulerTargets maps a scheduler task name to the kind whose clock it is\n// (taskV2DefIDs through rules 1 and 4, with the three named exceptions).\n")
	b.WriteString("var schedulerTargets = map[string]SchedulerTarget{\n")
	names := make([]string, 0, len(t.Scheduler))
	for n := range t.Scheduler {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := t.Scheduler[n]
		if s.Dropped {
			fmt.Fprintf(&b, "\t%q: {Dropped: true},\n", n)
		} else {
			fmt.Fprintf(&b, "\t%q: {Kind: %q},\n", n, string(s.Kind))
		}
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

func ruleName(r kindrename.Rule) string {
	switch r {
	case kindrename.RuleCore:
		return "RuleCore"
	case kindrename.RuleRepairs:
		return "RuleRepairs"
	case kindrename.RuleAlias:
		return "RuleAlias"
	case kindrename.RuleBooks:
		return "RuleBooks"
	case kindrename.RuleRetired:
		return "RuleRetired"
	}
	return fmt.Sprintf("Rule(%d)", uint8(r))
}

func markerName(m kindrename.Marker) string {
	switch m {
	case kindrename.MarkerRepairsPlan:
		return "MarkerRepairsPlan"
	case kindrename.MarkerRepairsApply:
		return "MarkerRepairsApply"
	case kindrename.MarkerLibraryScan:
		return "MarkerLibraryScan"
	}
	return fmt.Sprintf("Marker(%d)", uint8(m))
}

// Generate reads the inputs under root and returns map_gen.go's bytes.
func Generate(root string) ([]byte, error) {
	in, err := ReadInputs(root)
	if err != nil {
		return nil, err
	}
	t, err := Build(in)
	if err != nil {
		return nil, err
	}
	return Render(t)
}
