<!-- file: changelog.d/20260821_070000_file_provenance_ledger.md -->
<!-- version: 1.0.0 -->
<!-- guid: ca136761-ee50-4278-bc01-94e2b0c93f2e -->
<!-- last-edited: 2026-08-21 -->

### Added

#### An append-only record of every hash a file has ever had

The database kept exactly two hash slots per file, `original_file_hash` and
`post_metadata_hash`, and `WriteTagsSafe` overwrote both on every tag write.
The history was therefore destroyed as fast as it was made: after two writes
the first pair was simply gone, and nothing recorded that it had existed.

That is not a hypothetical loss. An attempt to recover books whose tags had
been stripped failed on exactly this — the stored hash had been recorded
*after* the damage, so it described the damaged copy, and there was no earlier
value to compare against. A two-slot column can only ever answer "what is it
now", never "what was it before".

Files now carry a provenance chain: an append-only sequence of events, each
recording what happened and every fingerprint the file had at that moment.
Nothing in the chain is ever updated or deleted.

The record is a *set* of digests rather than one hash, because no single
fingerprint answers every question:

- `sha256_full` — SHA-256 over the whole file. Proves two paths hold identical
  bytes right now. Changes on any tag write, by construction.
- `sha256_chunk` — the scanner's cheap variant (first 10MB ‖ last 10MB ‖ size
  above 100MB), so existing `file_hash` rows reconcile without rehashing.
- `size_bytes`, `duration_sec` — cheap, always available, and the first-pass
  key for matching across a mutation.
- `audio_md5`, `acoustid_seg0` — digests of the decoded audio, unchanged by
  any tag edit. These are what make a file identifiable across its own history.
- `torrent_hash` — the Deluge infohash of the source release. It identifies
  where the bytes came from rather than what they currently are, so it survives
  every local mutation and is often the only remaining link to a pristine
  original.

Three properties are load-bearing, and each is pinned by a test that fails when
the property is removed:

**The chain keys on `book_file_id`, not on a hash.** Keying on content would
mint a new identity on every tag write and chain nothing to anything. Files seen
before they have a row are recorded as orphans keyed by full hash, and adopted
into the chain on import, so a file observed outside the library and then
imported reads back as one continuous history.

**The pre-change state is recorded before the mutation, not after.** A ledger
written after the write loses precisely the crash it exists to explain. A failed
tag write now still leaves the prior state on record.

**Two events in the same nanosecond both survive.** The timestamp-keyed layout
this follows would otherwise silently overwrite one, and a ledger that loses
entries is worse than none, because it still reads as authoritative.

Looking a hash up returns the events that carried it, whether or not the file
still has it — which is the thing the two columns could never do.

### Fixed

#### A failed hash-column update is no longer silent

`WriteTagsSafe` ended with `_ = opts.Store.UpdateBookFileHashes(...)`. A
failure there let the columns drift away from the files on disk with no record
anywhere. It still cannot fail the write — the bytes are already committed, and
returning an error would invite a retry that writes the file twice — but it is
now logged with the file and row it concerns, and the ledger holds the digests
regardless.
