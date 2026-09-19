// file: internal/database/book_runtime.go
// version: 1.0.2
// guid: 64a8ba17-7539-4be1-a2f7-1ee0f0e0cc44
// last-edited: 2026-09-19

package database

import "math"

// Canonical book runtime.
//
// WHY this exists: every place that compared a book's runtime against
// something else — metadata candidate scoring, the bulk-apply runtime check,
// the dedup duration collectors, the AI context — read Book.Duration directly.
// Book.Duration is RecomputeBookAggregates' sum of the book_file durations
// that are KNOWN (> 0); a file whose duration was never probed contributes
// nothing and nothing records that it was skipped. For a multi-file book the
// scanner creates segment rows without probing each one, so on prod
// (2026-09-19 sample of 400 multi-file primary books) 26 carried a partial sum
// (median 50% of files, as low as 3%) that every consumer treated as the full
// runtime: a 10 h book with two known 20-minute chapters was compared at 40
// minutes and the correct 10 h candidate was vetoed as a "runtime mismatch".
// That is the "it only looks at the first file or two" symptom.
//
// ComputeBookRuntime is the ONE place that turns a book and its file rows into
// a runtime, and it says how complete that runtime is. Consumers must use
// Complete() / KnownSeconds() and treat anything else as missing evidence —
// never as a contradiction.

// RuntimeSource says where a BookRuntime's Seconds came from.
type RuntimeSource string

const (
	// RuntimeSourceFiles: summed from book_file rows.
	RuntimeSourceFiles RuntimeSource = "files"
	// RuntimeSourceBook: the stored Book.Duration aggregate, used only when the
	// book has at most one file row and that row's duration is unknown (or it
	// has no rows at all). For a single-file book Book.Duration is that file's
	// probed duration, so it can stand in; for a multi-file book it cannot.
	RuntimeSourceBook RuntimeSource = "book_aggregate"
	// RuntimeSourceNone: no usable runtime.
	RuntimeSourceNone RuntimeSource = "none"
)

// BookRuntime is a book's runtime together with how much of the book it covers.
type BookRuntime struct {
	// Seconds is the runtime. When Complete() is false it is the sum of the
	// counted files whose durations are known — the book's recorded runtime so
	// far, a lower bound on it — or 0. Never a total to compare as one.
	Seconds int `json:"seconds"`
	// Source is where Seconds came from.
	Source RuntimeSource `json:"source"`
	// FilesCounted is the number of file rows the runtime is computed over:
	// the present rows, or every row when none is present (a book whose files
	// all went missing still has a knowable runtime from its rows).
	FilesCounted int `json:"files_counted"`
	// FilesKnown is how many of FilesCounted carried a usable duration.
	FilesKnown int `json:"files_known"`
	// AllFilesMissing is true when every row is flagged missing and the
	// runtime was computed over the missing rows.
	AllFilesMissing bool `json:"all_files_missing,omitempty"`
	// FilesMissingUnmatched counts missing rows that match no present row
	// (see ComputeBookRuntime): chapters the book records but does not have
	// on disk. They are counted in FilesCounted with their durations; one
	// with no known duration makes the runtime partial like any other row.
	FilesMissingUnmatched int `json:"files_missing_unmatched,omitempty"`
	// BookAggregateSec is the stored Book.Duration, reported for display and
	// diagnosis only. It is NOT a runtime when Source != RuntimeSourceBook.
	BookAggregateSec int `json:"book_aggregate_sec,omitempty"`
}

// Complete reports whether Seconds is the book's full runtime: every counted
// file had a known duration, or the single-file Book.Duration fallback applied.
func (r BookRuntime) Complete() bool {
	if r.Seconds <= 0 {
		return false
	}
	switch r.Source {
	case RuntimeSourceFiles:
		// Every counted row has a known duration. An unmatched missing row is
		// counted WITH its duration: "missing" means the file is off disk,
		// not that its length is unknown, so a book whose every chapter has a
		// duration has a known total whether or not all are on disk.
		return r.FilesKnown > 0 && r.FilesKnown == r.FilesCounted
	case RuntimeSourceBook:
		return true
	}
	return false
}

