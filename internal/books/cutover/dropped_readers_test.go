// file: internal/books/cutover/dropped_readers_test.go
// version: 1.0.0
// guid: 7e3a9c14-2b85-4f60-9d1a-6c0b48e7f325
// last-edited: 2026-11-20

package cutover

import (
	"fmt"
	"go/token"
	"go/types"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const configPkg = "github.com/falkcorp/audiobook-organizer/internal/config"

// droppedWithValue are the ClassDropped HEAD fields a flipped Config still gives
// a value, so the readers that remain read something real. Every other dropped
// field is zero after the flip and nothing outside internal/config may read it
// (TestNoReaderOfADroppedField). A flipped Config holding a field at zero while a
// live reader branches on it is the shape that made scheduled.<task>.on_startup
// and library_scan_full.interval silent behaviour changes.
//
// The reasons are claims, each pinned where it holds: base is
// internal/books/config_wire.go's appConfigBase (TestAppConfigBaseValuesSurvive in
// internal/books), projected is a view of ai_endpoints (appconfig kindAI).
var droppedWithValue = map[string]string{
	"database_type":                    "base: appConfigBase sets \"pebble\"",
	"auto_update.window_start":         "base: appConfigBase sets 2",
	"auto_update.window_end":           "base: appConfigBase sets 5",
	"ai_backend.local_embedding_model": "base: appConfigBase sets the model",
	"ai_backend.local_llm_model":       "base: appConfigBase sets the model",
	"ai_backend.embedding_mode":        "projected: a view over ai_endpoints (appconfig kindAI)",
	"ai_backend.llm_mode":              "projected: a view over ai_endpoints (appconfig kindAI)",
	"ai_backend.local_base_url":        "projected: a view over ai_endpoints (appconfig kindAI)",
	"ai_endpoints_routing":             "OPEN: HEAD default false is read by 5 callers; 05-ai.md:803 says one path (routed). Decision pending",
}

// dropFields resolves a dotted HEAD name against the type of config.Config by
// JSON tag and returns the field it names, or nil when it names none.
func dropField(cfg *types.Named, head string) *types.Var {
	t := types.Type(cfg)
	var fv *types.Var
	for _, seg := range strings.Split(head, ".") {
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			return nil
		}
		fv = nil
		for i := 0; i < st.NumFields(); i++ {
			if strings.Split(reflectTag(st.Tag(i), "json"), ",")[0] == seg {
				fv = st.Field(i)
				break
			}
		}
		if fv == nil {
			return nil
		}
		t = fv.Type()
	}
	return fv
}

// reflectTag is reflect.StructTag.Get without importing reflect for one call.
func reflectTag(tag, key string) string {
	return reflect.StructTag(tag).Get(key)
}

// TestNoReaderOfADroppedField: no non-test code outside internal/config reads a
// HEAD field the flip drops (class x in the rename table), unless the flipped
// Config still gives it a value (droppedWithValue). It is type-checked, so a read
// through a local copy, a pointer or an alias counts, and a field of another
// struct with the same name does not.
func TestNoReaderOfADroppedField(t *testing.T) {
	if testing.Short() {
		t.Skip("loads and type-checks the module")
	}
	root := repoRoot(t)
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir:  root,
	}, "./internal/...", "./cmd/...")
	if err != nil {
		t.Fatal(err)
	}
	var cfgPkg *packages.Package
	for _, p := range pkgs {
		if p.PkgPath == configPkg {
			cfgPkg = p
		}
		for _, e := range p.Errors {
			t.Fatalf("loading %s: %v", p.PkgPath, e)
		}
	}
	if cfgPkg == nil {
		t.Fatal("internal/config was not loaded")
	}
	cfgNamed, ok := cfgPkg.Types.Scope().Lookup("Config").Type().(*types.Named)
	if !ok {
		t.Fatal("config.Config is not a named type")
	}

	// Positions identify a field across packages: a package that imports
	// internal/config sees its fields from export data with the same file and line.
	posKey := func(fset *token.FileSet, v *types.Var) string {
		pos := fset.Position(v.Pos())
		return fmt.Sprintf("%s:%d", pos.Filename[strings.LastIndex(pos.Filename, "/")+1:], pos.Line)
	}
	dropped := map[string]string{} // field position -> a HEAD name that drops it
	for _, r := range Table().Rows {
		if r.Class != ClassDropped || r.Removed || droppedWithValue[r.Head] != "" {
			continue
		}
		if fv := dropField(cfgNamed, r.Head); fv != nil {
			dropped[posKey(cfgPkg.Fset, fv)] = r.Head
		}
	}
	if len(dropped) < 2 {
		t.Fatalf("only %d dropped fields resolved against config.Config: the resolver or the table changed", len(dropped))
	}
	for h := range droppedWithValue {
		if !slices.ContainsFunc(Table().Rows, func(r Row) bool { return r.Head == h && r.Class == ClassDropped }) {
			t.Errorf("droppedWithValue names %s, which is not a dropped row of the table", h)
		}
	}

	var hits []string
	for _, p := range pkgs {
		if p.PkgPath == configPkg || strings.HasSuffix(p.PkgPath, "/internal/books/cutover") ||
			strings.Contains(p.PkgPath, "/internal/books/cutover/") {
			continue
		}
		for id, obj := range p.TypesInfo.Uses {
			v, ok := obj.(*types.Var)
			if !ok || !v.IsField() || v.Pkg() == nil || v.Pkg().Path() != configPkg {
				continue
			}
			pos := p.Fset.Position(id.Pos())
			if strings.HasSuffix(pos.Filename, "_test.go") {
				continue
			}
			if head, ok := dropped[posKey(p.Fset, v)]; ok {
				hits = append(hits, fmt.Sprintf("%s:%d reads %s (%s)", pos.Filename[len(root)+1:], pos.Line, head, v.Name()))
			}
		}
	}
	sort.Strings(hits)
	if len(hits) > 0 {
		t.Errorf("code outside internal/config reads a HEAD field the flip drops (the flipped Config holds it at zero):\n  %s",
			strings.Join(hits, "\n  "))
	}
}
