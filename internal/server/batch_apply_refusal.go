// file: internal/server/batch_apply_refusal.go
// version: 1.0.0
// guid: 4b23c08d-8533-4d97-9ea7-f107b4c18796
// last-edited: 2026-10-30
//
// A memory of the structural refusals metadata.batch-apply-cached has already
// made, so a later run does not re-offer a book whose refusal cannot change.
// Design and rejected alternatives:
// docs/decisions/2026-10-30-batch-apply-structural-refusal-backoff.md.

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// applyRefusalKeyPrefix is the key family of the per-book refusal records:
// apply_refusal:<book_id>. Registered in internal/database/keyfamilies.go.
const applyRefusalKeyPrefix = "apply_refusal:"

// applyRefusalFingerprintVersion is the shape of the fingerprint below. Bump it
// whenever the set of hashed inputs changes: every older record then reads as
// "changed" and the book is evaluated again. It does NOT track gate rules (see
// applyRefusalBuild).
const applyRefusalFingerprintVersion = 1

// applyRefusalRecord is what apply_refusal:<book_id> holds.
type applyRefusalRecord struct {
	// Reason is the applygate reason code (applygate.StructuralRefusal).
	Reason string `json:"reason"`
	// Detail is the gate's sentence, kept so an operator reading the record
	// sees why the book is being skipped.
	Detail string  `json:"detail,omitempty"`
	Score  float64 `json:"score"`
	// Fingerprint is applyRefusalInputs.fingerprint() at the time of the refusal.
	Fingerprint string `json:"fingerprint"`
	// FingerprintVersion and Build bind the record to the code that judged it:
	// a relaxed gate rule changes no input, so only a new build can notice it.
	FingerprintVersion int       `json:"fingerprint_version"`
	Build              string    `json:"build"`
	RecordedAt         time.Time `json:"recorded_at"`
}

func applyRefusalKey(bookID string) string { return applyRefusalKeyPrefix + bookID }

// applyRefusalBuild identifies the running binary: the VCS revision, its dirty
// flag and the link flags (which carry the version, -X main.version=...). A
// stored refusal is trusted only by the build that wrote it, so deploying a gate
// change re-evaluates every remembered book once instead of leaving it
// suppressed by a rule that no longer applies. A build with no stamp (go test,
// a bare go run) reads "", so its records last until the reset op.
//
// A var so a test can stand in for a second deploy.
var applyRefusalBuild = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var parts []string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision", "vcs.modified", "-ldflags":
			parts = append(parts, s.Key+"="+s.Value)
		}
	}
	return strings.Join(parts, ";")
})

// applyRefusalInputs is everything the structural legs of the certainty gate
// read about one book, captured from the SAME reads the gate judged
// (planCachedApply), so a refetch between the judgement and the record cannot
// stamp an old reason onto a candidate the gate never saw. The read side
// (gatherApplyRefusalInputs) builds the same value from a fresh read, and both
// sides fingerprint it with the one function below.
//
// The inputs are deliberately generous. Too many only re-offers a book -- what
// happens today. Too few leaves a repaired book suppressed forever.
type applyRefusalInputs struct {
	// RawCandidate is the stored JSON of the top cached candidate: its source,
	// ASIN, title, author, narrator, runtime and score, all of it. Hashed as
	// stored rather than re-marshalled because the gate stamps evidence onto
	// its decoded copy.
	RawCandidate      []byte
	SearchFingerprint string
	CacheStale        bool
	Book              *database.Book
	Authors           []string
	Files             []database.BookFile
}

