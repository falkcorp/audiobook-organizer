// file: tools/cmd/dedup-dataset-audit/main.go
// version: 1.0.2
// guid: 4e7a1c92-8b3d-4f10-9c21-7a5b6c4d3e2f
// last-edited: 2026-11-09

// Command dedup-dataset-audit is a READ-ONLY analysis tool that measures, over
// the full dedup candidate set, how well an auto-labeling "oracle" could label
// pairs and how much deterministic catchers (same-folder, duration-ratio) would
// move the needle. It backs "Experiment 0" of the dedup tuning-dataset design.
//
// It answers:
//   - recording_id oracle yield: how many candidate pairs have a MusicBrainz
//     recording_id on BOTH sides (auto-labelable), and of those, same recording
//     (auto-positive) vs disjoint (auto-negative).
//   - disagreement: oracle-negative pairs that the scorer ranked CERTAIN/exact
//     (false positives) and oracle-positive pairs ranked REVIEW (false negatives).
//   - deterministic-catcher impact: same-folder pairs and low duration-ratio
//     (part-vs-whole) pairs, cross-tabbed by band — i.e. how much obvious garbage
//     a rule would suppress without any model.
//   - attribute coverage: distinct candidate books carrying a recording_id and/or
//     a (non-tombstoned) iTunes PID.
//
// Pebble is RW-only with an exclusive lock, so run this against a COPY/snapshot of
// the prod DB (the server holds the lock on the live one), e.g.:
//
//	cp -a --reflink=auto /var/lib/audiobook-organizer/audiobooks.pebble /tmp/abk-snap
//	dedup-dataset-audit -db /tmp/abk-snap
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// bookAgg is the per-book rollup we need for pair analysis, memoised so books
// that appear in many candidate pairs are only read once.
type bookAgg struct {
	recIDs      map[string]struct{} // non-empty MusicBrainz online recording IDs
	dirs        map[string]struct{} // distinct parent directories of the book's files
	totalDurSec float64             // summed best-available duration across files
	hasITunes   bool                // any non-tombstoned iTunes PID on the book/files
	fileCount   int
}

func bandOf(b string) string {
	if b == "" {
		return "(none)"
	}
	return b
}