// Partial reports whether Seconds is a lower bound that must not be compared
// as a total: some counted row (present, or missing with no present copy) has
// no known duration.
func (r BookRuntime) Partial() bool {
	return r.Source == RuntimeSourceFiles && r.FilesKnown > 0 && !r.Complete()
}

// KnownSeconds returns the full runtime and true only when Complete(); every
// comparison against another runtime must go through this.
func (r BookRuntime) KnownSeconds() (int, bool) {
	if !r.Complete() {
		return 0, false
	}
	return r.Seconds, true
}

// Status is a one-word description for logs and UI: "complete", "partial",
// "unknown", with a "book_aggregate" variant for the single-file fallback.
func (r BookRuntime) Status() string {
	switch {
	case r.Complete() && r.Source == RuntimeSourceBook:
		return "book_aggregate"
	case r.Complete():
		return "complete"
	case r.Partial():
		return "partial"
	default:
		return "unknown"
	}
}

// BookFileRuntimeSec is the duration of one file row in seconds, or 0 when
// unknown. The container duration (Duration, normalized for historical
// millisecond rows) is preferred; AcoustIDFingerprintDurationSec is the
// fallback. fpcalc reports the full decoded stream length there even though
// the fingerprint frames cover only its analysis window (verified 2026-09-19:
// a 400 s file reports 400.00 under both the default 120 s window and
// -length 0), so it is a whole-file duration and safe to use.
func BookFileRuntimeSec(f *BookFile) int {
	if f == nil {
		return 0
	}
	if f.Duration > 0 {
		return NormalizeDurationSec(f.FileSize, f.Duration)
	}
	if f.AcoustIDFingerprintDurationSec > 0 {
		return int(math.Round(f.AcoustIDFingerprintDurationSec))
	}
	return 0
}

// ComputeBookRuntime is the canonical book runtime. files are the book's
// book_file rows (as GetBookFiles returns them, missing rows included); book
// may be nil when only the rows are at hand.
//
// Rows counted:
//   - every present row;
//   - every missing row that matches NO present row (missingDuplicateOf). A
//     missing row that matches one is the old half of a repoint — the same
//     content at its previous path — and counting it would double the file.
//     An unmatched missing row is a chapter the book has and the disk lacks:
//     it is counted with its duration. Dropping it instead
//     (the first version of this function) turned "45 s intro present, 20
//     chapters missing" into a COMPLETE 45 s runtime, which the dedup
//     min-duration gate read as a 45-second book;
//   - when no row is present, every row (AllFilesMissing).
//
// Result:
//   - every counted row has a duration → Complete, Source files;
//   - some counted row has a duration → Partial: Seconds is the known sum;
//   - none has, and the book has at most one row → Book.Duration when
//     positive, Source book_aggregate, Complete;
//   - none has and the book has several rows → unknown. Book.Duration is
//     then whatever an earlier writer put there and cannot be trusted as the
//     total; it is reported in BookAggregateSec only.
func ComputeBookRuntime(book *Book, files []BookFile) BookRuntime {
	var rt BookRuntime
	if book != nil && book.Duration != nil && *book.Duration > 0 {
		rt.BookAggregateSec = *book.Duration
	}

	var present []int
	for i := range files {
		if !files[i].Missing {
			present = append(present, i)
		}
	}
	useAll := len(present) == 0
	rt.AllFilesMissing = useAll && len(files) > 0
	// Each present row can absorb at most one missing duplicate.
	absorbed := make([]bool, len(present))

	for i := range files {
		f := &files[i]
		if f.Missing && !useAll {
			if j := missingDuplicateOf(f, files, present, absorbed); j >= 0 {
				absorbed[j] = true
				continue
			}
			rt.FilesMissingUnmatched++
		}
		rt.FilesCounted++
		if sec := BookFileRuntimeSec(f); sec > 0 {
			rt.Seconds += sec
			rt.FilesKnown++
		}
	}

	if rt.FilesKnown > 0 {
		rt.Source = RuntimeSourceFiles
		return rt
	}
	rt.Seconds = 0
	if len(files) <= 1 && rt.BookAggregateSec > 0 {
		rt.Seconds = rt.BookAggregateSec
		rt.Source = RuntimeSourceBook
		return rt
	}
	rt.Source = RuntimeSourceNone
	return rt
}

