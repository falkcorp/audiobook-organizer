// file: internal/franchise/detect.go
// version: 1.1.0
// guid: 5b0e6d2a-8c4f-4e71-9a3d-1f7c2b9e4d60
// last-edited: 2026-10-05

package franchise

import (
	"sort"
	"strings"
)

// Signal field names: where on a book a hit was found.
const (
	FieldPath                  = "path"
	FieldTitle                 = "title"
	FieldSeries                = "series"
	FieldNarrator              = "narrator"
	FieldAuthor                = "author"
	FieldPublisher             = "publisher"
	FieldTranscribedTitle      = "transcribed_title"
	FieldTranscribedAuthor     = "transcribed_author"
	FieldFilePath              = "file_path"
	FieldFileTranscribedTitle  = "file_transcribed_title"
	FieldFileTranscribedAuthor = "file_transcribed_author"
	FieldTag                   = "tag"
	// FieldFolderContents and FieldTwinFile are the weak, library-wide
	// signals a caller with a whole-library index adds itself (the
	// tag-franchise fixer): the book's folder holds files that carry a
	// signal, or a file of the book (same name and size) also sits in a
	// franchise folder.
	FieldFolderContents = "folder_contents"
	FieldTwinFile       = "twin_file"
)

// Signal is one piece of evidence on a book.
type Signal struct {
	Field string `json:"field"`
	Value string `json:"value"`
	Hit   Hit    `json:"hit"`
	// Weak marks evidence that holds the book but is not enough on its own
	// to tag it without a person reading the row: a title only the broad
	// rule names or one named only by a census range term, a credit named
	// only by "Missy", or a library-wide folder/twin signal.
	Weak bool `json:"weak,omitempty"`
}

// Evidence is everything Detect reads about one book. Empty fields are
// skipped.
type Evidence struct {
	Path              string
	Title             string
	Series            string
	Publisher         string
	Narrators         []string
	Authors           []string
	TranscribedTitle  string
	TranscribedAuthor string
	// FilePaths are the paths of every book_file row, missing rows
	// included.
	FilePaths              []string
	FileTranscribedTitles  []string
	FileTranscribedAuthors []string
	// Tags are the book's tag strings; a franchise: tag is a strong signal.
	Tags []string
}

// Result is what Detect found.
type Result struct {
	Signals []Signal `json:"signals"`
}

// weakCreditTerm: a credit (narrator, author) whose only hit is "Missy" is
// weak. The guard still holds it -- the original pattern always did -- but
// "Missy Elliott" or a narrator named Missy is not evidence enough to tag a
// book (census §1: "Missy as a first name is excluded").
func weakCreditTerm(hits []Hit) bool {
	for _, h := range hits {
		if !(h.Term == "core" && strings.EqualFold(h.Text, "missy")) {
			return false
		}
	}
	return len(hits) > 0
}

// MatchesCreditStrong reports whether a credit value (narrator, publisher)
// names the franchise by more than "Missy" alone. Owner decision 2026-10-05:
// in the credit checks the bulk-apply guard added on 2026-10-04 (narrator and
// publisher), a credit named only by "Missy" ("Missy Cambridge", "Missy
// Elliott") does not hold a book. Title, path and series keep the original
// pattern, "Missy" included: the old guard held on those.
func MatchesCreditStrong(s string) bool {
	hits := MatchAll(s)
	return len(hits) > 0 && !weakCreditTerm(hits)
}

