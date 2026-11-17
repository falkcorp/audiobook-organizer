// file: internal/transcribe/clip_version_test.go
// version: 2.0.1
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
	"go/token"
	"os"
	"reflect"
	"regexp"
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
// Forces a bump: an edit to any regular expression, table, threshold or keyword
// list of the clip grammar (every top-level var and const of clip.go,
// clip_classify.go, marker.go and numbers.go, see clipGrammarTables), or to the
// logic that reads them, that changes Kind, ChapterNumber, HasChapterNumber,
// ChapterTitle, PartNumber, HasPartNumber, Marker, Title, Author, Narrator,
// Translator, CoverArtist, Publisher, CollectionFrom or CollectionTo for any input.
//
// Does NOT force one: the test inputs (the hash runs over a frozen list in
// testdata/clip_version_inputs.json, separate from the corpus and the golden, so
// adding a corpus row or a table case never asks for a bump), the unstored Reason
// token, the parser's unexported plumbing (phase and marker-class tags), and
// anything the legacy parser alone produces.
//
// Every bump makes the next live re-parse rewrite every stored parse, so a bump
// is a real cost; this test exists so it is never skipped when it is due and is
// never demanded when it is not.

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

// clipGrammarFingerprints holds, per ClipParserVersion, two fingerprints of the
// clip grammar as it stood when that version was cut:
//
//	Outputs: a hash of the STORED fields of ParseClip over the frozen input list.
//	Sources: a hash of every regular expression, table and threshold the grammar
//	         declares, so an edit that no input exercises still shows.
//
// A row is never edited once its version has shipped: a grammar change adds a
// row for a new version. clipV1Pin pins version 1's row in a second place.
var clipGrammarFingerprints = map[int]struct{ Outputs, Sources string }{
	1: {Outputs: "2b63486f82c21b6245162afaf675d98ee1fbb3ca082c3f546fd28805e311ba32", Sources: "c88347fa1122eb440007c7fa0595f96bd902d8165a806283696959ccfc2b28e3"},
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

func tableRe(r *regexp.Regexp) string { return r.String() }

func tableInts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s:%d,", k, m[k])
	}
	return b.String()
}

