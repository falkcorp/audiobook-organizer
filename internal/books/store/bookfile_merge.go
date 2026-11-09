// file: internal/books/store/bookfile_merge.go
// version: 1.9.4
// guid: 0f6bdcdf-d13a-46a8-90f7-5d62bbccd3a8
// last-edited: 2026-11-09

package store

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
	// bfStoredOwned fields are owned by a dedicated operation, not by any
	// caller-built row: the stored value wins on EVERY merge path, whatever the
	// incoming row carries, zero or not. It is Retired: RetireBookFiles sets it
	// and UnretireBookFiles clears it, and a scanner/iTunes/UpdateBookFile row
	// that happens to carry Retired=true (or a copy of a retired row with it
	// cleared) must neither retire a live row nor revive a retired one. A NEW
	// row cannot carry it either; see refuseRetiredInsert.
	bfStoredOwned
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
	"Retired":                        {class: bfStoredOwned}, // set by RetireBookFiles, cleared only by UnretireBookFiles; no other write may flip it either way
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
	"AcoustIDFPVersion":              preserveAudio, // stamped with AcoustIDFingerprint by the acoustid backfill via UpdateBookFile
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
	// IntroSourceHash is the per-file done marker: the hash of the bytes that
	// produced the stored transcript. derivedAudio, not derivedBytes, on purpose:
	// it must live and die with the transcript and IntroRunOpID it describes
	// (clear-by-run-id and the stale-write guard read all three). A bytes-only
	// change (a tag rewrite) keeps all three plus the transcript fields, so the
	// marker no longer equals FileHash but the file is still DONE under the
	// legacy rule in IntroResultPresent (IntroTranscribedAt set and status
	// ok/unparsed/empty): selection does NOT re-transcribe it, because the
	// audio, and so the transcript, is unchanged. Changed audio drops the
	// marker, the run id and the transcript together, and the file is
	// transcribed again. Dropping the marker alone on any byte change would
	// leave a transcript with no provenance, indistinguishable from "never
	// done".
	"IntroSourceHash": preserveAudio,
	"IntroRunOpID":    preserveAudio, // what clear-by-run-id selects on; a rescan must not orphan the row from its run
	// Timeout strikes describe the audio's behaviour in the decoder, so like the
	// transcript they live and die with the audio: kept across a tag rewrite,
	// dropped when the audio changes (and IntroTimeoutHash no longer matches).
	"IntroTimeouts":    preserveAudio,
	"IntroTimeoutHash": preserveAudio,
	// The run that counted the latest strike; it travels with the strikes.
	"IntroTimeoutRunID": preserveAudio,
	"IntroTimeoutAt":    preserveAudio, // when that strike was counted; travels with the strikes
	// The worker that gave that strike; travels with the strikes.
	"IntroTimeoutWorker": preserveAudio,
	"IntroMeta":          {class: bfPreserveAlways, derived: derivedAudio}, // memdb-stripped
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
	inSec, inKnown := incoming.DurationSec()
	storedSec, storedKnown := stored.DurationSec()
	durationKnown := inKnown && storedKnown
	codecDiffers := incoming.Codec != "" && stored.Codec != "" && !strings.EqualFold(incoming.Codec, stored.Codec)
	v.audioChanged = !durationKnown || absInt(inSec-storedSec) > 1 || codecDiffers
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
// value always wins, except for a frozen bfWriteOnce pair and the bfStoredOwned
// fields, where the stored value always wins. Identity fields are left to the caller. It mutates
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
	incomingHadPrint := len(incoming.AcoustIDFingerprint) > 0
	// Whether the incoming row measured the duration itself, before the loop
	// restores the stored one into it; the audio-changed reset below needs it.
	// The Scan the caller supplied, before the loop below restores the whole
	// stored one into a wholly-zero Scan: which stages the caller itself set is
	// what the field-wise restore needs (restoreScanFields).
	incomingScan := incoming.Scan
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
		case bfStoredOwned:
			in.Field(i).Set(st.Field(i))
			continue
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
	// The loop above restores Scan only when the incoming one is wholly zero, so
	// a row that sets one stage (the duration probe's DurationOK) would replace
	// the stored NeedsDeep, HashStale, Attempts and every other stage with
	// zeros. On an upsert Scan is therefore restored field by field instead
	// (restoreScanFields), from the Scan the caller supplied.
	if upsert {
		// A Done record with no Duration to describe is no record at all: a
		// read-modify-write copy of the stored row whose Duration the audio
		// change dropped. The caller did not measure the replacement, so it
		// has not "set" the stage. (A failure record, Attempts > 0, is the
		// caller's own however it compares with the stored one.)
		if v.audioChanged && incoming.Duration == nil && incomingScan.DurationOK.Done {
			incomingScan.DurationOK = StageStatus{}
		}
		// The other audio-derived stages (the fingerprints) are dropped the same
		// way when the caller merely carried the stored value over: a stage equal
		// to the stored one, on a row whose audio changed, is a copy of the old
		// file's record, not a measurement of the new one. (A stage that differs
		// from the stored one is the caller's own and stays.) DurationOK is NOT
		// treated this way: a failure record equal to the stored one is
		// indistinguishable from a replacement that failed in exactly the same
		// way, and a fresh failure must not be wiped (see restoreScanFields).
		if v.audioChanged {
			dropCarriedOverAudioStages(&incomingScan, &stored.Scan)
		}
		incoming.Scan = incomingScan
		restoreScanFields(&incoming.Scan, &stored.Scan, v)
	}
	// A Duration and its stage record must agree: see normalizeDurationStage.
	normalizeDurationStage(incoming)
	// The whole-file print and its encoding version are ONE unit: the version
	// must describe the bytes actually written. Per-field preservation could
	// pair restored stored bytes with the caller's version (or the caller's
	// bytes with the stored version) — e.g. legacy bytes under version 1,
	// garbage certified as current. Take the version from whichever side the
	// bytes came from; with no bytes on either side, keep what the merge chose.
	if !incomingHadPrint && len(incoming.AcoustIDFingerprint) > 0 {
		incoming.AcoustIDFPVersion = stored.AcoustIDFPVersion
	}
	if upsert && presence == presenceSeen {
		incoming.Missing = false
	}
	return v
}

