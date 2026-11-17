// file: internal/applygate/title_runtime_witness_test.go
// version: 1.5.0
// guid: 4d8b2f61-7a3c-4e95-8c10-b2f6a9e35d07
// last-edited: 2026-11-17

package applygate

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The safety case for a NEUTRAL title is that the runtime is the independent
// source: a title that neither agrees nor disagrees confirms nothing, so the
// pair must not apply unless the files' runtime confirms the match. This
// property checks it at the Evaluate level, over generated inputs: whatever
// else agrees (author, narrator, series, sequence position), a pair whose title
// check is neutral never applies while the runtime is unknown or outside the 5%
// agree band. The ASIN, the book-level transcription and the two transcription
// lifts are checked as other witnesses below.

type wtPair struct {
	stored string
	cand   metafetch.MetadataCandidate
}

// wtTitlePool covers every shape the title check can call neutral, plus shapes
// it calls agree and disagree (the property is only checked on neutral ones).
func wtTitlePool() []wtPair {
	var out []wtPair
	add := func(stored, title, sub string) {
		out = append(out, wtPair{stored, metafetch.MetadataCandidate{Title: title, Subtitle: sub}})
	}
	words := []string{"Hush", "Knightfall", "Dark Victory", "The Long Halloween", "Year One", "Batman"}
	forms := []func(string) string{
		func(w string) string { return w },
		func(w string) string { return "Batman: " + w },
		func(w string) string { return w + ": Part 2" },
		func(w string) string { return w + " 2" },
		func(w string) string { return w + " Returns" },
		func(w string) string { return w + " (Book 2)" },
		func(w string) string { return "08 - " + w },
		func(w string) string { return w + " - 001" },
		func(w string) string { return w + ": A Gotham Story" },
	}
	for _, a := range words {
		for _, b := range words {
			for _, fa := range forms {
				for _, fb := range forms[:6] {
					add(fa(a), fb(b), "")
				}
			}
		}
	}
	add("Batman", "Batman", "Dark Victory")
	add("Knightfall", "Knightfall", "Book 2")
	add("Year One", "Grayson: Year One", "")
	add("Night of Seven Owls", "Night of Five Owls", "")
	add("The Second Robin", "The Third Robin", "")
	return out
}

func wtRun(p wtPair, narrator, series, pos string, runtime string) Verdict {
	book := database.Book{Title: p.stored, Author: &database.Author{Name: "Bruce Wayne"}}
	cand := p.cand
	cand.Author = "Bruce Wayne"
	cand.Score = 0.95
	switch narrator {
	case "agree":
		book.Narrator = new("Alfred Pennyworth")
		cand.Narrator = "Alfred Pennyworth"
	case "candidate-only":
		cand.Narrator = "Alfred Pennyworth"
	}
	switch series {
	case "both":
		cand.Series = "Legends of the Dark Knight"
		cand.SeriesPosition = pos
	}
	if pos != "" {
		book.SeriesPositionRaw = new(pos)
	}
	switch runtime {
	case "none": // the files have no runtime and the candidate has one
		cand.DurationSec = 36600
	case "candidate-unknown":
		book.Duration = new(36000)
	case "6%":
		book.Duration = new(36000)
		cand.DurationSec = 38160
	case "9%":
		book.Duration = new(36000)
		cand.DurationSec = 39240
	case "30%":
		book.Duration = new(36000)
		cand.DurationSec = 46800
	}
	return Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil)
}

func wtTitleOutcome(v Verdict) string {
	for _, ch := range v.Evidence.Checks {
		if ch.Name == "title" {
			return string(ch.Outcome)
		}
	}
	return ""
}

func wtRuntimeOutcome(v Verdict) string {
	for _, ch := range v.Evidence.Checks {
		if ch.Name == "runtime" {
			return string(ch.Outcome)
		}
	}
	return ""
}

// wtForEachChunk runs fn over the pool in a fixed number of chunks, each in its
// own parallel subtest, and returns once every chunk is done. Every input is
// independent (Evaluate keeps no state between calls), so the chunks never touch
// the same data; the caller merges its per-chunk results under its own lock. The
// wrapping "chunks" subtest is what makes the parent wait: parallel subtests
// only start when their parent function returns, so code after a bare loop of
// t.Run calls would run before any chunk had.
func wtForEachChunk(t *testing.T, pool []wtPair, fn func(chunk []wtPair)) {
	t.Helper()
	const chunks = 16
	per := (len(pool) + chunks - 1) / chunks
	t.Run("chunks", func(t *testing.T) {
		for lo := 0; lo < len(pool); lo += per {
			chunk := pool[lo:min(lo+per, len(pool))]
			t.Run(strconv.Itoa(lo/per), func(t *testing.T) {
				t.Parallel()
				fn(chunk)
			})
		}
	})
}

