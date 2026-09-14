// file: internal/database/bookfile_merge.go
// version: 1.3.0
// guid: 0f6bdcdf-d13a-46a8-90f7-5d62bbccd3a8
// last-edited: 2026-09-13

package database

import (
	"reflect"
	"strings"
)

// FileHashKindSampled is BookFile.OriginalFileHashKind for a digest computed by
// filehash.BookFileHash (head + tail chunks + size for large files), the same
// digest BookFile.FileHash holds. It is the only kind OriginalFileHash may be
// frozen as: a whole-file SHA-256 of the same bytes is a different string for
// any file above filehash's threshold, so comparing the two kinds reports a
// change that never happened.
const FileHashKindSampled = "bookfilehash"

// bookFileFieldClass says how the book_file write paths (UpdateBookFile,
// UpsertBookFile, BatchUpsertBookFiles / BatchUpsertScannedBookFiles) treat one
// BookFile field when the incoming row carries its zero value and a stored row
// already exists.
//
// Why this exists: the library scanner hands the batch upsert a BARE row (ID,
// BookID, FilePath, OriginalFilename, Format, FileSize, TrackNumber, plus
// RawTags/Title/hashes when it can read them). The upsert paths used to restore
// only identity, the fingerprint fields and IntroTranscription from the stored
// row, so every rescan zeroed 40 fields nothing on the scan path owns —
// transcription state, Duration, codec/bitrate info, Missing, SkipScan, iTunes
// and Deluge provenance, the scan cache, fingerprint failure state. The iTunes
// sync builds bare rows the same way. Measured by
// TestUpsertBookFile_ScannerRescanPreservesUnownedFields on main before this fix.
//
// Every BookFile field MUST appear in bookFileFieldClasses.
// TestBookFileFieldClasses_EveryFieldClassified fails naming any field that is
// missing, so a new field cannot silently default into a wipe or a preserve.
//
// PatchBookFileFields (pebble_store_bookfile_patch.go) is deliberately OUTSIDE
// this merge. It is a field-level setter: it re-reads the stored row, sets only
// the fields it names (TrackNumber, DiscNumber, SkipScan, DownloadHash), zero
// included, and writes that row back. There is no partial incoming row to merge,
// so it never calls mergeBookFileFromStored, and it is the way to clear
// SkipScan or DownloadHash on purpose.
type bookFileFieldClass uint8

const (
	// bfIdentity fields are set by each write path itself (ID, BookID and
	// CreatedAt from the stored row on an upsert; UpdatedAt from the clock). The
	// merge never touches them.
	bfIdentity bookFileFieldClass = iota + 1
	// bfNotStored fields are dropped by marshalBookFileDropSegs on every write,
	// so there is nothing stored to preserve.
	bfNotStored
	// bfUpsertOwned fields are written exactly as the incoming row carries them
	// on every path, zero included. A rescan observes them and its zero is a
	// real observation: a disc placement the scanner no longer trusts is cleared
	// to 0 (metadata.ApplyTagPlacement), and keeping the stored disc would plan
	// two files onto one target.
	bfUpsertOwned
	// bfPreserveOnUpsert fields keep the stored value when an UPSERT row carries
	// zero/nil, because upsert callers build partial rows (the scanner, the
	// iTunes sync). UpdateBookFile writes them exactly as given: it is the
	// full-row read-modify-write path used by the intentional clears (repoint
	// and mark-missing flip Missing, acoustid backfill clears the fingerprint
	// failure fields).
	bfPreserveOnUpsert
	// bfPreserveAlways fields keep the stored value on EVERY path when the
	// incoming value is zero. They are the ones stripBookFileForMemdb nils, so a
	// slim memdb round-trip can never carry them and a zero there means "not
	// loaded", never "clear". The one exception is an upsert whose audio
	// changed (see derivedAudio), which drops them.
	bfPreserveAlways
	// bfScanObserved is bfPreserveOnUpsert, except on a row the scanner has just
	// stat'd successfully (BatchUpsertScannedBookFiles with Present=true), where
	// the incoming zero is written. It exists for Missing: the scanner found the
	// file, so it must be able to clear the flag, while the iTunes sync — which
	// never looks at the disk — must not.
	bfScanObserved
	// bfWriteOnce is OriginalFileHash and its OriginalFileHashKind: the digest
	// of the file as first seen. When the stored pair is a known-kind digest
	// (kind FileHashKindSampled), it is kept on EVERY path, whatever the
	// incoming row carries: the scanner sets OriginalFileHash to the CURRENT
	// hash on every row it builds, so without this the first-seen identity
	// would be replaced by the latest one. When the stored kind is anything
	// else (legacy rows: a whole-file SHA-256 from a tag write, or a value of
	// no recorded kind), the pair behaves as bfPreserveOnUpsert, so the next
	// scan replaces it with a sampled digest and it is frozen from then on.
	bfWriteOnce
)

