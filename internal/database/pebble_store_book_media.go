// file: internal/database/pebble_store_book_media.go
// version: 1.0.0
// guid: 4b623e68-386f-4ecd-9e24-026f6dbf0a43
// last-edited: 2026-09-13

package database

// BookMediaInfoPatch carries media properties derived from a book's audio file
// (ffprobe / mediainfo) for FillBookMediaInfo. A nil field means "not derived";
// a non-nil field is a candidate value for a stored field that is still empty.
type BookMediaInfoPatch struct {
	Duration   *int
	Codec      *string
	Bitrate    *int
	SampleRate *int
	Channels   *int
}

// IsEmpty reports whether the patch carries no derived value at all.
func (p BookMediaInfoPatch) IsEmpty() bool {
	return p.Duration == nil && p.Codec == nil && p.Bitrate == nil &&
		p.SampleRate == nil && p.Channels == nil
}

// applyTo fills each field of book that is still nil from the patch and
// reports whether it changed anything. A field that already holds a value is
// never overwritten: a writer that set it (a metadata apply, a scan) wins.
func (p BookMediaInfoPatch) applyTo(book *Book) bool {
	changed := false
	if book.Duration == nil && p.Duration != nil {
		v := *p.Duration
		book.Duration = &v
		changed = true
	}
	if book.Codec == nil && p.Codec != nil {
		v := *p.Codec
		book.Codec = &v
		changed = true
	}
	if book.Bitrate == nil && p.Bitrate != nil {
		v := *p.Bitrate
		book.Bitrate = &v
		changed = true
	}
	if book.SampleRate == nil && p.SampleRate != nil {
		v := *p.SampleRate
		book.SampleRate = &v
		changed = true
	}
	if book.Channels == nil && p.Channels != nil {
		v := *p.Channels
		book.Channels = &v
		changed = true
	}
	return changed
}

// FillBookMediaInfo writes the patch's media fields onto book id, but only
// into fields the STORED row still has empty, and returns the stored row as it
// stands afterwards. When no field needs filling it writes nothing (no
// UpdatedAt bump, no version snapshot) and returns the row as read.
//
// It exists for read paths (GET /audiobooks/:id, the tags endpoint) that
// backfill media info from the file. Those used to call UpdateBook with the
// whole struct they had read before running ffprobe, which replaced every
// column and silently reverted any write -- a metadata apply, an edit -- that
// landed in between. Here the row is re-read inside the store and only the
// empty media fields are set on that fresh copy, so no other column can be
// carried over from a stale read.
//
// bookMediaMu serializes FillBookMediaInfo calls against each other. It does
// not serialize against UpdateBook, whose own read-then-commit is unlocked
// (see updateBookAfterOldReadHook): a full UpdateBook that commits in the few
// microseconds between this method's read and its commit can still be
// overwritten, exactly as between any two UpdateBook calls. Nothing slow
// (ffprobe, tag reads) runs inside that window any more.
func (p *PebbleStore) FillBookMediaInfo(id string, patch BookMediaInfoPatch) (*Book, error) {
	p.bookMediaMu.Lock()
	defer p.bookMediaMu.Unlock()

	fresh, err := p.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, nil
	}
	if !patch.applyTo(fresh) {
		return fresh, nil
	}
	return p.UpdateBook(id, fresh)
}