// fingerprint is the SHA-256 of the canonical form of the inputs. Files are
// sorted so a re-ordered read does not change it; authors are not, because
// their order is the credit order.
func (in applyRefusalInputs) fingerprint() string {
	type fileDoc struct {
		Path     string
		Size     int64
		Duration int
		Hash     string
		Missing  bool
	}
	files := make([]fileDoc, 0, len(in.Files))
	for _, f := range in.Files {
		files = append(files, fileDoc{Path: f.FilePath, Size: f.FileSize, Duration: f.Duration, Hash: f.FileHash, Missing: f.Missing})
	}
	slices.SortFunc(files, func(a, b fileDoc) int {
		return strings.Compare(fmt.Sprintf("%s\x00%s", a.Path, a.Hash), fmt.Sprintf("%s\x00%s", b.Path, b.Hash))
	})
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	num := func(p *int) int {
		if p == nil {
			return -1
		}
		return *p
	}
	type bookDoc struct {
		Title, Subtitle, FilePath, Author, Series string
		SeriesSequence, Duration                  int
		SeriesPositionRaw, Narrator               string
		ASIN, ISBN10, ISBN13                      string
		TranscribedTitle, TranscribedAuthor       string
	}
	var b bookDoc
	if bk := in.Book; bk != nil {
		b = bookDoc{
			Title: bk.Title, Subtitle: str(bk.Subtitle), FilePath: bk.FilePath,
			SeriesSequence: num(bk.SeriesSequence), Duration: num(bk.Duration),
			SeriesPositionRaw: str(bk.SeriesPositionRaw), Narrator: str(bk.Narrator),
			ASIN: str(bk.ASIN), ISBN10: str(bk.ISBN10), ISBN13: str(bk.ISBN13),
			TranscribedTitle: str(bk.TranscribedTitle), TranscribedAuthor: str(bk.TranscribedAuthor),
		}
		if bk.Author != nil {
			b.Author = bk.Author.Name
		}
		if bk.Series != nil {
			b.Series = bk.Series.Name
		}
	}
	doc := struct {
		Candidate         string
		SearchFingerprint string
		CacheStale        bool
		Book              bookDoc
		Authors           []string
		Files             []fileDoc
	}{
		Candidate:         hexSHA256(in.RawCandidate),
		SearchFingerprint: in.SearchFingerprint,
		CacheStale:        in.CacheStale,
		Book:              b,
		Authors:           in.Authors,
		Files:             files,
	}
	raw, _ := json.Marshal(doc) // plain strings, numbers and slices: cannot fail
	return hexSHA256(raw)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// gatherApplyRefusalInputs reads, fresh, the inputs a remembered refusal was
// fingerprinted from. ok is false when any read fails or there is no cached
// candidate: the caller then evaluates the book normally (fail open).
func gatherApplyRefusalInputs(svc cachedApplyService, books bookReader, id string) (applyRefusalInputs, bool) {
	entry, _, err := svc.GetCachedCandidates(id)
	if err != nil || entry == nil || len(entry.Candidates) == 0 {
		return applyRefusalInputs{}, false
	}
	book, err := books.GetBookByID(id)
	if err != nil || book == nil {
		return applyRefusalInputs{}, false
	}
	authors, err := database.LiveBookAuthorNames(books, book)
	if err != nil {
		return applyRefusalInputs{}, false
	}
	files, err := books.GetBookFiles(id)
	if err != nil {
		return applyRefusalInputs{}, false
	}
	return applyRefusalInputs{
		RawCandidate:      entry.Candidates[0],
		SearchFingerprint: entry.SearchFingerprint,
		CacheStale:        entry.Stale,
		Book:              book,
		Authors:           authors,
		Files:             files,
	}, true
}

// applyRefusalLedger reads and writes the per-book refusal records. The zero
// value and a nil pointer are both a ledger that remembers nothing, so a test
// double with no raw KV store needs no special case.
type applyRefusalLedger struct {
	kv    database.RawKVStore
	build string
	now   func() time.Time
}

func newApplyRefusalLedger(kv database.RawKVStore) *applyRefusalLedger {
	if kv == nil {
		return nil
	}
	return &applyRefusalLedger{kv: kv, build: applyRefusalBuild(), now: time.Now}
}

// Lookup returns the stored record for id. A read failure or an undecodable
// value is "no record": the book is evaluated normally and, if it is refused
// again, the record is rewritten.
func (l *applyRefusalLedger) Lookup(id string) (applyRefusalRecord, bool) {
	if l == nil || l.kv == nil {
		return applyRefusalRecord{}, false
	}
	raw, err := l.kv.GetRaw(applyRefusalKey(id))
	if err != nil || len(raw) == 0 {
		if err != nil {
			logging.Warn(context.Background(), "apply refusal record unreadable; evaluating the book normally", "book_id", id, "err", err)
		}
		return applyRefusalRecord{}, false
	}
	var rec applyRefusalRecord
	if json.Unmarshal(raw, &rec) != nil {
		return applyRefusalRecord{}, false
	}
	return rec, true
}

// Skip decides whether id is left out of this run unevaluated. had reports
// whether a record existed (hand it back to Settle). A pinned book is the owner
// choosing to apply over the gate, so it is never skipped and never consults the
// memory.
func (l *applyRefusalLedger) Skip(svc cachedApplyService, books bookReader, id string, pin *metafetch.CandidatePin) (rec applyRefusalRecord, had, skip bool) {
	if l == nil || pin != nil {
		return applyRefusalRecord{}, false, false
	}
	rec, had = l.Lookup(id)
	return rec, had, had && l.unchanged(rec, svc, books, id)
}

// unchanged reports whether rec still describes id: its reason is structural,
// it was written by this build and fingerprint version, and the inputs read now
// fingerprint to what it stored. Any doubt answers false (re-evaluate).
func (l *applyRefusalLedger) unchanged(rec applyRefusalRecord, svc cachedApplyService, books bookReader, id string) bool {
	if l == nil || !applygate.StructuralRefusal(rec.Reason) ||
		rec.FingerprintVersion != applyRefusalFingerprintVersion || rec.Build != l.build {
		return false
	}
	in, ok := gatherApplyRefusalInputs(svc, books, id)
	return ok && in.fingerprint() == rec.Fingerprint
}

// Settle updates the record after id was evaluated. had says a record existed
// when the book was looked up, so a successful apply costs a delete only for
// the rare book that had one (a delete is a synced write).
//
//   - refused for a structural reason: write the record for these inputs;
//   - applied, or refused for a reason that is not structural: drop a record
//     that no longer describes the book;
//   - anything else (no candidate, apply failed, authors unreadable, ...): leave it
//     alone. It is checked against the fingerprint before it is ever trusted.
//
// A write failure is logged and otherwise ignored: the cost of losing a record
// is one more evaluation of the book.
func (l *applyRefusalLedger) Settle(id string, out applyOutcome, had bool, pin *metafetch.CandidatePin) {
	// A pinned apply is judged with the owner's review in play (a row pin even
	// skips the manual-only guard), so its verdict is not the unattended one a
	// later run would reach.
	if l == nil || l.kv == nil || pin != nil {
		return
	}
	switch {
	case out.Applied:
		l.drop(id, had)
	case out.Reason == applySkipGateBlocked && out.Gate != nil && out.Judged != nil && applygate.StructuralRefusal(out.Gate.Reason):
		rec := applyRefusalRecord{
			Reason: out.Gate.Reason, Detail: out.Gate.Detail, Score: out.Gate.Score,
			Fingerprint:        out.Judged.fingerprint(),
			FingerprintVersion: applyRefusalFingerprintVersion,
			Build:              l.build,
			RecordedAt:         l.now().UTC(),
		}
		raw, err := json.Marshal(rec)
		if err == nil {
			err = l.kv.SetRaw(applyRefusalKey(id), raw)
		}
		if err != nil {
			logging.Warn(context.Background(), "apply refusal record not written; the book will be evaluated again next run", "book_id", id, "err", err)
		}
	case out.Reason == applySkipGateBlocked && out.Gate != nil:
		l.drop(id, had)
	}
}

func (l *applyRefusalLedger) drop(id string, had bool) {
	if !had {
		return
	}
	if err := l.kv.DeleteRaw(applyRefusalKey(id)); err != nil {
		logging.Warn(context.Background(), "stale apply refusal record not deleted", "book_id", id, "err", err)
	}
}