func tableSet(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// clipGrammarTables is every top-level var and const of the four clip grammar
// files except clipGrammarNotData, by name. TestClipGrammarTablesAreComplete
// parses the files and fails when a declaration is in neither set, so a new table
// cannot be added without entering the version hash.
func clipGrammarTables() map[string]string {
	kinds := make([]string, len(AllClipKinds))
	for i, k := range AllClipKinds {
		kinds[i] = string(k)
	}
	return map[string]string{
		"ClipKindNone": string(ClipKindNone), "ClipKindTitle": string(ClipKindTitle), "ClipKindCredits": string(ClipKindCredits),
		"ClipKindChapter": string(ClipKindChapter), "ClipKindPart": string(ClipKindPart), "ClipKindMatter": string(ClipKindMatter),
		"ClipKindNarration": string(ClipKindNarration), "ClipKindSilence": string(ClipKindSilence), "ClipKindGarbage": string(ClipKindGarbage),
		"AllClipKinds": strings.Join(kinds, ","),

		"soundTagRe": tableRe(soundTagRe), "hallucinationRe": tableRe(hallucinationRe), "creditsOnlyRe": tableRe(creditsOnlyRe),
		"publisherishRe": tableRe(publisherishRe), "presentsTailRe": tableRe(presentsTailRe), "publishedByRe": tableRe(publishedByRe),
		"writtenByRe":    tableRe(writtenByRe),
		"minRepeatWords": fmt.Sprint(minRepeatWords), "maxRepeatDistinctRatio": fmt.Sprint(maxRepeatDistinctRatio), "minLetterShare": fmt.Sprint(minLetterShare),

		"clipPresentsPrefixRe": tableRe(clipPresentsPrefixRe), "creditVerbOnlyRe": tableRe(creditVerbOnlyRe), "leadSentenceEndRe": tableRe(leadSentenceEndRe),

		"markerKeywordRe": tableRe(markerKeywordRe), "matterRe": tableRe(matterRe), "bareNumberRe": tableRe(bareNumberRe),
		"midKeywordRe": tableRe(midKeywordRe), "midContinuationRe": tableRe(midContinuationRe), "continuedWordRe": tableRe(continuedWordRe),
		"connectorTitleWords": tableSet(connectorTitleWords), "maxChapterTitleWords": fmt.Sprint(maxChapterTitleWords),
		"collectionKeywordRe": tableRe(collectionKeywordRe), "listSepRe": tableRe(listSepRe),

		"numUnits": tableInts(numUnits), "numTens": tableInts(numTens), "ordUnits": tableInts(ordUnits), "ordTens": tableInts(ordTens),
		"numDigitsRe": tableRe(numDigitsRe), "numThe": tableRe(numThe), "numWordRe": tableRe(numWordRe), "numSepRe": tableRe(numSepRe),
		"romanRe": tableRe(romanRe), "maxDigitChapter": fmt.Sprint(maxDigitChapter),

		// Legacy-parser thresholds the clip grammar reads (parse.go, classify.go,
		// unchanged by this package's clip work).
		"minProseWords": fmt.Sprint(minProseWords), "creditVerbWindow": fmt.Sprint(creditVerbWindow),
	}
}

// clipGrammarNotData lists the top-level declarations of the clip grammar files
// that are deliberately NOT in the version hash, each with the reason.
var clipGrammarNotData = map[string]string{
	"ClipParserVersion": "the version itself",
	"markerChapter":     "internal marker-class tag, not data",
	"markerPart":        "internal marker-class tag, not data",
	"markerMatter":      "internal marker-class tag, not data",
	"phaseStart":        "number-scanner state tag, not data",
	"phaseUnit":         "number-scanner state tag, not data",
	"phaseTeen":         "number-scanner state tag, not data",
	"phaseTens":         "number-scanner state tag, not data",
	"phaseHundred":      "number-scanner state tag, not data",
	"phaseDone":         "number-scanner state tag, not data",
}

func clipGrammarSourcesHash() string {
	tables := clipGrammarTables()
	names := make([]string, 0, len(tables))
	for n := range tables {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s=%s\n", n, tables[n])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// TestClipGrammarTablesAreComplete fails when a top-level var or const of the clip
// grammar files is in neither the version hash nor the explicit exclusion list.
func TestClipGrammarTablesAreComplete(t *testing.T) {
	declared := map[string]bool{}
	fs := token.NewFileSet()
	for _, file := range []string{"clip.go", "clip_classify.go", "marker.go", "numbers.go"} {
		af, err := parser.ParseFile(fs, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, d := range af.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || (g.Tok != token.VAR && g.Tok != token.CONST) {
				continue
			}
			for _, s := range g.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					declared[n.Name] = true
				}
			}
		}
	}
	tables := clipGrammarTables()
	for name := range declared {
		_, hashed := tables[name]
		_, excluded := clipGrammarNotData[name]
		if !hashed && !excluded {
			t.Errorf("%s is declared in the clip grammar files but is neither in clipGrammarTables (the version hash) nor in clipGrammarNotData: add it to one", name)
		}
		if hashed && excluded {
			t.Errorf("%s is both hashed and excluded", name)
		}
	}
	// A name in the hash that no longer exists in the four files is stale (the
	// two legacy thresholds are the allowed exceptions, they live in parse.go).
	legacy := map[string]bool{"minProseWords": true, "creditVerbWindow": true}
	for name := range tables {
		if !declared[name] && !legacy[name] {
			t.Errorf("%s is in clipGrammarTables but not declared in the clip grammar files", name)
		}
	}
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
// view. Version 1's row is final.
const clipV1Pin = "15879963bc98133da824b7a3bdcbc740b8301bfc75d95cf78890ffa073bfa23b"

func clipRowDigest(r struct{ Outputs, Sources string }) string {
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
	gotOut, gotSrc := clipGrammarOutputsHash(t), clipGrammarSourcesHash()
	want := clipGrammarFingerprints[ClipParserVersion]
	if want.Outputs == "FILL" || want.Sources == "FILL" {
		t.Fatalf("no fingerprint recorded for ClipParserVersion %d; add:\n\t%d: {Outputs: %q, Sources: %q},", ClipParserVersion, ClipParserVersion, gotOut, gotSrc)
	}
	// Version 1 is final.
	if got := clipRowDigest(clipGrammarFingerprints[1]); got != clipV1Pin {
		t.Fatalf("version 1's recorded fingerprints were edited (digest %s, pinned %s): a shipped version's row is never changed; a grammar change is a new version with a new row", got, clipV1Pin)
	}

	if gotOut != want.Outputs || gotSrc != want.Sources {
		t.Fatalf("the clip grammar changed without a ClipParserVersion bump (version %d):\n"+
			"  outputs %s (recorded %s)\n  sources %s (recorded %s)\n"+
			"bump ClipParserVersion in clip.go and add a row for the new version here (see the comment at the top of this file for what forces a bump)",
			ClipParserVersion, gotOut, want.Outputs, gotSrc, want.Sources)
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
