// file: internal/transcribe/classify_corpus_test.go
// version: 1.0.0
// guid: c9a4e7f1-30b8-4d62-8e15-7a6f2b904c38
// last-edited: 2026-08-07

package transcribe

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The corpus is 188 real production transcripts sampled across the library
// (stratified by shape, deduplicated, trimmed to the announcement window). It
// exists because every threshold in the classifier — the title word caps, the
// credit-verb window, the prose markers — is only as good as the text it was
// tuned against, and hand-invented examples cannot tell you that "written by" is
// the single most common credit variant in the library.
//
// These tests assert INVARIANTS plus a distribution canary rather than an exact
// label per row. Exact labels would need 188 hand-verdicts and would rot on the
// first legitimate tuning change; invariants catch the defects that actually
// shipped, and the canary catches silent drift.
//
// Regenerate with the sampler documented in the PR body. Note the API paginates
// on offset/limit and SILENTLY IGNORES ?page= — a page-based sampler returns the
// same window every time and yields a corpus with 50 distinct books wearing 600
// hats.

type corpusRow struct {
	Bucket         string `json:"bucket"`
	CuratedTitle   string `json:"curated_title"`
	Text           string `json:"text"`
	LegacyTitle    string `json:"legacy_title"`
	LegacyAuthor   string `json:"legacy_author"`
	LegacyNarrator string `json:"legacy_narrator"`
	LegacyStatus   string `json:"legacy_status"`
}

func loadCorpus(t *testing.T) []corpusRow {
	t.Helper()
	f, err := os.Open("testdata/intro_corpus.jsonl")
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer f.Close()

	var rows []corpusRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r corpusRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("parse corpus line: %v", err)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan corpus: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("corpus is empty")
	}
	return rows
}

// TestCorpusInvariants asserts the properties that must hold for EVERY real
// transcript. Each one corresponds to a defect observed in production.
func TestCorpusInvariants(t *testing.T) {
	rows := loadCorpus(t)
	leakedVerbs := []string{"written", "edited", "narrated", "performed", "translated", "adapted"}

	for _, r := range rows {
		c := ClassifyIntro(r.Text, UnknownPosition)

		// 1. A non-credits verdict must never hand back fields.
		if c.Kind != IntroKindCredits && (c.Fields.Title != "" || c.Fields.Author != "") {
			t.Errorf("[%s] non-credits kind %q leaked fields %+v\n  text=%q",
				r.Bucket, c.Kind, c.Fields, trunc(r.Text))
		}

		// 2. Credits must be complete: a title with no author is not an
		// announcement, it is a split that happened to succeed.
		if c.Kind == IntroKindCredits && (c.Fields.Title == "" || c.Fields.Author == "") {
			t.Errorf("[%s] incomplete credits %+v\n  text=%q", r.Bucket, c.Fields, trunc(r.Text))
		}

		// 3. No credit verb may survive into the title. This is the 24.8%
		// library-wide defect ("Awakened Essence 1 Written").
		lt := strings.ToLower(c.Fields.Title)
		for _, v := range leakedVerbs {
			if strings.HasSuffix(strings.TrimSpace(lt), " "+v) {
				t.Errorf("[%s] title %q ends in leaked credit verb %q\n  text=%q",
					r.Bucket, c.Fields.Title, v, trunc(r.Text))
			}
		}

		// 4. A title must never be a paragraph. The worst production case ran
		// ~150 words before its "by".
		if n := len(strings.Fields(c.Fields.Title)); n > maxTitleWords {
			t.Errorf("[%s] title is %d words (cap %d): %q", r.Bucket, n, maxTitleWords, trunc(c.Fields.Title))
		}

		// 5. Confidence stays in range and every verdict is explained.
		if c.Confidence < 0 || c.Confidence > 1 {
			t.Errorf("[%s] confidence %.3f out of range", r.Bucket, c.Confidence)
		}
		if c.Reason == "" {
			t.Errorf("[%s] empty reason for text=%q", r.Bucket, trunc(r.Text))
		}
	}
}

