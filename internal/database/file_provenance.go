// file: internal/database/file_provenance.go
// version: 1.1.0
// guid: 7c1f4a92-3d6b-4e08-9a5c-1b2e8f0d4a37
// last-edited: 2026-08-21

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FileEventKind names what happened to a file at one point in its life.
//
// The set is deliberately small and closed. A kind is added only when the
// event is something we can actually observe at a chokepoint in the code —
// a kind nothing emits is a lie in the schema.
type FileEventKind string

const (
	// FileEventObserved records a file's digests without claiming anything
	// changed. Emitted the first time we see a file and immediately before a
	// mutation, so the pre-change state is durable even if the process dies
	// mid-write.
	FileEventObserved FileEventKind = "observed"
	// FileEventImported records a file entering the library from outside it.
	FileEventImported FileEventKind = "imported"
	// FileEventTagsWritten records a completed tag write.
	FileEventTagsWritten FileEventKind = "tags_written"
	// FileEventMoved records a path change with byte-identical content.
	FileEventMoved FileEventKind = "moved"
	// FileEventCopied records a duplication of the bytes to a new path.
	FileEventCopied FileEventKind = "copied"
	// FileEventMerged records the file being folded into another book.
	FileEventMerged FileEventKind = "merged"
)

// FileDigest is every fingerprint we hold for a file at one instant.
//
// It is a set rather than a single hash on purpose. The two SHA fields both
// change whenever tags change — they identify exact bytes, which is what makes
// them useless for tracking a file ACROSS a tag write and exactly right for
// proving two paths hold the same bytes right now. The tag-independent fields
// (AudioMD5, AcoustIDSeg0, TorrentHash) are the ones that survive a tag write
// and can re-link a file to its own past.
//
// Every field is optional. A digest with only SizeBytes set is still worth
// recording: it is cheap, it is true, and it narrows a later search.
type FileDigest struct {
	// SHA256Full is a SHA-256 over the entire file, matching
	// fileops.ComputeFileHash. This is the field the codebase was missing:
	// original_file_hash holds one, but only for files that happened to go
	// through a tag write, and each write overwrote the previous value.
	SHA256Full string `json:"sha256_full,omitempty"`
	// SHA256Chunk is the scanner's cheap variant — for files over 100MB,
	// SHA-256(first 10MB ‖ last 10MB ‖ size); a full-file digest below that
	// threshold. Matches scanner.ComputeFileHash and book_files.file_hash, so
	// existing rows can be reconciled against the ledger without rehashing.
	SHA256Chunk string `json:"sha256_chunk,omitempty"`
	// SizeBytes is the file size. Cheap, always available, and the first-pass
	// key for tag-independent matching.
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// AudioMD5 digests the DECODED audio stream, so it is unchanged by any tag
	// edit. This is the field that makes cross-mutation identity possible.
	AudioMD5 string `json:"audio_md5,omitempty"`
	// AcoustIDSeg0 is the chromaprint fingerprint of the first segment. Like
	// AudioMD5 it survives tag writes, and unlike it, it survives re-encoding.
	AcoustIDSeg0 string `json:"acoustid_seg0,omitempty"`
	// DurationSec is the decoded duration. Pairs with SizeBytes to make a
	// tag-independent match far more selective than size alone.
	DurationSec float64 `json:"duration_sec,omitempty"`
	// TorrentHash is the Deluge infohash of the release this file came from.
	// It identifies the SOURCE rather than the bytes, so it is stable across
	// every local mutation and is often the only link back to the pristine
	// original. Mirrors BookFile.DelugeHash.
	TorrentHash string `json:"torrent_hash,omitempty"`
}

// IsZero reports whether the digest carries no information at all.
func (d FileDigest) IsZero() bool {
	return d.SHA256Full == "" && d.SHA256Chunk == "" && d.SizeBytes == 0 &&
		d.AudioMD5 == "" && d.AcoustIDSeg0 == "" && d.DurationSec == 0 &&
		d.TorrentHash == ""
}

