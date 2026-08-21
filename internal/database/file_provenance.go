// file: internal/database/file_provenance.go
// version: 1.0.0
// guid: 7c1f4a92-3d6b-4e08-9a5c-1b2e8f0d4a37
// last-edited: 2026-08-21

package database

import "time"

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
