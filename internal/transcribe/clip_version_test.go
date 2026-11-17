// file: internal/transcribe/clip_version_test.go
// version: 3.0.0
// guid: 9a3d6e18-5b27-4c40-a1f9-2e8b7c0d4a65
// last-edited: 2026-11-17

package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// What forces a ClipParserVersion bump, and what does not.
//
// A stored IntroParse carries the version that produced it. The re-parse op uses
// it to leave a parse from a NEWER grammar alone (a rollback must not overwrite
// newer results with older ones) and to tell a current parse from an old one. That
// only works if the version moves whenever a STORED field of ParseClip can change.
//
// Two fingerprints guard it (clipGrammarFingerprints):
//
//	Outputs: a hash of the STORED fields of ParseClip over a frozen input list
//	         (testdata/clip_version_inputs.json, separate from the corpus and the
//	         golden, so adding a corpus row or a table case never asks for a bump).
//	Sources: a hash of the SOURCE of every top-level declaration ParseClip can reach,
//	         in the clip grammar files and in the legacy parser files it reads
//	         (clipGrammarSource): every regular expression, table, threshold, keyword
//	         list, local or package level, and the logic around them, with comments,
//	         whitespace and formatting taken out (a token-stream hash, so a comment or a
//	         gofmt change does not move it).
//
// The rule, enforced by TestClipParserVersionIsBumped through clipFingerprintVerdict:
//
//	outputs changed on the frozen inputs        -> ClipParserVersion MUST be bumped
//	                                               (add a row for the new version);
//	sources changed, outputs identical          -> the test prints the new sources hash;
//	                                               record it in the version's
//	                                               EquivalentSources, reviewer-visible, with
//	                                               NO bump, once you have confirmed the change
//	                                               cannot alter a stored field for any input
//	                                               (a refactor, a renamed helper). If it can,
//	                                               bump instead: the frozen inputs are a
//	                                               sample, and this is the one place a
//	                                               behaviour change they do not exercise is
//	                                               caught, by the author saying so in review.
//
// Does NOT force anything: the unstored Reason token, the parser's unexported
// plumbing, comments and formatting, and anything the legacy parser alone produces
// (declarations ParseClip cannot reach are outside the Sources slice).
//
// Every bump makes the next live re-parse rewrite every stored parse, so a bump is a
// real cost; this test exists so it is never skipped when it is due and is never
// demanded when it is not.

// clipStoredFields is the part of a ClipParse that reaches storage (maintenance's
// introParseFromClip copies exactly these). Reason is the one field of ClipParse
// that is not stored.
type clipStoredFields struct {
	Kind             ClipKind
	ChapterNumber    int
	HasChapterNumber bool
	ChapterTitle     string
	PartNumber       int
	HasPartNumber    bool
	Marker           string
	Title            string
	Author           string
	Narrator         string
	Translator       string
	CoverArtist      string
	Publisher        string
	CollectionFrom   int
	CollectionTo     int
}

func storedClipFields(p ClipParse) clipStoredFields {
	return clipStoredFields{
		Kind: p.Kind, ChapterNumber: p.ChapterNumber, HasChapterNumber: p.HasChapterNumber,
		ChapterTitle: p.ChapterTitle, PartNumber: p.PartNumber, HasPartNumber: p.HasPartNumber,
		Marker: p.Marker, Title: p.Title, Author: p.Author, Narrator: p.Narrator,
		Translator: p.Translator, CoverArtist: p.CoverArtist, Publisher: p.Publisher,
		CollectionFrom: p.CollectionFrom, CollectionTo: p.CollectionTo,
	}
}

func clipVersionInputs(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/clip_version_inputs.json")
	if err != nil {
		t.Fatalf("read frozen inputs: %v", err)
	}
	var in []string
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("frozen inputs: %v", err)
	}
	return in
}