// FileEvent is one append-only entry in a file's provenance chain.
//
// Events are never updated or deleted. Reading a file's chain in order tells
// you every digest it has ever had and what caused each change, which is what
// makes a hash from any point in the past still resolvable to a live file.
type FileEvent struct {
	// BookFileID is the stable in-database identity. It survives tag writes,
	// which is why the chain keys on it rather than on a hash. Empty for a
	// file observed before it has a row — see FileProvenanceStore.
	BookFileID string `json:"book_file_id,omitempty"`
	// Path is where the file was when the event happened.
	Path string `json:"path"`
	// Kind is what happened.
	Kind FileEventKind `json:"kind"`
	// At is when it happened, and orders the chain.
	At time.Time `json:"at"`
	// Digest is the file's fingerprints AS OF this event. For a mutation the
	// digest is the post-change state; the pre-change state is the preceding
	// observed event.
	Digest FileDigest `json:"digest"`
	// Detail is a human-readable note about the change, e.g.
	// `author: "" -> "Brandon Sanderson"`. Free-form by design; the schema
	// should not have to grow a field for every kind of edit.
	Detail string `json:"detail,omitempty"`
	// Actor names what caused the event — an op name, a plugin, a username.
	Actor string `json:"actor,omitempty"`

	// Seq is a store-wide monotonic sequence number assigned at append time,
	// in the same batch as the event itself.
	//
	// It does two jobs a per-chain link cannot. It gives the export a cursor
	// that is correct in the presence of AdoptOrphanEvents — which preserves
	// an event's original At, so an adopted event is written NOW with an OLD
	// timestamp and any time-watermark cursor would skip it forever. And a
	// GAP in the sequence proves rows were deleted wholesale, including the
	// deletion of an entire file's chain, which a chain link can never notice
	// because the evidence goes with it.
	//
	// Zero means the event predates this field. See VerifyFileChain.
	Seq uint64 `json:"seq,omitempty"`
	// PrevHash is the Hash of the preceding event in this file's chain, or
	// empty for the first event (or where the predecessor is itself
	// unchained).
	PrevHash string `json:"prev_hash,omitempty"`
	// Hash is this event's canonical digest, covering its content, its Seq and
	// its PrevHash. Stored rather than derived so a verifier can tell an
	// in-place edit of the row apart from a broken link.
	//
	// Empty means the event was written before chaining existed. That is a
	// legitimate state, NOT tampering — see VerifyFileChain.
	Hash string `json:"hash,omitempty"`
}

// fileEventCanonicalVersion prefixes the canonical encoding so a future field
// can be added without retroactively invalidating every historical hash: a
// verifier that knows v1 keeps validating v1 rows byte-for-byte.
const fileEventCanonicalVersion = "v1"

// canonicalBytes renders the event as a deterministic byte string.
//
// This is deliberately NOT json.Marshal. Go does not promise struct field
// order forever, a JSON tag rename would silently invalidate every stored
// hash, and map iteration order inside any future field would be fatal. Each
// field is length-prefixed so that concatenating "ab"+"c" cannot collide with
// "a"+"bc" — without that, a hash chain is trivially forgeable by moving
// characters across a field boundary.
//
// Hash itself is excluded, for the obvious reason. Seq and PrevHash are
// INCLUDED, so renumbering or re-pointing an event breaks its own digest and
// not merely its neighbour's link.
func (e FileEvent) canonicalBytes() []byte {
	var b strings.Builder
	w := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	w(fileEventCanonicalVersion)
	w(e.BookFileID)
	w(e.Path)
	w(string(e.Kind))
	w(strconv.FormatInt(e.At.UTC().UnixNano(), 10))
	w(e.Digest.SHA256Full)
	w(e.Digest.SHA256Chunk)
	w(strconv.FormatInt(e.Digest.SizeBytes, 10))
	w(e.Digest.AudioMD5)
	w(e.Digest.AcoustIDSeg0)
	// 'g' with precision -1 is the shortest representation that round-trips,
	// so a duration read back from JSON re-encodes to the same bytes.
	w(strconv.FormatFloat(e.Digest.DurationSec, 'g', -1, 64))
	w(e.Digest.TorrentHash)
	w(e.Detail)
	w(e.Actor)
	w(strconv.FormatUint(e.Seq, 10))
	w(e.PrevHash)
	return []byte(b.String())
}

// ComputeHash returns the canonical digest of the event as it currently
// stands. Callers set Seq and PrevHash first; the store does this for them.
func (e FileEvent) ComputeHash() string {
	sum := sha256.Sum256(e.canonicalBytes())
	return hex.EncodeToString(sum[:])
}

// ChainVerdict is the outcome of verifying one file's chain.
type ChainVerdict string

const (
	// ChainOK means every chained event verified and every link held.
	ChainOK ChainVerdict = "ok"
	// ChainUnchained means the chain carries no hash data at all — every
	// event predates chaining. This is a legitimate state for a ledger that
	// was written before this feature shipped, and must never be reported as
	// tampering: the first verify run after deploy would otherwise flag the
	// entire existing library.
	ChainUnchained ChainVerdict = "unchained"
	// ChainBroken means a stored hash disagrees with its content, or a link
	// does not match its predecessor. Corruption or rewriting.
	ChainBroken ChainVerdict = "broken"
)

// ChainReport is the result of verifying a single file's chain.
type ChainReport struct {
	Verdict ChainVerdict `json:"verdict"`
	// Events is how many events were examined.
	Events int `json:"events"`
	// Chained is how many carried hash data.
	Chained int `json:"chained"`
	// Unchained is how many predate chaining. Counted, never an error.
	Unchained int `json:"unchained"`
	// Problems describes each failure, most useful first.
	Problems []string `json:"problems,omitempty"`
}

