// file: internal/transcribe/clip_version_test.go
// version: 1.0.0
// guid: 9a3d6e18-5b27-4c40-a1f9-2e8b7c0d4a65
// last-edited: 2026-11-17

package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// clipGrammarFingerprints holds, per ClipParserVersion, two fingerprints of the
// clip grammar as it stood when that version was cut:
//
//	Outputs: a hash of ParseClip over every legacy-golden input (the 188-row
//	         corpus, the 346 stored production transcripts and the table inputs).
//	Sources: a hash of the grammar's regular expressions, number tables and
//	         thresholds, so an edit that no input exercises still shows.
//
// A stored IntroParse carries the version that produced it; the re-parse op uses
// it to refuse to overwrite a parse from a NEWER grammar. That guard only works
// if the version moves whenever the grammar does, which this test enforces: a
// grammar change fails here until ClipParserVersion is bumped and the new
// version's row is added.
var clipGrammarFingerprints = map[int]struct{ Outputs, Sources string }{
	1: {Outputs: "9835476a8207a93c8a713a38fc33c7ba7693fca40b24e0a2c906c11064c92cab", Sources: "87d8b185e1cf38a18f0d40ce9e673edcf9a9cd9ef1459dc48bf0651964ed11fa"},
}

func clipGrammarOutputsHash(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, s := range legacyGoldenInputs(t) {
		if err := enc.Encode(ParseClip(s)); err != nil {
			t.Fatal(err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func clipGrammarSourcesHash() string {
	res := map[string]*regexp.Regexp{
		"soundTagRe": soundTagRe, "hallucinationRe": hallucinationRe, "creditsOnlyRe": creditsOnlyRe,
		"publisherishRe": publisherishRe, "presentsTailRe": presentsTailRe, "publishedByRe": publishedByRe,
		"writtenByRe": writtenByRe, "clipPresentsPrefixRe": clipPresentsPrefixRe, "creditVerbOnlyRe": creditVerbOnlyRe,
		"leadSentenceEndRe": leadSentenceEndRe, "markerKeywordRe": markerKeywordRe, "matterRe": matterRe,
		"bareNumberRe": bareNumberRe, "midKeywordRe": midKeywordRe, "midContinuationRe": midContinuationRe,
		"continuedWordRe": continuedWordRe,
	}
	names := make([]string, 0, len(res))
	for n := range res {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s=%s\n", n, res[n].String())
	}
	for _, m := range []map[string]int{numUnits, numTens} {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s:%d,", k, m[k])
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "minRepeatWords=%d maxRepeatDistinctRatio=%v minLetterShare=%v maxDigitChapter=%d minProseWords=%d creditVerbWindow=%d\n",
		minRepeatWords, maxRepeatDistinctRatio, minLetterShare, maxDigitChapter, minProseWords, creditVerbWindow)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func TestClipParserVersionIsBumped(t *testing.T) {
	want, ok := clipGrammarFingerprints[ClipParserVersion]
	gotOut, gotSrc := clipGrammarOutputsHash(t), clipGrammarSourcesHash()
	if !ok || want.Outputs == "FILL" {
		t.Fatalf("no fingerprint recorded for ClipParserVersion %d; add:\n\t%d: {Outputs: %q, Sources: %q},", ClipParserVersion, ClipParserVersion, gotOut, gotSrc)
	}
	if gotOut != want.Outputs || gotSrc != want.Sources {
		t.Fatalf("the clip grammar changed without a ClipParserVersion bump (version %d):\n"+
			"  outputs %s (recorded %s)\n  sources %s (recorded %s)\n"+
			"bump ClipParserVersion in clip.go and add a row for the new version here",
			ClipParserVersion, gotOut, want.Outputs, gotSrc, want.Sources)
	}
}