// clipGrammarRoot is the function whose reachable declarations are hashed, and
// clipGrammarExcluded the declarations deliberately outside the hash: the version
// constant (a bump would otherwise change the hash that demands the bump).
const clipGrammarRoot = "ParseClip"

var clipGrammarExcluded = map[string]string{
	"ClipParserVersion": "the version itself",
}

// clipUnit is one top-level declaration: a function, a method, a type, a var spec, or
// a const group (kept whole, because a const group's later specs can depend on the
// earlier ones through iota; a var group's specs are independent units).
type clipUnit struct {
	name string // "ParseClip", "(Recv).Method", "a,b" for a spec naming several
	file string
	node ast.Node
	// start and end are the unit's byte range in its file.
	start, end int
	src        string // the unit's source text
	// litEnd is the file offset just past the unit's first string or number literal
	// (litString: it is a string), 0 when it has none; tests use it to edit a unit.
	litEnd    int
	litString bool
}

// readPackageSources returns every non-test .go file of this package by file name.
func readPackageSources(t *testing.T) map[string]string {
	t.Helper()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		out[m] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("no package sources found: the test must run in the package directory")
	}
	return out
}

// clipTokens is the unit's token stream with comments, whitespace and layout
// removed: identifiers and literals keep their exact text, and every semicolon (the
// scanner inserts one at a line end) is the same token.
func clipTokens(src string) string {
	fset := token.NewFileSet()
	file := fset.AddFile("unit.go", fset.Base(), len(src))
	var sc scanner.Scanner
	sc.Init(file, []byte(src), nil, 0) // mode 0: comments are skipped
	var b strings.Builder
	for {
		_, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		switch {
		case tok == token.SEMICOLON:
			b.WriteString(";")
		case lit != "":
			b.WriteString(tok.String() + "\x00" + lit)
		default:
			b.WriteString(tok.String())
		}
		b.WriteByte(' ')
	}
	return b.String()
}

// clipIndex parses src (file name -> text) and indexes the top-level units by name;
// methods are indexed apart, by method name.
func clipIndex(src map[string]string) (units []*clipUnit, top, methods map[string][]*clipUnit, err error) {
	fset := token.NewFileSet()
	top, methods = map[string][]*clipUnit{}, map[string][]*clipUnit{}
	files := make([]string, 0, len(src))
	for f := range src {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, fn := range files {
		af, perr := parser.ParseFile(fset, fn, src[fn], 0) // no comments in the tree
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("parse %s: %w", fn, perr)
		}
		mk := func(n ast.Node, name string) *clipUnit {
			st, en := fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset
			u := &clipUnit{name: name, file: fn, node: n, start: st, end: en, src: src[fn][st:en]}
			ast.Inspect(n, func(x ast.Node) bool {
				if b, ok := x.(*ast.BasicLit); ok && u.litEnd == 0 && (b.Kind == token.STRING || b.Kind == token.INT || b.Kind == token.FLOAT) {
					u.litEnd, u.litString = fset.Position(b.End()).Offset, b.Kind == token.STRING
				}
				return u.litEnd == 0
			})
			units = append(units, u)
			return u
		}
		for _, d := range af.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil && len(d.Recv.List) == 1 {
					u := mk(d, "("+recvName(d.Recv.List[0].Type)+")."+d.Name.Name)
					methods[d.Name.Name] = append(methods[d.Name.Name], u)
				} else {
					u := mk(d, d.Name.Name)
					top[d.Name.Name] = append(top[d.Name.Name], u)
				}
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue
				}
				specNames := func(sp ast.Spec) []string {
					var ns []string
					switch sp := sp.(type) {
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							ns = append(ns, n.Name)
						}
					case *ast.TypeSpec:
						ns = append(ns, sp.Name.Name)
					}
					return ns
				}
				if d.Tok == token.CONST {
					var all []string
					for _, sp := range d.Specs {
						all = append(all, specNames(sp)...)
					}
					u := mk(d, strings.Join(all, ","))
					for _, n := range all {
						top[n] = append(top[n], u)
					}
					continue
				}
				for _, sp := range d.Specs {
					ns := specNames(sp)
					u := mk(sp, strings.Join(ns, ","))
					for _, n := range ns {
						top[n] = append(top[n], u)
					}
				}
			}
		}
	}
	return units, top, methods, nil
}