func TestEvaluate_NeutralTitleNeverAppliesWithoutAnAgreeingRuntime(t *testing.T) {
	pool := sampled(wtTitlePool())
	var mu sync.Mutex
	checked, neutral := 0, 0
	var offenders []string
	byNarrator := map[string]int{}
	wtForEachChunk(t, pool, func(chunk []wtPair) {
		var lChecked, lNeutral int
		var lOffenders []string
		lByNarrator := map[string]int{}
		for _, p := range chunk {
			for _, narrator := range []string{"none", "candidate-only", "agree"} {
				for _, series := range []string{"none", "both"} {
					for _, pos := range []string{"", "2"} {
						for _, runtime := range []string{"none", "candidate-unknown", "6%", "9%", "30%"} {
							v := wtRun(p, narrator, series, pos, runtime)
							lChecked++
							if wtTitleOutcome(v) != string(OutcomeNeutral) || wtRuntimeOutcome(v) == string(OutcomeAgree) {
								continue
							}
							lNeutral++
							if v.Allowed {
								lByNarrator[narrator]++
								lOffenders = append(lOffenders, fmt.Sprintf("stored %q cand %q/%q narrator=%s series=%s pos=%q runtime=%s: APPLIED (%d agreements)",
									p.stored, p.cand.Title, p.cand.Subtitle, narrator, series, pos, runtime, v.Evidence.Agreements))
							}
						}
					}
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		checked += lChecked
		neutral += lNeutral
		offenders = append(offenders, lOffenders...)
		for k, n := range lByNarrator {
			byNarrator[k] += n
		}
	})
	if neutral < scaled(500) {
		t.Fatalf("only %d of %d generated inputs had a neutral title and no agreeing runtime: the generator is not reaching the case", neutral, checked)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders) // chunks finish in any order; keep the shown sample stable
		show := offenders
		if len(show) > 12 {
			show = show[:12]
		}
		for _, o := range show {
			t.Log(o)
		}
		t.Logf("applied unattended, by narrator mode: %v", byNarrator)
		t.Fatalf("%d of %d neutral-title, no-runtime-witness inputs applied unattended (first %d shown)", len(offenders), neutral, len(show))
	}
	t.Log("checked " + strconv.Itoa(checked) + " inputs, " + strconv.Itoa(neutral) + " with a neutral title and no agreeing runtime")
}

// wtWitnessRun is wtRun with a second agreement that does not look at the
// title or the length. witness is one of:
//
//	"asin"          the book and the candidate carry the same ASIN
//	"audio"         the book's transcribed title is the candidate's title (the
//	                score leg's audioConfirmed)
//	"lift-audio"    EvaluateTranscribed with a search query equal to the
//	                candidate's title AND the book-level transcription
//	"lift-query"    EvaluateTranscribed with a search query equal to the
//	                candidate's title, no book-level transcription
//
// The author is the path-confirming one, so with the title neutral these are
// the "two agreements" that used to apply on their own.
func wtWitnessRun(p wtPair, witness, runtime string) Verdict {
	book := database.Book{Title: p.stored, Author: &database.Author{Name: "Bruce Wayne"}}
	cand := p.cand
	cand.Author = "Bruce Wayne"
	cand.Score = 0.95
	switch witness {
	case "asin":
		book.ASIN = new("B00BATMAN1")
		cand.ASIN = "B00BATMAN1"
	case "audio", "lift-audio":
		book.TranscribedTitle = new(cand.Title)
		book.TranscribedAuthor = new("Bruce Wayne")
	}
	switch runtime {
	case "none":
		cand.DurationSec = 36600
	case "candidate-unknown":
		book.Duration = new(36000)
	case "4%":
		book.Duration = new(36000)
		cand.DurationSec = 37440
	case "6%":
		book.Duration = new(36000)
		cand.DurationSec = 38160
	case "9%":
		book.Duration = new(36000)
		cand.DurationSec = 39240
	case "30%":
		book.Duration = new(36000)
		cand.DurationSec = 46800
	}
	rt := database.ComputeBookRuntime(&book, nil)
	if witness == "lift-audio" || witness == "lift-query" {
		return EvaluateTranscribed(&book, snap(&book), rt, &cand, nil, nil, nil, TranscribedSearch{Query: cand.Title, Author: "Bruce Wayne"}, ManualOnlyGuard{})
	}
	return Evaluate(&book, snap(&book), rt, &cand, nil)
}

// The ASIN, the book-level transcription and the transcription lift are the
// other witnesses that are not the title and not the runtime. Each is run over
// the same pool and the same runtime sweep; the property is the one above:
// a neutral title with no agreeing runtime never applies. The counts logged
// are what each witness did before the tally rule, kept in the PR body.
func TestEvaluate_NeutralTitleNeedsRuntimeWhateverTheOtherWitness(t *testing.T) {
	for _, witness := range []string{"asin", "audio", "lift-audio", "lift-query"} {
		t.Run(witness, func(t *testing.T) {
			var mu sync.Mutex
			checked, neutral, titleAgree := 0, 0, 0
			var offenders []string
			wtForEachChunk(t, sampled(wtTitlePool()), func(chunk []wtPair) {
				var lChecked, lNeutral, lAgree int
				var lOffenders []string
				for _, p := range chunk {
					for _, runtime := range []string{"none", "candidate-unknown", "4%", "6%", "9%", "30%"} {
						v := wtWitnessRun(p, witness, runtime)
						lChecked++
						switch wtTitleOutcome(v) {
						case string(OutcomeAgree):
							lAgree++
							continue
						case string(OutcomeNeutral):
						default:
							continue
						}
						if wtRuntimeOutcome(v) == string(OutcomeAgree) {
							continue
						}
						lNeutral++
						if v.Allowed {
							lOffenders = append(lOffenders, fmt.Sprintf("stored %q cand %q runtime=%s: APPLIED", p.stored, p.cand.Title, runtime))
						}
					}
				}
				mu.Lock()
				defer mu.Unlock()
				checked += lChecked
				neutral += lNeutral
				titleAgree += lAgree
				offenders = append(offenders, lOffenders...)
			})
			sort.Strings(offenders)
			t.Logf("%s: %d inputs, %d with a neutral title and no agreeing runtime, %d with the title agreeing, %d of the neutral ones applied",
				witness, checked, neutral, titleAgree, len(offenders))
			for i, o := range offenders {
				if i == 5 {
					break
				}
				t.Log(o)
			}
			if len(offenders) > 0 {
				t.Fatalf("%d neutral-title inputs applied without an agreeing runtime", len(offenders))
			}
		})
	}
}
