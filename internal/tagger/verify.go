// file: internal/tagger/verify.go
// version: 1.0.0
// guid: 3e8d1b6a-5c47-4a92-b0f3-7d2a9c4e1f58
// last-edited: 2026-10-11
//
// verify_after_write: the read-back every tag writer shares.
//
// tagger.WriteTagsSafe, and the native (cgo) writers in internal/metadata,
// which production builds use (-tags native_taglib), all finish a write by
// calling SafeWriteDeps.VerifyWritten, so the setting behaves the same in
// every build variant.

package tagger

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// tagReader reads the tags back for verification; a variable so tests can
// substitute a reader.
var tagReader = readTags

// ErrVerifyAfterWrite is returned (wrapped in *VerifyAfterWriteError) when the
// verify_after_write read-back finds a written tag missing or different. The
// write itself succeeded: the file stays as written.
var ErrVerifyAfterWrite = errors.New("tag write verification failed")

// KeyDiff is one tag key whose read-back differed from what was written.
type KeyDiff struct {
	// Key is the upper-cased tag key.
	Key string
	// Want is what the write asked for (nil for a delete).
	Want []string
	// Got is what the file holds; nil when the key is absent.
	Got []string
}

// VerifyAfterWriteError names the tag keys whose read-back differed.
type VerifyAfterWriteError struct {
	Path string
	// Keys are the differing keys, upper-cased and sorted.
	Keys []string
	// Diffs holds the wanted and read values of each key in Keys, in the same
	// order.
	Diffs []KeyDiff
	// ReadErr is why the file could not be read back, when that is the failure.
	// Every written key is then in Keys.
	ReadErr error
}

func (e *VerifyAfterWriteError) Error() string {
	msg := fmt.Sprintf("%s: %s:%s", ErrVerifyAfterWrite.Error(), e.Path, e.diffText(maxErrorBytes))
	if e.ReadErr != nil {
		msg += fmt.Sprintf(" (read-back failed: %v)", e.ReadErr)
	}
	return msg
}

func (e *VerifyAfterWriteError) Unwrap() []error {
	if e.ReadErr != nil {
		return []error{ErrVerifyAfterWrite, e.ReadErr}
	}
	return []error{ErrVerifyAfterWrite}
}

// Bounds on the text a mismatch renders to, so one pathological value (a
// description of several KB) cannot bloat a log line or an append-only ledger.
const (
	maxDetailValueRunes = 80
	maxDetailBytes      = 1024
	maxErrorBytes       = 512
)

// diffText renders each differing key with the value wanted and the value
// read, as " KEY want [...] got [...];" pieces. Values are quoted and
// truncated, and the text stops, naming how many keys were left out, before it
// would pass limit bytes.
func (e *VerifyAfterWriteError) diffText(limit int) string {
	var b strings.Builder
	for i, d := range e.Diffs {
		piece := fmt.Sprintf(" %s want %s got %s;", d.Key, renderValues(d.Want, true), renderValues(d.Got, false))
		if b.Len()+len(piece) > limit {
			fmt.Fprintf(&b, " (+%d more keys: %s)", len(e.Diffs)-i, strings.Join(e.Keys[i:], ", "))
			break
		}
		b.WriteString(piece)
	}
	return b.String()
}

// Detail renders the mismatch for the provenance ledger: each differing key
// with the value wanted and the value read, within maxDetailBytes.
func (e *VerifyAfterWriteError) Detail() string {
	text := "verify_after_write mismatch:"
	if e.ReadErr != nil {
		text += " read-back failed: " + truncateRunes(e.ReadErr.Error(), maxDetailValueRunes) + ";"
	}
	return text + e.diffText(maxDetailBytes)
}

func renderValues(vs []string, wanted bool) string {
	if len(vs) == 0 {
		if wanted {
			return "<delete>"
		}
		return "<absent>"
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = strconv.Quote(truncateRunes(v, maxDetailValueRunes))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}

// FoldTagKeys returns tags with its keys upper-cased, the form taglib stores
// them in. Two keys that differ only by case ({"title": .., "TITLE": ..})
// name one property, and the writer would keep whichever its map iteration
// reached last; folding them first makes that choice deterministic: the
// key already upper-case wins, else the first in sorted order. Every writer
// folds before it writes, and the verifier compares against the folded map, so
// the value checked is the value written.
func FoldTagKeys(tags map[string][]string) map[string][]string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string][]string, len(tags))
	winner := make(map[string]string, len(tags)) // upper-cased key -> original key chosen
	for _, k := range keys {
		u := strings.ToUpper(k)
		prev, seen := winner[u]
		switch {
		case !seen:
			winner[u] = k
			out[u] = tags[k]
		case k == u:
			// An exact upper-case key outranks a differently-cased twin.
			logCaseConflict(u, prev, k, out[u], tags[k])
			winner[u] = k
			out[u] = tags[k]
		default:
			logCaseConflict(u, k, prev, tags[k], out[u])
		}
	}
	return out
}

// logCaseConflict warns when two spellings of one key carry different values;
// the same values in two spellings are harmless and stay quiet.
func logCaseConflict(upper, dropped, kept string, droppedVals, keptVals []string) {
	if strings.Join(droppedVals, "\v") == strings.Join(keptVals, "\v") {
		return
	}
	logger.New("tagger").Warn("tag write names %s twice with different values; keeping %s, dropping %s",
		logger.SanitizeLogValue(upper), logger.SanitizeLogValue(kept), logger.SanitizeLogValue(dropped))
}