// clipGrammarSource returns the hash of every top-level declaration reachable from
// ParseClip in src (file name -> text), with the sorted unit names. A name that
// appears anywhere in a reached unit reaches every top-level declaration of that name,
// and a selector reaches every method of that name: an over-approximation, so a new
// dependency can only add to the slice, never fall out of it.
func clipGrammarSource(src map[string]string) (hash string, names []string, err error) {
	_, top, methods, err := clipIndex(src)
	if err != nil {
		return "", nil, err
	}
	if len(top[clipGrammarRoot]) != 1 {
		return "", nil, fmt.Errorf("%s is declared %d times in the package sources, want exactly once", clipGrammarRoot, len(top[clipGrammarRoot]))
	}
	reached := map[*clipUnit]bool{}
	queue := []*clipUnit{top[clipGrammarRoot][0]}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		if reached[u] {
			continue
		}
		reached[u] = true
		ast.Inspect(u.node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if _, skip := clipGrammarExcluded[n.Name]; !skip {
					queue = append(queue, top[n.Name]...)
				}
			case *ast.SelectorExpr:
				queue = append(queue, methods[n.Sel.Name]...)
			}
			return true
		})
	}
	sel := make([]*clipUnit, 0, len(reached))
	for u := range reached {
		sel = append(sel, u)
	}
	sort.Slice(sel, func(i, j int) bool {
		if sel[i].name != sel[j].name {
			return sel[i].name < sel[j].name
		}
		return clipTokens(sel[i].src) < clipTokens(sel[j].src)
	})
	h := sha256.New()
	for _, u := range sel {
		names = append(names, u.name)
		fmt.Fprintf(h, "%s\n%s\n", u.name, clipTokens(u.src))
	}
	return hex.EncodeToString(h.Sum(nil)), names, nil
}

// recvName renders a receiver type expression ("T", "*T") for a unit name.
func recvName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + recvName(e.X)
	case *ast.IndexExpr:
		return recvName(e.X)
	}
	return "?"
}

// clipFingerprint is the fingerprints of the clip grammar as it stood when a version
// was cut. EquivalentSources are later source hashes recorded, with the version's
// outputs unchanged, as behaviour-preserving edits.
type clipFingerprint struct {
	Outputs, Sources  string
	EquivalentSources []string
}

// clipGrammarFingerprints holds one row per ClipParserVersion. A row's Outputs and
// Sources are never edited once the version has shipped (clipV1Pin pins version 1's in
// a second place); a grammar change adds a row for a new version, and a refactor adds
// to EquivalentSources.
//
// Version 1 had not shipped when the sources fingerprint moved from a hand-listed set
// of tables to the reachable-source hash above, so its Sources value (and clipV1Pin)
// were re-recorded once, in the same change.
var clipGrammarFingerprints = map[int]clipFingerprint{
	1: {Outputs: "2b63486f82c21b6245162afaf675d98ee1fbb3ca082c3f546fd28805e311ba32", Sources: "ee56e95ee48e8bb96587558623cc99034c2d4f175babb9d05e9a1ecb6a9060d9"},
}