func main() {
	dbPath := flag.String("db", "", "path to the PebbleDB directory (a COPY of prod — the live DB is locked by the server)")
	durRatioThresh := flag.Float64("dur-ratio", 0.5, "duration ratio (min/max) below which a pair is flagged part-vs-whole")
	flag.Parse()
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "usage: dedup-dataset-audit -db <pebble-dir-copy>")
		os.Exit(2)
	}

	ps, err := database.NewPebbleStore(*dbPath)
	if err != nil {
		log.Fatalf("open pebble store at %s: %v", *dbPath, err)
	}
	defer ps.Close()
	es := database.NewEmbeddingStore(ps.DB())

	// One ListCandidates call loads + sorts the whole table; a huge limit gets
	// every row in a single pass (the candidate set is ~tens of thousands).
	cands, total, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Limit:      5_000_000,
	})
	if err != nil {
		log.Fatalf("list candidates: %v", err)
	}
	log.Printf("loaded %d candidates (store reports total=%d)", len(cands), total)

	aggCache := map[string]*bookAgg{}
	getAgg := func(bookID string) *bookAgg {
		if a, ok := aggCache[bookID]; ok {
			return a
		}
		a := &bookAgg{recIDs: map[string]struct{}{}, dirs: map[string]struct{}{}}
		files, ferr := ps.GetBookFiles(bookID)
		if ferr == nil {
			a.fileCount = len(files)
			for _, f := range files {
				if f.AcoustIDOnlineRecordingID != "" {
					a.recIDs[f.AcoustIDOnlineRecordingID] = struct{}{}
				}
				if f.FilePath != "" {
					a.dirs[filepath.Dir(f.FilePath)] = struct{}{}
				}
				if f.ITunesPersistentID != "" {
					a.hasITunes = true
				}
				// Prefer the fpcalc-measured duration; fall back to the
				// container duration. Sum across files = the book's total runtime.
				if f.AcoustIDFingerprintDurationSec > 0 {
					a.totalDurSec += f.AcoustIDFingerprintDurationSec
				} else if sec, ok := f.DurationSec(); ok {
					a.totalDurSec += float64(sec)
				}
			}
		}
		if !a.hasITunes {
			if maps, merr := ps.GetExternalIDsForBook(bookID); merr == nil {
				for _, m := range maps {
					if !m.Tombstoned {
						a.hasITunes = true
						break
					}
				}
			}
		}
		aggCache[bookID] = a
		return a
	}

	// Counters.
	var (
		byBand         = map[string]int{}
		byStatus       = map[string]int{}
		byLayer        = map[string]int{}
		oracleElig     int // both sides have ≥1 recording_id
		oraclePos      int // shared recording_id
		oracleNeg      int // disjoint recording_ids
		falsePos       int // oracle-negative but band CERTAIN/HIGH (or exact layer)
		falseNeg       int // oracle-positive but band REVIEW/MEDIUM
		sameFolder     int
		sameFolderCert int
		lowDurRatio    int // pairs below dur-ratio threshold
		lowDurHighBand int // ...that also scored CERTAIN/HIGH/exact
		bothHaveDur    int
	)
	booksWithRec := map[string]struct{}{}
	booksWithITunes := map[string]struct{}{}
	allBooks := map[string]struct{}{}

	isHighBand := func(c database.DedupCandidate) bool {
		return c.Band == "CERTAIN" || c.Band == "HIGH" || c.Layer == "exact"
	}
	isLowBand := func(c database.DedupCandidate) bool {
		return c.Band == "REVIEW" || c.Band == "MEDIUM"
	}

	for _, c := range cands {
		byBand[bandOf(c.Band)]++
		byStatus[c.Status]++
		byLayer[c.Layer]++
		a := getAgg(c.EntityAID)
		b := getAgg(c.EntityBID)
		allBooks[c.EntityAID] = struct{}{}
		allBooks[c.EntityBID] = struct{}{}
		if len(a.recIDs) > 0 {
			booksWithRec[c.EntityAID] = struct{}{}
		}
		if len(b.recIDs) > 0 {
			booksWithRec[c.EntityBID] = struct{}{}
		}
		if a.hasITunes {
			booksWithITunes[c.EntityAID] = struct{}{}
		}
		if b.hasITunes {
			booksWithITunes[c.EntityBID] = struct{}{}
		}

		// recording_id pair oracle.
		if len(a.recIDs) > 0 && len(b.recIDs) > 0 {
			oracleElig++
			shared := false
			for r := range a.recIDs {
				if _, ok := b.recIDs[r]; ok {
					shared = true
					break
				}
			}
			if shared {
				oraclePos++
				if isLowBand(c) {
					falseNeg++
				}
			} else {
				oracleNeg++
				if isHighBand(c) {
					falsePos++
				}
			}
		}

		// same-folder (shared parent directory) — the screenshot bug class.
		sf := false
		for d := range a.dirs {
			if _, ok := b.dirs[d]; ok {
				sf = true
				break
			}
		}
		if sf {
			sameFolder++
			if c.Band == "CERTAIN" || c.Layer == "exact" {
				sameFolderCert++
			}
		}

		// duration ratio (part-vs-whole).
		if a.totalDurSec > 0 && b.totalDurSec > 0 {
			bothHaveDur++
			lo, hi := a.totalDurSec, b.totalDurSec
			if lo > hi {
				lo, hi = hi, lo
			}
			if hi > 0 && lo/hi < *durRatioThresh {
				lowDurRatio++
				if isHighBand(c) {
					lowDurHighBand++
				}
			}
		}
	}

	pct := func(n, d int) string {
		if d == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
	}
	printMap := func(title string, m map[string]int) {
		fmt.Printf("\n%s:\n", title)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
		for _, k := range keys {
			fmt.Printf("  %-12s %6d  (%s)\n", k, m[k], pct(m[k], len(cands)))
		}
	}

	fmt.Printf("\n================ DEDUP DATASET AUDIT ================\n")
	fmt.Printf("candidates analysed: %d   distinct books: %d\n", len(cands), len(allBooks))
	printMap("by band", byBand)
	printMap("by status", byStatus)
	printMap("by layer", byLayer)

	fmt.Printf("\n---- recording_id pair oracle ----\n")
	fmt.Printf("books with ≥1 online recording_id: %d / %d  (%s)\n", len(booksWithRec), len(allBooks), pct(len(booksWithRec), len(allBooks)))
	fmt.Printf("books with a live iTunes PID:       %d / %d  (%s)\n", len(booksWithITunes), len(allBooks), pct(len(booksWithITunes), len(allBooks)))
	fmt.Printf("pairs auto-labelable (both sides have recording_id): %d  (%s of all pairs)\n", oracleElig, pct(oracleElig, len(cands)))
	fmt.Printf("  ├─ auto-POSITIVE (shared recording):   %d  (%s of eligible)\n", oraclePos, pct(oraclePos, oracleElig))
	fmt.Printf("  └─ auto-NEGATIVE (disjoint recordings): %d  (%s of eligible)\n", oracleNeg, pct(oracleNeg, oracleElig))
	fmt.Printf("  DISAGREEMENT with scorer:\n")
	fmt.Printf("    false positives (auto-neg but band CERTAIN/HIGH/exact): %d\n", falsePos)
	fmt.Printf("    false negatives (auto-pos but band REVIEW/MEDIUM):      %d\n", falseNeg)

	fmt.Printf("\n---- deterministic catchers (no model) ----\n")
	fmt.Printf("same-folder pairs (shared parent dir): %d  (%s of all pairs)\n", sameFolder, pct(sameFolder, len(cands)))
	fmt.Printf("  └─ of those scored CERTAIN/exact:    %d  (%s) — the screenshot bug class\n", sameFolderCert, pct(sameFolderCert, sameFolder))
	fmt.Printf("pairs with both durations known:       %d\n", bothHaveDur)
	fmt.Printf("low duration-ratio (<%.2f) pairs:      %d  (%s of dur-known)\n", *durRatioThresh, lowDurRatio, pct(lowDurRatio, bothHaveDur))
	fmt.Printf("  └─ of those scored CERTAIN/HIGH/exact: %d — part-vs-whole false positives\n", lowDurHighBand)
	fmt.Printf("====================================================\n")
}