// bookFileDerivation says what a field was computed from, which decides when an
// upsert that carries different bytes (a changed FileHash) stops restoring it.
type bookFileDerivation uint8

const (
	// derivedNone: identity, provenance or state (Missing, SkipScan, iTunes and
	// Deluge fields, VersionID, OrganizeMethod, ...). Kept across any change.
	derivedNone bookFileDerivation = iota
	// derivedBytes: computed from the file's bytes as a whole — its tags, size,
	// scan-cache keys and post-write hash. They are dropped whenever the hash
	// changed.
	derivedBytes
	// derivedAudio: computed from the decoded audio — duration, codec and
	// stream info, fingerprint and its failure state, the online match, the
	// intro transcript and transcription state. On a changed hash they are
	// KEPT only when the incoming row proves the audio is the same: both rows
	// carry a Duration and they agree within one second, and the codecs do not
	// differ where both are known. Anything else — a different duration, a
	// different codec, or no duration to compare — drops them. Our own tag
	// writes no longer change FileHash (WriteTagsSafe records the new digest),
	// so a changed hash is a file replaced or rewritten outside the app, and
	// keeping an old recording's fingerprint would feed it to dedup and
	// content matching. The scanner probes the duration of exactly these
	// files (probeReplacedFileAudio) so the comparison has something to use.
	derivedAudio
)

// bookFileFieldRule is one row of the classification table.
type bookFileFieldRule struct {
	class   bookFileFieldClass
	derived bookFileDerivation
}

// bookFileWriteMode selects which classes the merge restores.
type bookFileWriteMode uint8

const (
	// bookFileWriteReplace is UpdateBookFile: the caller supplies the full row.
	bookFileWriteReplace bookFileWriteMode = iota + 1
	// bookFileWriteUpsert is UpsertBookFile / BatchUpsertBookFiles: the caller
	// may supply a partial row.
	bookFileWriteUpsert
)

// scanPresence is what the caller knows about the file on disk.
type scanPresence uint8

const (
	// presenceNotScanned: the caller did not stat the file (the iTunes sync,
	// UpsertBookFile, UpdateBookFile). Missing is kept; a changed hash is judged.
	presenceNotScanned scanPresence = iota
	// presenceSeen: the scanner's stat succeeded. Missing=false is written.
	presenceSeen
	// presenceNotSeen: the scanner's stat failed. Missing is kept, and a changed
	// hash is NOT acted on — the scanner could not look at the file, so it has
	// no evidence the stored data is stale.
	presenceNotSeen
)

// ScannedBookFile is one row for BatchUpsertScannedBookFiles. Present is true
// only when the scanner's own os.Stat of File.FilePath succeeded during this
// scan; it is what licenses the upsert to write Missing=false.
type ScannedBookFile struct {
	File    *BookFile
	Present bool
}

// scanPresenceOf maps batchUpsertBookFiles' optional presence slice to a
// scanPresence for row i. A nil slice means no row was stat'd.
func scanPresenceOf(present []bool, i int) scanPresence {
	switch {
	case present == nil:
		return presenceNotScanned
	case present[i]:
		return presenceSeen
	default:
		return presenceNotSeen
	}
}

var (
	preserve      = bookFileFieldRule{class: bfPreserveOnUpsert}
	preserveBytes = bookFileFieldRule{class: bfPreserveOnUpsert, derived: derivedBytes}
	preserveAudio = bookFileFieldRule{class: bfPreserveOnUpsert, derived: derivedAudio}
)