// scanStateFieldClasses says what each ScanState field is derived from, which
// decides when restoreScanFields stops restoring it on an upsert that carries
// different content, exactly as bookFileFieldClasses.derived does for BookFile's
// own fields. TestScanStateFieldClasses_EveryFieldClassified fails naming any
// field missing here, so a stage added to ScanState must say what invalidates it.
//
//   - derivedNone: pipeline state about the row (what the deep pass still owes,
//     how often it failed). Kept across any content change.
//   - derivedBytes: describes the bytes as a whole; dropped when the hash changed.
//   - derivedAudio: describes the decoded audio (duration, fingerprints); dropped
//     when the audio changed, as Duration and AcoustIDFingerprint are.
var scanStateFieldClasses = map[string]bookFileDerivation{
	"NeedsDeep": derivedNone,
	"HashStale": derivedNone, // is itself the "bytes changed" marker
	"HeaderBad": derivedNone,
	"Attempts":  derivedNone,
	"LastError": derivedNone,
	// Tagged is the tag header of the bytes it was read from, so it will want
	// derivedBytes the day a writer sets it; nothing does yet, and the choice is
	// made with that writer, not before it.
	"Tagged":     derivedNone,
	"DurationOK": derivedAudio,
	"FPWindowed": derivedAudio,
	"FPFull":     derivedAudio,
}

var scanStateDerivationByIndex = func() []bookFileDerivation {
	t := reflect.TypeFor[ScanState]()
	out := make([]bookFileDerivation, t.NumField())
	for i := range out {
		out[i] = scanStateFieldClasses[t.Field(i).Name] // unclassified: derivedNone, and the test fails
	}
	return out
}()

// restoreScanFields fills the ScanState fields the incoming row did not set
// from the stored row, on an upsert. A zero sub-field is "not provided",
// exactly as a zero top-level field is. A field the caller DID set wins whole,
// so a caller that carries the stored attempt count forward and adds one
// (RecordDurationProbeFailure) accumulates; the merge never sums attempts
// itself, because a fresh failure is indistinguishable here from a copy that
// merely carries the stored count. Whether the caller set the field is read
// from the row as the caller built it (the incoming argument), never inferred
// by comparing it with the stored value: a replaced file that fails in exactly
// the way the old file did is still a fresh record.
//
// A field whose derivation fired (derivedBytes on a changed hash, derivedAudio
// on changed audio) is NOT restored, so the replacement starts that stage clean.
//
// The rule that follows from "zero means not provided": an upsert cannot clear
// a Scan sub-field. A clear that must land goes through UpdateBookFile (the
// full-row write; ScanState is not restored there) or PatchBookFileFields. A
// stage added to ScanState needs its row in scanStateFieldClasses, which is what
// decides how it is invalidated; this function needs no change for it.
func restoreScanFields(incoming, stored *ScanState, v bookFileContentVerdict) {
	in := reflect.ValueOf(incoming).Elem()
	st := reflect.ValueOf(stored).Elem()
	for i, derived := range scanStateDerivationByIndex {
		if (derived == derivedBytes && v.bytesChanged) || (derived == derivedAudio && v.audioChanged) {
			continue
		}
		if f := in.Field(i); f.IsZero() {
			f.Set(st.Field(i))
		}
	}
}

// normalizeDurationStage makes a row's Duration and its DurationOK record agree.
// Known rows carry DurationOK{Done:true} and nothing else: attempts and an error
// beside a value would be a failure record for a probe that has since succeeded.
// A row with no Duration is not Done: Done beside nil would read as "measured"
// with nothing measured (a Done record that survived from a row whose Duration
// has since been dropped). Both are reachable: an upsert that carries a Duration
// but no stage, a row the scanner seeded with the stored failure, probed, and
// whose stored Duration the merge then kept because the audio did not change, and
// a copy of a stored row whose Duration was cleared.
//
// It runs wherever a row is written: at the tail of the merge (every path that
// merges with a stored row) and in the two create paths, which have nothing to
// merge with. It must run after normalizeBookFileDuration, which can turn a
// non-positive Duration into nil.
func normalizeDurationStage(f *BookFile) {
	if f == nil {
		return
	}
	if f.Duration != nil {
		f.Scan.DurationOK = StageStatus{Done: true}
		return
	}
	f.Scan.DurationOK.Done = false
}

// dropCarriedOverAudioStages zeroes, in the Scan a caller supplied, each
// audio-derived stage other than DurationOK that is identical to the stored
// row's: on a row whose audio changed it describes the old file. Which stages
// are audio-derived comes from scanStateFieldClasses.
func dropCarriedOverAudioStages(incoming, stored *ScanState) {
	in := reflect.ValueOf(incoming).Elem()
	st := reflect.ValueOf(stored).Elem()
	t := in.Type()
	for i, derived := range scanStateDerivationByIndex {
		if derived != derivedAudio || t.Field(i).Name == "DurationOK" {
			continue
		}
		if f := in.Field(i); !f.IsZero() && f.Equal(st.Field(i)) {
			f.SetZero()
		}
	}
}