// missingDuplicateOf returns the index into present of an unabsorbed present
// row that the missing row m is a copy of, or -1. A copy must be proven by
// CONTENT identity, never by a name or a size alone:
//   - a content hash in common (FileHash or OriginalFileHash on either side —
//     a repoint keeps the bytes; an organize tag-write keeps the pre-write
//     hash as OriginalFileHash), or
//   - the same original filename AND the same byte size.
//
// A base name alone is shared by every disc of a multi-disc rip
// (CD1/01.mp3, CD2/01.mp3), and a size alone by every chapter of a
// constant-bitrate split (Part 01..20, all 28.8 MB); matching on either
// absorbed real chapters. When both rows carry a duration the durations must
// also agree within durationMatchSlack, so even a hash collision between
// genuinely different files cannot absorb one. An unmatched row is counted,
// so the failure mode left is over-counting a copy nothing identifies —
// a longer runtime, which only ever makes a runtime check stricter.
func missingDuplicateOf(m *BookFile, files []BookFile, present []int, absorbed []bool) int {
	mHashes := [...]string{m.FileHash, m.OriginalFileHash}
	for j, pi := range present {
		if absorbed[j] {
			continue
		}
		p := &files[pi]
		same := false
		for _, h := range mHashes {
			if h != "" && (h == p.FileHash || h == p.OriginalFileHash) {
				same = true
			}
		}
		if !same && m.OriginalFilename != "" && m.FileSize > 0 &&
			m.OriginalFilename == p.OriginalFilename && m.FileSize == p.FileSize {
			same = true
		}
		if same && durationsAgree(BookFileRuntimeSec(m), BookFileRuntimeSec(p)) {
			return j
		}
	}
	return -1
}

// durationMatchSlack is how far two copies' durations may differ (container
// rounding, a re-mux) and still be one file: 2 s or 1%, whichever is larger.
const durationMatchSlack = 2

// durationsAgree reports whether two file durations can be the same file.
// An unknown duration (0) on either side does not disagree.
func durationsAgree(a, b int) bool {
	if a <= 0 || b <= 0 {
		return true
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	lim := max(a, b) / 100
	return d <= max(lim, durationMatchSlack)
}

// StoredAggregateSec is the value Book.Duration should hold for this runtime,
// and false when the rows give no value (keep what is stored). It is the known
// sum over the canonical runtime's counted rows: present rows plus every
// missing row that is not a repoint duplicate of a present one, millisecond
// rows normalized. That is the all-rows sum RecomputeBookAggregates always
// stored MINUS only the duplicated copies, so a chapter going missing never
// lowers it; only removing a double count does.
// Book.Duration is a display and sort field; anything that COMPARES runtimes
// must use ComputeBookRuntime and KnownSeconds instead.
func (r BookRuntime) StoredAggregateSec() (int, bool) {
	if r.Source != RuntimeSourceFiles || r.Seconds <= 0 {
		return 0, false
	}
	return r.Seconds, true
}

// BookFilesGetter is the one store method LoadBookRuntime needs.
type BookFilesGetter interface {
	GetBookFiles(bookID string) ([]BookFile, error)
}

// LoadBookRuntime reads book's file rows from store and returns its canonical
// runtime. When the rows cannot be read (nil store or a read error) the answer
// is UNKNOWN, with Book.Duration reported in BookAggregateSec only: the
// single-file Book.Duration fallback needs the rows to establish that the book
// has at most one file, and promoting a multi-file book's aggregate to a total
// is exactly the failure this function exists to prevent. The read error is
// returned so the caller can log it.
func LoadBookRuntime(store BookFilesGetter, book *Book) (BookRuntime, error) {
	if book == nil {
		return BookRuntime{Source: RuntimeSourceNone}, nil
	}
	if store == nil {
		return unreadRuntime(book), nil
	}
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		return unreadRuntime(book), err
	}
	return ComputeBookRuntime(book, files), nil
}

// unreadRuntime is the runtime of a book whose rows were not read: unknown,
// with the stored aggregate reported for display only.
func unreadRuntime(book *Book) BookRuntime {
	rt := BookRuntime{Source: RuntimeSourceNone}
	if book != nil && book.Duration != nil && *book.Duration > 0 {
		rt.BookAggregateSec = *book.Duration
	}
	return rt
}