// bookFileFieldClasses is the explicit per-field classification. Keep it in
// BookFile declaration order so a reviewer can diff it against store.go.
var bookFileFieldClasses = map[string]bookFileFieldRule{
	"ID":                             {class: bfIdentity},
	"BookID":                         {class: bfIdentity},
	"VersionID":                      preserve,
	"FilePath":                       {class: bfUpsertOwned}, // the match key; never zero on an upsert that matched by path
	"OriginalFilename":               preserve,
	"ITunesPath":                     preserve,
	"ITunesPersistentID":             preserve,
	"TrackNumber":                    {class: bfUpsertOwned},
	"TrackCount":                     preserveBytes, // tag total; ApplyTagPlacement keeps the stored count when the tag has none
	"DiscNumber":                     {class: bfUpsertOwned},
	"DiscCount":                      {class: bfUpsertOwned},
	"Title":                          preserveBytes, // scanner sets it only when the tag read succeeds
	"RawTags":                        preserveBytes, // scanner sets it only when the tag read succeeds
	"Format":                         preserve,
	"Codec":                          preserveAudio,
	"Duration":                       preserveAudio,
	"FileSize":                       preserveBytes, // a failed stat yields 0; a real 0-byte file is re-measured by the deep pass
	"BitrateKbps":                    preserveAudio,
	"SampleRateHz":                   preserveAudio,
	"Channels":                       preserveAudio,
	"BitDepth":                       preserveAudio,
	"FileHash":                       preserve, // scanner sets it only when hashing succeeds; a non-empty value always wins
	"OriginalFileHash":               {class: bfWriteOnce},
	"OriginalFileHashKind":           {class: bfWriteOnce},
	"Scan":                           preserve, // scan-pipeline state; zeroing it would read as "deep pass done"
	"LastScanMtime":                  preserveBytes,
	"LastScanSize":                   preserveBytes,
	"NeedsRescan":                    preserve, // scan-cache flag; owned by UpdateScanCache / MarkNeedsRescan
	"PostMetadataHash":               preserveBytes,
	"AcoustIDFingerprint":            {class: bfPreserveAlways, derived: derivedAudio}, // memdb-stripped
	"AcoustIDFingerprintDurationSec": preserveAudio,
	"AcoustIDSeg0":                   {class: bfNotStored},
	"AcoustIDSeg1":                   {class: bfNotStored},
	"AcoustIDSeg2":                   {class: bfNotStored},
	"AcoustIDSeg3":                   {class: bfNotStored},
	"AcoustIDSeg4":                   {class: bfNotStored},
	"AcoustIDSeg5":                   {class: bfNotStored},
	"AcoustIDSeg6":                   {class: bfNotStored},
	"FingerprintFailedAt":            preserveAudio, // acoustid backfill clears it via UpdateBookFile
	"FingerprintFailureReason":       preserveAudio, // memdb-stripped, but backfill clears it via UpdateBookFile
	"FingerprintFailureDetail":       preserveAudio, // same
	"FingerprintDiagnosticJSON":      preserveAudio, // same
	"AcoustIDOnlineRecordingID":      preserveAudio,
	"AcoustIDOnlineScore":            preserveAudio,
	"AcoustIDOnlineLookedUpAt":       preserveAudio,
	"OrganizeMethod":                 preserve,
	"Missing":                        {class: bfScanObserved}, // cleared by a successful scanner stat, or on purpose via UpdateBookFile
	"SkipScan":                       preserve,                // cleared on purpose via PatchBookFileFields (the PATCH handler)
	"CreatedAt":                      {class: bfIdentity},
	"UpdatedAt":                      {class: bfIdentity},
	"DelugeHash":                     preserve,
	"DownloadHash":                   preserve, // cleared on purpose via PatchBookFileFields
	"DelugeOriginalPath":             preserve,
	"ImportedFromDelugeAt":           preserve,
	"IntroTranscription":             {class: bfPreserveAlways, derived: derivedAudio}, // memdb-stripped
	"TranscribedTitle":               preserveAudio,
	"TranscribedAuthor":              preserveAudio,
	"TranscribedNarrator":            preserveAudio,
	"TranscribedTranslator":          preserveAudio,
	"TranscribedCoverArtist":         preserveAudio,
	"IntroTranscribedAt":             preserveAudio,
	"TranscribeStatus":               preserveAudio,
	"TranscribeError":                preserveAudio,
	"TranscribeAttemptedAt":          preserveAudio,
}