// TestCorpusShortClipsAreUnknown pins the absent-evidence rule against real
// data: a clip that caught only a publisher jingle carries no information, and
// must not be reported as prose (which downstream reads as weak continuation
// evidence).
func TestCorpusShortClipsAreUnknown(t *testing.T) {
	for _, r := range loadCorpus(t) {
		if r.Bucket != "short_clip" {
			continue
		}
		if c := ClassifyIntro(r.Text, UnknownPosition); c.Kind == IntroKindProse {
			t.Errorf("short clip read as prose (should be unknown): %q -> %+v", trunc(r.Text), c)
		}
	}
}

// TestCorpusDistributionCanary records how the classifier currently splits the
// corpus. It is a DRIFT DETECTOR, not a correctness claim: a change here is not
// automatically a failure, but it must be a change someone intended and
// re-baselined deliberately. The bounds are wide enough to absorb ordinary
// tuning and tight enough to catch a rule that silently swallows a whole bucket.
func TestCorpusDistributionCanary(t *testing.T) {
	rows := loadCorpus(t)
	counts := map[IntroKind]int{}
	for _, r := range rows {
		counts[ClassifyIntro(r.Text, UnknownPosition).Kind]++
	}
	total := len(rows)
	t.Logf("corpus n=%d -> credits=%d chapter=%d prose=%d unknown=%d",
		total, counts[IntroKindCredits], counts[IntroKindChapter],
		counts[IntroKindProse], counts[IntroKindUnknown])

	// The recorded baseline, measured 2026-08-07 against this exact corpus. These
	// are written down rather than merely logged so that drift is a DIFF someone
	// has to justify, not a number that silently changes. A "none are zero"
	// assertion would let 30 rows migrate from credits to prose unnoticed —
	// which is precisely the regression this test is named for.
	//
	// Re-baselining is legitimate; do it deliberately, in the same commit as the
	// rule change, and say why in the message.
	baseline := map[IntroKind]int{
		IntroKindCredits: 89,
		IntroKindChapter: 50,
		IntroKindProse:   42,
		IntroKindUnknown: 7,
	}
	const tolerance = 10 // absorbs ordinary tuning, catches a bucket shifting wholesale

	if total != 188 {
		t.Fatalf("corpus size changed to %d (baseline 188) — re-baseline the counts below deliberately", total)
	}
	for _, k := range []IntroKind{IntroKindCredits, IntroKindChapter, IntroKindProse, IntroKindUnknown} {
		got, want := counts[k], baseline[k]
		if got < want-tolerance || got > want+tolerance {
			t.Errorf("kind %q: %d rows, baseline %d (±%d). If intended, update the baseline in this test and explain the rule change.",
				k, got, want, tolerance)
		}
	}
}

// TestCorpusLegacyComparison reports where the new classifier disagrees with the
// values production currently stores. It never fails: the legacy values are the
// thing being corrected, so disagreement is the POINT. It exists so the
// reviewer can eyeball the blast radius before any reparse is applied.
func TestCorpusLegacyComparison(t *testing.T) {
	rows := loadCorpus(t)
	var wouldClear, titleChanged, same int
	for _, r := range rows {
		c := ClassifyIntro(r.Text, UnknownPosition)
		switch {
		case r.LegacyTitle != "" && c.Kind != IntroKindCredits:
			wouldClear++
		case c.Fields.Title != r.LegacyTitle:
			titleChanged++
		default:
			same++
		}
	}
	t.Logf("vs stored production values: unchanged=%d title-corrected=%d would-clear=%d (of %d)",
		same, titleChanged, wouldClear, len(rows))
	t.Logf("NOTE: 'would-clear' rows are why reparse must never write on an " +
		"unknown verdict — some stored parses derive from a better transcript " +
		"that was later overwritten by a worse one.")
}

func trunc(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