// VerifyFileChain checks a file's event chain.
//
// The chain records APPEND order, not event time. Those differ: an event's At
// describes when something happened in the world, and callers legitimately
// append out of order (a pre-write observation recorded after the fact, an
// orphan adopted into the middle of a chain). Linking by timestamp would fork
// the chain on every such write and report honest code as tampering, so the
// link follows Seq and this function sorts by Seq before walking. Callers can
// pass GetFileHistory's output directly — it is in time order, and gets
// re-sorted here.
//
// It distinguishes THREE states rather than two. An event with no Hash was
// written before chaining existed; it is skipped, counted, and breaks the link
// for its successor without being an error itself. Conflating "no chain data"
// with "chain broken" would make every pre-existing row read as tampering,
// which is the fastest way to teach an operator to ignore the verifier.
//
// Sequence gaps are deliberately NOT checked here. A single file's events are
// not contiguous in the store-wide sequence — every other file's events fall
// between them — so gap detection is only meaningful over a global scan.
func VerifyFileChain(events []FileEvent) ChainReport {
	rep := ChainReport{Verdict: ChainOK, Events: len(events)}

	// Copy before sorting: a verifier that reorders its caller's slice is a
	// nasty surprise for anything that reads the history afterwards.
	ordered := append([]FileEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	// The Hash of the previous event, or empty when there is no verifiable
	// predecessor — either the chain start, or an unchained event, which
	// cannot vouch for what follows it.
	var prevHash string
	for i, e := range ordered {
		if e.Hash == "" {
			rep.Unchained++
			prevHash = ""
			continue
		}
		rep.Chained++
		if got := e.ComputeHash(); got != e.Hash {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("event %d (seq %d): content does not match its stored hash", i, e.Seq))
		}
		if e.PrevHash != prevHash {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("event %d (seq %d): prev_hash does not match the preceding event", i, e.Seq))
		}
		prevHash = e.Hash
	}

	switch {
	case len(rep.Problems) > 0:
		rep.Verdict = ChainBroken
	case rep.Chained == 0:
		rep.Verdict = ChainUnchained
	}
	return rep
}

// FileProvenanceStore is the append-only record of what has happened to files.
//
// It exists because the codebase previously kept exactly two hash slots per
// file (original_file_hash, post_metadata_hash) and overwrote them on every
// tag write, so the history was destroyed as it was made. Recovering a
// stripped file later needs the chain, not the last pair.
type FileProvenanceStore interface {
	// AppendFileEvent durably records one event. Callers should append the
	// pre-change observation BEFORE mutating a file, so a crash mid-write
	// still leaves the prior state recorded.
	//
	// An event with an empty BookFileID is an orphan observation — a file seen
	// outside the library before it has a row. It is indexed by digest so that
	// AdoptOrphanEvents can later attach it to the row it becomes.
	AppendFileEvent(e FileEvent) error
	// GetFileHistory returns every event for a book_file, oldest first.
	GetFileHistory(bookFileID string) ([]FileEvent, error)
	// FindFileEventsByHash resolves ANY historical SHA — full or chunked — to
	// the events that carried it. This is the payoff of keeping the chain: a
	// hash recorded before a tag write still finds the file afterwards, which
	// a two-slot column can never do.
	FindFileEventsByHash(hash string) ([]FileEvent, error)
	// AdoptOrphanEvents attaches previously orphaned observations to a
	// book_file row once the file is imported, matching on full-file SHA. It
	// returns the number of events adopted.
	AdoptOrphanEvents(bookFileID, sha256Full string) (int, error)

	// --- store-wide sequence, for export and gap detection ---
	//
	// These live on the same interface rather than a separate one because they
	// are properties of the ledger's storage, not a different concern: the
	// sequence is assigned by AppendFileEvent in the same batch as the event,
	// and splitting the reader off would let a caller hold one half without
	// the other. There is exactly one implementation.

	// MaxFileEventSeq returns the highest sequence number handed out, 0 if none.
	MaxFileEventSeq() (uint64, error)
	// ScanFileEventsBySeq returns events in store-wide sequence order starting
	// after afterSeq, capped at limit (0 = uncapped). A sequence slot whose
	// event is missing comes back with a zero Event rather than being skipped:
	// a dangling slot is the evidence that a row was deleted, and hiding it
	// would defeat the point of keeping the index.
	ScanFileEventsBySeq(afterSeq uint64, limit int) ([]FileEventSeqRow, error)
	// GetFileProvExportCursor returns the highest sequence already exported.
	GetFileProvExportCursor() (uint64, error)
	// SetFileProvExportCursor advances the export cursor. It must never move
	// backwards — an append-only file cannot un-write what a rewind would
	// cause it to duplicate.
	SetFileProvExportCursor(seq uint64) error
}

// FileProvenanceRecorder is the write half of FileProvenanceStore, narrowed for
// callers that only append.
//
// It is deliberately separate and NOT embedded in the big Store interface.
// fileops already depends on the tiny BookFileHashUpdater rather than Store for
// the same reason: a package that writes one kind of record should not have to
// know about the other two hundred methods, and widening Store forces a
// regeneration of a 13k-line mockery mock for every consumer.
type FileProvenanceRecorder interface {
	AppendFileEvent(e FileEvent) error
}