// bookFileRulesByIndex is bookFileFieldClasses resolved to BookFile struct
// field indices, built once. An unclassified field is treated as
// bfPreserveOnUpsert, not derived — the choice that can never destroy stored
// data — and the classification test fails naming it.
var bookFileRulesByIndex = buildBookFileRulesByIndex()

func buildBookFileRulesByIndex() []bookFileFieldRule {
	t := reflect.TypeFor[BookFile]()
	out := make([]bookFileFieldRule, t.NumField())
	for i := range t.NumField() {
		rule, ok := bookFileFieldClasses[t.Field(i).Name]
		if !ok {
			rule = preserve
		}
		out[i] = rule
	}
	return out
}

// bookFileContentVerdict is what an upsert row says about the file's content
// relative to the stored row.
type bookFileContentVerdict struct {
	// bytesChanged: both hashes known and unequal, on a row whose file the
	// caller did not fail to stat. derivedBytes fields are dropped.
	bytesChanged bool
	// audioChanged: bytesChanged, and the incoming row does not prove the audio
	// is the same (see derivedAudio). derivedAudio fields are dropped.
	audioChanged bool
}

// judgeBookFileContent compares an upsert row with the stored row. An empty
// hash on either side is "unknown", never "changed", and a scanned row whose
// stat failed is never judged changed.
func judgeBookFileContent(incoming, stored *BookFile, presence scanPresence) bookFileContentVerdict {
	var v bookFileContentVerdict
	if presence == presenceNotSeen || incoming.FileHash == "" || stored.FileHash == "" || incoming.FileHash == stored.FileHash {
		return v
	}
	v.bytesChanged = true
	durationKnown := incoming.Duration > 0 && stored.Duration > 0
	codecDiffers := incoming.Codec != "" && stored.Codec != "" && !strings.EqualFold(incoming.Codec, stored.Codec)
	v.audioChanged = !durationKnown || absInt(incoming.Duration-stored.Duration) > 1 || codecDiffers
	return v
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// originalHashFrozen reports whether the stored OriginalFileHash is a
// known-kind digest that bfWriteOnce must keep.
func originalHashFrozen(stored *BookFile) bool {
	return stored.OriginalFileHash != "" && stored.OriginalFileHashKind == FileHashKindSampled
}

// mergeBookFileFromStored is the one merge rule shared by the book_file write
// paths that accept a caller-built row. For every field the mode restores, a
// zero/nil incoming value is replaced by the stored value; a non-zero incoming
// value always wins, except for a frozen bfWriteOnce pair, where the stored
// value always wins. Identity fields are left to the caller. It mutates
// incoming in place, matching the write paths' existing contract.
//
// On an upsert, a changed FileHash drops derivedBytes fields, and drops
// derivedAudio fields unless the incoming row proves the audio is unchanged;
// see bookFileDerivation. presenceSeen writes Missing=false.
//
// A clear that must land goes through UpdateBookFile with the full row (only
// the memdb-stripped heavy fields and a frozen bfWriteOnce pair are restored
// there), or through PatchBookFileFields for the fields it names.
func mergeBookFileFromStored(incoming, stored *BookFile, mode bookFileWriteMode, presence scanPresence) bookFileContentVerdict {
	if incoming == nil || stored == nil || incoming == stored {
		return bookFileContentVerdict{}
	}
	upsert := mode == bookFileWriteUpsert
	var v bookFileContentVerdict
	if upsert {
		v = judgeBookFileContent(incoming, stored, presence)
	}
	frozen := originalHashFrozen(stored)
	in := reflect.ValueOf(incoming).Elem()
	st := reflect.ValueOf(stored).Elem()
	for i, rule := range bookFileRulesByIndex {
		var restore bool
		switch rule.class {
		case bfWriteOnce:
			if frozen {
				in.Field(i).Set(st.Field(i))
				continue
			}
			restore = upsert
		case bfPreserveAlways:
			restore = true
		case bfPreserveOnUpsert:
			restore = upsert
		case bfScanObserved:
			restore = upsert && presence != presenceSeen
		}
		if !restore {
			continue
		}
		if (rule.derived == derivedBytes && v.bytesChanged) || (rule.derived == derivedAudio && v.audioChanged) {
			continue
		}
		if f := in.Field(i); f.IsZero() {
			f.Set(st.Field(i))
		}
	}
	if upsert && presence == presenceSeen {
		incoming.Missing = false
	}
	return v
}