// Detect returns every signal in e, strong and weak.
func Detect(e Evidence) Result {
	var r Result
	add := func(field, value string, weak bool, hits []Hit) {
		if h, ok := best(hits); ok {
			r.Signals = append(r.Signals, Signal{Field: field, Value: value, Hit: h, Weak: weak})
		}
	}
	one := func(field, value string) {
		add(field, value, false, MatchAll(value))
	}
	credit := func(field, value string) {
		hits := MatchAll(value)
		add(field, value, weakCreditTerm(hits), hits)
	}
	one(FieldPath, e.Path)
	if e.Title != "" {
		if h, ok := MatchTitle(e.Title); ok {
			// Only the franchise's own words in a title are strong
			// ("Doctor Who: ...", "Torchwood: ...", the studio). A title named
			// only by a census range term ("Genesis of the Cybermen", "The
			// Sirens of Time") is weak: the words also name novels, episodes
			// of other shows and prose. The guards still hold on it.
			r.Signals = append(r.Signals, Signal{Field: FieldTitle, Value: e.Title, Hit: h, Weak: h.Term != "title"})
		} else {
			// The bulk-apply guard has always held a title the broad rule
			// names ("Big Finish to the Season"); not enough to tag.
			add(FieldTitle, e.Title, true, MatchAll(e.Title))
		}
	}
	one(FieldSeries, e.Series)
	credit(FieldPublisher, e.Publisher)
	for _, n := range e.Narrators {
		credit(FieldNarrator, n)
	}
	for _, a := range e.Authors {
		credit(FieldAuthor, a)
	}
	one(FieldTranscribedTitle, e.TranscribedTitle)
	credit(FieldTranscribedAuthor, e.TranscribedAuthor)
	seen := map[string]bool{}
	for _, p := range e.FilePaths {
		if p == "" || seen[p] || p == e.Path {
			continue
		}
		seen[p] = true
		one(FieldFilePath, p)
	}
	for _, t := range e.FileTranscribedTitles {
		one(FieldFileTranscribedTitle, t)
	}
	for _, a := range e.FileTranscribedAuthors {
		credit(FieldFileTranscribedAuthor, a)
	}
	for _, t := range e.Tags {
		if _, ok := HeldByTags([]string{t}); ok {
			h := Hit{Term: "tag", Text: t, Franchise: strings.TrimPrefix(strings.ToLower(t), FranchiseTagPrefix)}
			r.Signals = append(r.Signals, Signal{Field: FieldTag, Value: t, Hit: h})
		}
	}
	return r
}

// Held reports whether any signal, strong or weak, was found: the guards
// hold on any.
func (r Result) Held() bool { return len(r.Signals) > 0 }

// Strong reports whether at least one signal is strong.
func (r Result) Strong() bool {
	for _, s := range r.Signals {
		if !s.Weak {
			return true
		}
	}
	return false
}

// First returns the first signal (strong ones first), for a guard's reason.
func (r Result) First() (Signal, bool) {
	for _, s := range r.Signals {
		if !s.Weak {
			return s, true
		}
	}
	if len(r.Signals) > 0 {
		return r.Signals[0], true
	}
	return Signal{}, false
}

// Classify picks the franchise and range the signals agree on. Strong
// signals are counted first; weak ones only when there is no strong one.
// The franchise named most often wins (ties: Doctor Who, then Torchwood,
// then Big Finish, since a Big Finish Doctor Who release names both); the
// range is the most-named range of that franchise, "" when none is named.
// Tag signals are not counted: a tag records an earlier answer, it is not
// evidence for one.
func (r Result) Classify() (franchise, rng string) {
	pick := func(weak bool) (string, string) {
		fc := map[string]int{}
		rc := map[string]map[string]int{}
		for _, s := range r.Signals {
			if s.Weak != weak || s.Field == FieldTag || s.Hit.Franchise == "" {
				continue
			}
			fc[s.Hit.Franchise]++
			if s.Hit.Range != "" {
				if rc[s.Hit.Franchise] == nil {
					rc[s.Hit.Franchise] = map[string]int{}
				}
				rc[s.Hit.Franchise][s.Hit.Range]++
			}
		}
		if len(fc) == 0 {
			return "", ""
		}
		order := map[string]int{DoctorWho: 0, Torchwood: 1, BigFinish: 2}
		fs := make([]string, 0, len(fc))
		for f := range fc {
			fs = append(fs, f)
		}
		// A Big Finish range that is not Doctor Who (Stargate, Dark
		// Shadows) outranks the bare studio name it is filed under.
		sort.Slice(fs, func(i, j int) bool {
			if fc[fs[i]] != fc[fs[j]] {
				return fc[fs[i]] > fc[fs[j]]
			}
			return order[fs[i]] < order[fs[j]]
		})
		f := fs[0]
		// "Big Finish" alongside a Doctor Who range: the release is Doctor
		// Who. Doctor Who signals of any count beat a bare studio name.
		if f == BigFinish && fc[DoctorWho] > 0 && len(rc[BigFinish]) == 0 {
			f = DoctorWho
		}
		best, n := "", 0
		rs := make([]string, 0, len(rc[f]))
		for x := range rc[f] {
			rs = append(rs, x)
		}
		sort.Strings(rs)
		for _, x := range rs {
			if rc[f][x] > n {
				best, n = x, rc[f][x]
			}
		}
		return f, best
	}
	if f, x := pick(false); f != "" {
		return f, x
	}
	return pick(true)
}