func clipGrammarOutputsHash(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, s := range clipVersionInputs(t) {
		if err := enc.Encode(storedClipFields(ParseClip(s))); err != nil {
			t.Fatal(err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// clipVerdict is what the current grammar's fingerprints say about the recorded row.
type clipVerdict int

const (
	// clipCurrent: nothing the version guards has changed.
	clipCurrent clipVerdict = iota
	// clipNeedsBump: stored outputs changed on the frozen inputs.
	clipNeedsBump
	// clipRecordEquivalent: the sources changed and no output did.
	clipRecordEquivalent
)

// clipFingerprintVerdict is the whole decision. Outputs decide first, so no
// equivalence record can excuse a changed output.
func clipFingerprintVerdict(rec clipFingerprint, gotOut, gotSrc string) clipVerdict {
	if gotOut != rec.Outputs {
		return clipNeedsBump
	}
	if gotSrc == rec.Sources || slices.Contains(rec.EquivalentSources, gotSrc) {
		return clipCurrent
	}
	return clipRecordEquivalent
}

// TestClipStoredProjectionCoversEveryField fails when ClipParse gains a field that
// the version hash would silently ignore.
func TestClipStoredProjectionCoversEveryField(t *testing.T) {
	stored := map[string]bool{}
	st := reflect.TypeOf(clipStoredFields{})
	for i := 0; i < st.NumField(); i++ {
		stored[st.Field(i).Name] = true
	}
	ct := reflect.TypeOf(ClipParse{})
	for i := 0; i < ct.NumField(); i++ {
		name := ct.Field(i).Name
		if name == "Reason" {
			continue // the one field that is not stored (see ClipParse)
		}
		if !stored[name] {
			t.Errorf("ClipParse.%s is not in clipStoredFields: if it is stored it belongs in the version hash; if not, say so here like Reason", name)
		}
	}
	for name := range stored {
		if _, ok := ct.FieldByName(name); !ok {
			t.Errorf("clipStoredFields.%s is not a ClipParse field", name)
		}
	}
}

// clipV1Pin is a second, separate record of version 1's row. Editing version 1's
// recorded fingerprints in place (to make a grammar change pass without a bump)
// now also needs this value changed, in a different place, with this comment in
// view. Version 1's row is final once it ships; EquivalentSources is not part of it.
const clipV1Pin = "66671b514d3e1b03882cf6f24c5668265e53c9c3bef02a84f3ead70246514457"

func clipRowDigest(r clipFingerprint) string {
	sum := sha256.Sum256([]byte(r.Outputs + "|" + r.Sources))
	return hex.EncodeToString(sum[:])
}

func TestClipParserVersionIsBumped(t *testing.T) {
	// The table is contiguous: one row per version from 1 to ClipParserVersion.
	if len(clipGrammarFingerprints) != ClipParserVersion {
		t.Fatalf("clipGrammarFingerprints has %d rows and ClipParserVersion is %d: there is exactly one row per version, 1..%d",
			len(clipGrammarFingerprints), ClipParserVersion, ClipParserVersion)
	}
	for v := 1; v <= ClipParserVersion; v++ {
		if _, ok := clipGrammarFingerprints[v]; !ok {
			t.Fatalf("clipGrammarFingerprints has no row for version %d", v)
		}
	}
	gotSrc, _, err := clipGrammarSource(readPackageSources(t))
	if err != nil {
		t.Fatal(err)
	}
	gotOut := clipGrammarOutputsHash(t)
	want := clipGrammarFingerprints[ClipParserVersion]
	if want.Outputs == "FILL" || want.Sources == "FILL" || clipV1Pin == "FILL" {
		t.Fatalf("no fingerprint recorded for ClipParserVersion %d; add:\n\t%d: {Outputs: %q, Sources: %q},\nand clipV1Pin = %q",
			ClipParserVersion, ClipParserVersion, gotOut, gotSrc, clipRowDigest(clipFingerprint{Outputs: gotOut, Sources: gotSrc}))
	}
	// Version 1 is final.
	if got := clipRowDigest(clipGrammarFingerprints[1]); got != clipV1Pin {
		t.Fatalf("version 1's recorded fingerprints were edited (digest %s, pinned %s): a shipped version's row is never changed; a grammar change is a new version with a new row", got, clipV1Pin)
	}

	switch clipFingerprintVerdict(want, gotOut, gotSrc) {
	case clipNeedsBump:
		t.Fatalf("the stored fields ParseClip produces changed over the frozen inputs without a ClipParserVersion bump (version %d):\n"+
			"  outputs %s (recorded %s)\n"+
			"bump ClipParserVersion in clip.go and add a row for the new version here (see the comment at the top of this file)",
			ClipParserVersion, gotOut, want.Outputs)
	case clipRecordEquivalent:
		t.Fatalf("the grammar's source changed (version %d) and no stored field changed over the frozen inputs:\n"+
			"  sources %s (recorded %s)\n"+
			"If this edit cannot change ParseClip's stored fields for ANY input (a refactor), add %q to version %d's EquivalentSources: no bump.\n"+
			"If it can, even for an input the frozen list does not hold, bump ClipParserVersion instead.",
			ClipParserVersion, gotSrc, want.Sources, gotSrc, ClipParserVersion)
	}

	// A bump must come with a real change: no later row may repeat an earlier one.
	for v := 2; v <= ClipParserVersion; v++ {
		for u := 1; u < v; u++ {
			a, b := clipGrammarFingerprints[u], clipGrammarFingerprints[v]
			if a.Outputs == b.Outputs && a.Sources == b.Sources {
				t.Errorf("versions %d and %d record identical fingerprints: a bump without a grammar change", u, v)
			}
		}
	}
}

// The verdict is a pure decision, tested on its own: the rule is the contract, and a
// mistake in it (an equivalence record that excuses a changed output, an unrecorded
// source edit that passes) would otherwise pass every test that runs the real grammar.
func TestClipFingerprintVerdict(t *testing.T) {
	rec := clipFingerprint{Outputs: "out1", Sources: "src1", EquivalentSources: []string{"src2"}}
	for _, c := range []struct {
		name     string
		out, src string
		want     clipVerdict
	}{
		{"nothing changed", "out1", "src1", clipCurrent},
		{"a recorded equivalent source", "out1", "src2", clipCurrent},
		{"an unrecorded source edit, same outputs", "out1", "src3", clipRecordEquivalent},
		{"outputs changed, sources as recorded", "out2", "src1", clipNeedsBump},
		{"outputs changed, sources a recorded equivalent: no record excuses an output change", "out2", "src2", clipNeedsBump},
		{"outputs and sources both changed", "out2", "src3", clipNeedsBump},
	} {
		if got := clipFingerprintVerdict(rec, c.out, c.src); got != c.want {
			t.Errorf("%s: verdict %v, want %v", c.name, got, c.want)
		}
	}
}

// literalEdit returns src with the first string or number literal inside the named
// unit changed, and false when the unit has none.
func literalEdit(t *testing.T, src map[string]string, unitName string) (map[string]string, bool) {
	t.Helper()
	units, _, _, err := clipIndex(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if u.name != unitName || u.litEnd == 0 {
			continue
		}
		text := src[u.file]
		out := maps2(src)
		if u.litString {
			out[u.file] = text[:u.litEnd-1] + "Q" + text[u.litEnd-1:] // before the closing quote
		} else {
			out[u.file] = text[:u.litEnd] + "1" + text[u.litEnd:] // after the last digit
		}
		return out, true
	}
	return nil, false
}

func maps2(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Every declaration the parser reaches changes the hash when a literal in it is edited:
// no regular expression, table, threshold or keyword list is outside it, in the clip
// files or in the legacy files ParseClip reads, package level or local to a function.
func TestClipGrammarSourceHashCoversEveryReachedDeclaration(t *testing.T) {
	src := readPackageSources(t)
	base, names, err := clipGrammarSource(src)
	if err != nil {
		t.Fatal(err)
	}
	edited := 0
	for _, n := range names {
		mod, ok := literalEdit(t, src, n)
		if !ok {
			continue // a unit with no literal has nothing to edit; its logic is covered below
		}
		edited++
		got, _, err := clipGrammarSource(mod)
		if err != nil {
			t.Fatalf("edit of %s does not parse: %v", n, err)
		}
		if got == base {
			t.Errorf("editing a literal in %s does not change the sources hash", n)
		}
	}
	if edited < 40 {
		t.Errorf("only %d reached declarations hold a literal; the slice is smaller than the grammar", edited)
	}
}

// The review's named gaps, each one a literal or keyword the first hash could not see.
func TestClipGrammarSourceHashSeesTheLegacyRulesAndLocalKeywordLists(t *testing.T) {
	src := readPackageSources(t)
	base, names, err := clipGrammarSource(src)
	if err != nil {
		t.Fatal(err)
	}
	reached := map[string]bool{}
	for _, n := range names {
		for _, part := range strings.Split(n, ",") {
			reached[part] = true
		}
	}
	for _, want := range []string{
		// legacy package-level regexes ParseClip reads
		"anyCreditVerbRe", "boilerplateOnlyRe", "junkOpenerRe", "proseMarkerRe", "proseVerbRe", "creditVerbByRe", "translatorByRe", "coverArtByRe",
		// legacy thresholds and helpers
		"minProseWords", "creditVerbWindow", "extractCredits", "truncateName", "splitOnFirstRe", "roleFromSpans", "titleHasSentenceBreak", "clean", "collapseWS", "splitLines",
		// functions whose keyword lists are local
		"markerClassOf", "publisherFromJingle", "scanSpelled", "stepSpelled",
	} {
		if !reached[want] {
			t.Errorf("%s is read by ParseClip and is not in the hashed slice", want)
		}
	}
	if reached["ClipParserVersion"] {
		t.Error("the version constant is in the hashed slice: bumping it would change the hash that demands the bump")
	}

	changed := func(file, from, to string) {
		t.Helper()
		if !strings.Contains(src[file], from) {
			t.Fatalf("%s no longer contains %q: update this test's anchor", file, from)
		}
		mod := maps2(src)
		mod[file] = strings.Replace(src[file], from, to, 1)
		got, _, err := clipGrammarSource(mod)
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Errorf("%s: %q -> %q does not change the sources hash", file, from, to)
		}
	}
	changed("marker.go", `case "chapter", "track", "episode":`, `case "chapter", "track", "episode", "scene":`)
	changed("clip.go", `strings.HasPrefix(low, "this is ")`, `strings.HasPrefix(low, "this was ")`)
	changed("numbers.go", `if lw == "hundred" {`, `if lw == "hundreds" {`)
	changed("classify.go", `\b(?:was|were|had|would|could|should|said|went|came)\b`, `\b(?:was|were|had|would|could|should|said|went)\b`)

	same := func(file, from, to string) {
		t.Helper()
		if !strings.Contains(src[file], from) {
			t.Fatalf("%s no longer contains %q: update this test's anchor", file, from)
		}
		mod := maps2(src)
		mod[file] = strings.Replace(src[file], from, to, 1)
		got, _, err := clipGrammarSource(mod)
		if err != nil {
			t.Fatal(err)
		}
		if got != base {
			t.Errorf("%s: %q -> %q changes the sources hash, and it is not a grammar edit", file, from, to)
		}
	}
	// A comment, layout, and an edit to a function ParseClip cannot reach.
	same("clip.go", "func ParseClip(text string) ClipParse {", "func ParseClip(text string) ClipParse {\n\t// a comment inside the body\n\n\n")
	same("clip.go", "func ParseClip(text string) ClipParse {", "func ParseClip(text string)   ClipParse   {")
	same("clip.go", "package transcribe", "package transcribe\n\n// trailing note\n")
	same("parse.go", "func ParseAudiobookIntro(text string) IntroFields {", "func ParseAudiobookIntro(text string) IntroFields {\n\t_ = \"not reachable from ParseClip\"\n")
}