// numericTagKeys are the properties MP4 stores as integer atoms (trkn, disk,
// tmpo, cpil). TagLib renders them back from the integers, so "03" reads back
// as "3" and "03/10" as "3/10"; verification compares them numerically.
var numericTagKeys = map[string]bool{
	"TRACKNUMBER": true,
	"DISCNUMBER":  true,
	"BPM":         true,
	"COMPILATION": true,
}

// normalizeNumeric strips leading zeros from each "/"-separated part of v when
// every part is a plain number ("03/010" -> "3/10"); any other value is
// returned unchanged, so a non-numeric value is still compared as text.
func normalizeNumeric(v string) string {
	parts := strings.Split(strings.TrimSpace(v), "/")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return v
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return v
			}
		}
		if p = strings.TrimLeft(p, "0"); p == "" {
			p = "0"
		}
		parts[i] = p
	}
	return strings.Join(parts, "/")
}

// isDelete reports whether writing want removes the key. TagLib's writer
// joins the values with \v and erases the key when the joined text is empty,
// so {} and {""} are both deletes.
func isDelete(want []string) bool {
	return strings.Join(want, "\v") == ""
}

// valuesMatch reports whether the values read back for key are what a write of
// want leaves. A delete must read back absent or empty. Otherwise the values
// must be equal as ordered lists, MP4's numeric atoms compared numerically,
// and a written value containing \v (the writer's separator) may read back
// split.
func valuesMatch(key string, want, got []string) bool {
	if isDelete(want) {
		return isDelete(got)
	}
	numeric := numericTagKeys[key]
	if equalValues(want, got, numeric) {
		return true
	}
	if joined := strings.Join(want, "\v"); strings.Contains(joined, "\v") {
		return equalValues(strings.Split(joined, "\v"), got, numeric)
	}
	return false
}

func equalValues(a, b []string, numeric bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if numeric {
			x, y = normalizeNumeric(x), normalizeNumeric(y)
		}
		if x != y {
			return false
		}
	}
	return true
}

// verifyWrittenTags re-reads path and compares every key in want.
//
// Keys match case-insensitively, because taglib upper-cases the keys it reads
// back; keys that differ only by case in want are folded first (FoldTagKeys).
// Values must be equal as ordered slices (valuesMatch has the exceptions). Keys
// not in want are ignored, so a merge write is not blamed for tags it did not
// touch. A read failure is itself a verification failure, reported against
// every written key.
func verifyWrittenTags(path string, want map[string][]string) *VerifyAfterWriteError {
	want = FoldTagKeys(want)
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	got, rerr := tagReader(path)
	var diffs []KeyDiff
	if rerr != nil {
		for _, k := range keys {
			diffs = append(diffs, KeyDiff{Key: k, Want: nonEmptyOrNil(want[k])})
		}
	} else {
		have := make(map[string][]string, len(got))
		for k, v := range got {
			have[strings.ToUpper(k)] = v
		}
		for _, k := range keys {
			if !valuesMatch(k, want[k], have[k]) {
				diffs = append(diffs, KeyDiff{Key: k, Want: nonEmptyOrNil(want[k]), Got: nonEmptyOrNil(have[k])})
			}
		}
	}
	if len(diffs) == 0 {
		return nil
	}
	verr := &VerifyAfterWriteError{Path: path, Diffs: diffs, ReadErr: rerr}
	for _, d := range diffs {
		verr.Keys = append(verr.Keys, d.Key)
	}
	return verr
}

// nonEmptyOrNil maps a delete ({} or {""}) to nil, which renders as "<delete>"
// or "<absent>", and any other list to itself.
func nonEmptyOrNil(vs []string) []string {
	if isDelete(vs) {
		return nil
	}
	return vs
}

// VerifyWritten is the verify_after_write step of a tag write that has already
// landed on disk: when the setting is on (or deps.ForceVerify), it re-reads
// path and checks every key in want, returning a *VerifyAfterWriteError
// (wrapping ErrVerifyAfterWrite) on a difference, with a FileEventObserved
// event recorded for it. The file is never touched.
//
// bookFileID is the book_file row of the file written (empty when it has none)
// and postHash its whole-file SHA-256 when the writer computed one (empty
// otherwise). The event is keyed on bookFileID when there is one, else on the
// file's digest, so a later AdoptOrphanEvents can attach it to the row.
//
// Exported so the writers outside this package (the native taglib writers in
// internal/metadata, which production builds use) verify through the same
// code as WriteTagsSafe.
func (deps SafeWriteDeps) VerifyWritten(path string, want map[string][]string, bookFileID, postHash string) error {
	if !deps.ForceVerify && !config.Snapshot().VerifyAfterWrite {
		return nil
	}
	verr := verifyWrittenTags(path, want)
	if verr == nil {
		return nil
	}
	deps.recordVerifyFailure(bookFileID, postHash, verr)
	return verr
}

// recordVerifyFailure appends the FileEventObserved event carrying the diff and
// the file's post-write digest.
func (deps SafeWriteDeps) recordVerifyFailure(bookFileID, postHash string, verr *VerifyAfterWriteError) {
	rec := deps.Provenance
	if rec == nil {
		// Not a bare assertion: production's HashStore is a decorator that
		// embeds database.Store, which hides capability methods. See
		// database.AsCapability.
		rec, _ = database.AsCapability[database.FileProvenanceRecorder](deps.HashStore)
	}
	if rec == nil {
		return
	}
	sha, size := postHash, int64(0)
	if sha != "" {
		if fi, err := os.Stat(verr.Path); err == nil {
			size = fi.Size()
		}
	} else if h, n, err := fileops.ComputeFileHashAndSize(verr.Path); err == nil {
		sha, size = h, n
	}
	fileops.RecordObserved(rec, bookFileID, "tagger", verr.Path, sha, size, verr.Detail())
}
