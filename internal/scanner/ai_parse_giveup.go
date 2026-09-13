// file: internal/scanner/ai_parse_giveup.go
// version: 1.0.0
// guid: 3c7e9a51-4d2b-4f86-b1a0-8e5d2c6f7a14
// last-edited: 2026-09-13

package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// maxAIParseSingleFileFailures is how many runs may fail to parse a file ON
// ITS OWN before the batch AI parse stops asking about it.
//
// A file that fails alone (parseBatchSplitting's recursion floor) is neither
// saved nor stamped, so without this every later scan re-nominates it and the
// split re-asks it every run -- up to 2n-1 calls per run for a batch where
// every file fails, forever. With it, each file costs at most this many
// failing runs, so a poisoned batch of n costs at most
// maxAIParseSingleFileFailures x (2n-1) calls over its lifetime: 3 x 15 = 45
// at the production batch size of 8.
const maxAIParseSingleFileFailures = 3

// aiParseGiveUpKeyPrefix namespaces the markers in the raw KV store. The key is
// a hash of the PATH, not the book ID: a renamed or re-filed book has a new
// path, hence a new key and a zero count, which is exactly the "try again when
// the filename or path changes" rule -- with no code needed to reset anything.
const aiParseGiveUpKeyPrefix = "scanner:ai_parse_single_fail:"

// aiParseFailureMark is one file's single-file AI parse failure record. It
// records a FAILURE with its reason; it is never read as "parsed".
type aiParseFailureMark struct {
	// Path is kept so a hash collision can never apply one file's record to
	// another: a mark whose Path differs is ignored.
	Path       string    `json:"path"`
	Count      int       `json:"count"`
	LastReason string    `json:"last_reason"`
	LastAt     time.Time `json:"last_at"`
}

// givenUp reports whether the cap has been reached.
func (m aiParseFailureMark) givenUp() bool {
	return m.Count >= maxAIParseSingleFileFailures
}

func aiParseGiveUpKey(path string) string {
	h := sha256.Sum256([]byte(path))
	return aiParseGiveUpKeyPrefix + hex.EncodeToString(h[:16])
}

// aiParseMarkMu serializes the read-modify-write in recordAIParseSingleFileFailure.
// Workers own disjoint candidates, but two candidates can share a path.
var aiParseMarkMu sync.Mutex

// loadAIParseFailureMark returns path's mark, or a zero mark when there is
// none, no store, or the stored record belongs to another path.
func loadAIParseFailureMark(path string) (aiParseFailureMark, error) {
	store := getStore()
	if store == nil {
		return aiParseFailureMark{}, nil
	}
	raw, err := store.GetRaw(aiParseGiveUpKey(path))
	if err != nil || len(raw) == 0 {
		return aiParseFailureMark{}, err
	}
	var m aiParseFailureMark
	if err := json.Unmarshal(raw, &m); err != nil {
		return aiParseFailureMark{}, err
	}
	if m.Path != path {
		return aiParseFailureMark{}, nil
	}
	return m, nil
}

// recordAIParseSingleFileFailure adds one failed run to path's mark and reports
// the new count and whether that reached the cap.
func recordAIParseSingleFileFailure(path, reason string) (int, bool, error) {
	store := getStore()
	if store == nil {
		return 0, false, nil
	}
	aiParseMarkMu.Lock()
	defer aiParseMarkMu.Unlock()
	m, err := loadAIParseFailureMark(path)
	if err != nil {
		// A corrupt or unreadable mark restarts the count rather than blocking
		// the record: the worst outcome is N more attempts, never a lost file.
		m = aiParseFailureMark{}
	}
	m.Path = path
	m.Count++
	m.LastReason = reason
	m.LastAt = time.Now().UTC()
	raw, err := json.Marshal(m)
	if err != nil {
		return m.Count, false, err
	}
	if err := store.SetRaw(aiParseGiveUpKey(path), raw); err != nil {
		return m.Count, false, err
	}
	return m.Count, m.givenUp(), nil
}

// filterGivenUpAIParse drops candidates whose file has reached the cap and
// returns the rest with the number dropped.
//
// A read error keeps the candidate. This is not a fail-closed read path: a
// failed lookup means one more attempt at the model, which is exactly what
// happened before the cap existed, while dropping the book would stop it being
// parsed on a transient store error.
//
// This is the ONLY place the mark is consulted, and only the batch phase calls
// it. The interactive POST /ai/parse-filename parses a filename directly and
// never goes through here, so an explicit per-file parse still reaches a
// given-up book.
func filterGivenUpAIParse(books []Book, candidates []int, log logger.Logger) ([]int, int) {
	if getStore() == nil {
		return candidates, 0
	}
	kept := make([]int, 0, len(candidates))
	skipped := 0
	for _, idx := range candidates {
		m, err := loadAIParseFailureMark(books[idx].FilePath)
		if err != nil {
			log.Warn("AI parse give-up marker unreadable for %s, parsing it anyway: %v", books[idx].FilePath, err)
		}
		if err == nil && m.givenUp() {
			skipped++
			continue
		}
		kept = append(kept, idx)
	}
	if skipped > 0 {
		log.Info("AI parse: skipping %d book(s) that failed on their own in %d runs (rename the file to retry)",
			skipped, maxAIParseSingleFileFailures)
	}
	return kept, skipped
}
