// file: internal/server/handlers/abs/mapper.go
// version: 1.0.0
// guid: 7a2f58d1-0b64-4e93-8c1d-6f9047b5e2a3
// last-edited: 2026-07-30

package abs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"golang.org/x/sync/errgroup"
)

// itemView is everything the DTO layer needs about one book, gathered once so the
// minified, expanded and play-session shapes are all rendered from the SAME numbers.
// That is not tidiness: spec §5b requires ONE authoritative duration per book used
// consistently in media.duration, the play session and progress math, because the
// three legitimate sources disagree by ~52 ms and mixing them leaves a finished book
// stuck at 99% forever.
type itemView struct {
	Book   *database.Book
	SyncID string
	Files  []fileView

	// Chapters is the whole-book chapter timeline: the persisted chapters when the
	// scanner extracted any, otherwise one synthesized chapter per track.
	Chapters []audioutil.Chapter

	// DurationSec is THE authoritative duration: the sum of the per-file durations.
	// It matches the timeline clients seek within and reproduces real ABS's
	// startOffset values exactly. Book.Duration is only a fallback for a book with
	// no files at all.
	DurationSec float64

	// SizeBytes is the sum of the per-file sizes.
	SizeBytes int64

	Authors   []database.Author
	Narrators []string
	Series    *database.Series
}

// fileView is one BookFile plus the durable file id and the on-disk facts.
type fileView struct {
	File database.BookFile
	// SyncFileID is the value exposed as `ino`. It comes from the sync_file
	// keyspace, NEVER from a real filesystem inode: this app moves and reorganizes
	// files as its core function, and an inode is not stable across a move to
	// another filesystem or a copy-then-replace, which would break every offline
	// client's cached download URL (spec §4.2b).
	SyncFileID string
	// StartOffsetSec is the cumulative offset of this file on the whole-book
	// timeline. Unrounded.
	StartOffsetSec float64
	DurationSec    float64
	SizeBytes      int64
	BirthtimeMs    int64
	MtimeMs        int64
	CtimeMs        int64
	// Exists records whether the path resolved on disk at load time. A missing file
	// still gets a track entry, because omitting it would renumber the timeline.
	Exists bool
}

// ── loading ─────────────────────────────────────────────────────────────────

// loadItemViews gathers the per-book data for a page of books.
//
// The per-book work (file listing, chapter read, and minting two kinds of durable
// id) is real I/O, so it runs in a bounded worker pool sized to runtime.NumCPU()
// rather than a serial loop — CLAUDE.md's concurrency rule. The relation lookups in
// front of it are BATCHED (one call for all authors, one for all narrators, one for
// all series) so a page never issues a query per book for them.
//
// Order is preserved: results are written into a pre-sized slice by index, never
// appended, so concurrency cannot reorder the page.
func (h *Handler) loadItemViews(ctx context.Context, books []database.Book) ([]itemView, error) {
	if len(books) == 0 {
		return nil, nil
	}

	ids := make([]string, len(books))
	seriesIDs := make([]int, 0, len(books))
	for i := range books {
		ids[i] = books[i].ID
		if books[i].SeriesID != nil {
			seriesIDs = append(seriesIDs, *books[i].SeriesID)
		}
	}

	authorsByBook, err := h.library.GetAuthorsByBookIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load authors: %w", err)
	}
	narratorsByBook, err := h.library.GetNarratorsByBookIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load narrators: %w", err)
	}
	seriesByID := map[int]*database.Series{}
	if len(seriesIDs) > 0 {
		seriesByID, err = h.library.GetSeriesByIDs(seriesIDs)
		if err != nil {
			return nil, fmt.Errorf("load series: %w", err)
		}
	}

	out := make([]itemView, len(books))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i := range books {
		i := i
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			view, err := h.loadOneItemView(&books[i], authorsByBook[books[i].ID], narratorsByBook[books[i].ID], seriesByID)
			if err != nil {
				return err
			}
			out[i] = *view
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// loadItemView is the single-book path (GET /api/items/:id, play sessions).
func (h *Handler) loadItemView(ctx context.Context, book *database.Book) (*itemView, error) {
	views, err := h.loadItemViews(ctx, []database.Book{*book})
	if err != nil {
		return nil, err
	}
	if len(views) != 1 {
		return nil, fmt.Errorf("abs: loadItemView: expected 1 view, got %d", len(views))
	}
	return &views[0], nil
}

func (h *Handler) loadOneItemView(
	book *database.Book,
	authors []database.Author,
	narrators []database.Narrator,
	seriesByID map[int]*database.Series,
) (*itemView, error) {
	syncID, err := h.identity.MintOrGetSyncID(book.ID)
	if err != nil {
		return nil, fmt.Errorf("mint sync id for %s: %w", book.ID, err)
	}

	files, err := h.library.GetBookFiles(book.ID)
	if err != nil {
		return nil, fmt.Errorf("load files for %s: %w", book.ID, err)
	}
	// Track order is the playback timeline, so it must be deterministic and it must
	// match what the listener expects: disc, then track, then path as a last-resort
	// tiebreak for files with no numbering at all.
	sort.SliceStable(files, func(a, b int) bool {
		if files[a].DiscNumber != files[b].DiscNumber {
			return files[a].DiscNumber < files[b].DiscNumber
		}
		if files[a].TrackNumber != files[b].TrackNumber {
			return files[a].TrackNumber < files[b].TrackNumber
		}
		return files[a].FilePath < files[b].FilePath
	})

	view := &itemView{Book: book, SyncID: syncID, Authors: authors}
	for i := range files {
		fv, err := h.loadFileView(book.ID, files[i])
		if err != nil {
			return nil, err
		}
		fv.StartOffsetSec = view.DurationSec
		view.DurationSec += fv.DurationSec
		view.SizeBytes += fv.SizeBytes
		view.Files = append(view.Files, *fv)
	}
	// A book with no BookFile rows still needs a positive duration, or Absorb
	// classifies it ebook-only and hides the play button (requirement 13).
	if len(view.Files) == 0 && book.Duration != nil {
		view.DurationSec = float64(*book.Duration)
	}
	if view.SizeBytes == 0 && book.FileSize != nil {
		view.SizeBytes = *book.FileSize
	}

	view.Chapters = h.loadChapters(book.ID, view.Files)
	view.Narrators = resolveNarrators(book, narrators)
	if book.SeriesID != nil {
		view.Series = seriesByID[*book.SeriesID]
	}
	return view, nil
}

func (h *Handler) loadFileView(bookID string, f database.BookFile) (*fileView, error) {
	syncFileID, err := h.identity.MintOrGetSyncFileID(bookID, f.ID)
	if err != nil {
		return nil, fmt.Errorf("mint sync file id for %s/%s: %w", bookID, f.ID, err)
	}
	fv := &fileView{
		File:        f,
		SyncFileID:  syncFileID,
		DurationSec: float64(f.Duration),
		SizeBytes:   f.FileSize,
		BirthtimeMs: msEpoch(f.CreatedAt),
		MtimeMs:     msEpoch(f.UpdatedAt),
		CtimeMs:     msEpoch(f.UpdatedAt),
	}
	// Stat is best-effort: a missing file must not fail the whole response, because
	// one unreadable file in a 50-item page would blank the client's entire grid.
	if st, statErr := os.Stat(f.FilePath); statErr == nil {
		fv.Exists = true
		fv.SizeBytes = st.Size()
		fv.MtimeMs = st.ModTime().UnixMilli()
		if fv.CtimeMs == 0 {
			fv.CtimeMs = fv.MtimeMs
		}
	}
	return fv, nil
}

// loadChapters returns the whole-book chapter timeline.
//
// Persisted chapters (extracted by the scanner from a container's embedded markers)
// win. When there are none, one chapter per track is synthesized with cumulative
// offsets — which is exactly what real ABS does for a multi-file book, titling each
// chapter from the embedded track title rather than the filename
// (testdata/abs-fixtures/README.md item 4).
func (h *Handler) loadChapters(bookID string, files []fileView) []audioutil.Chapter {
	if h.chapters != nil {
		if stored, err := h.chapters.GetChaptersForBook(bookID); err == nil && len(stored) > 0 {
			out := make([]audioutil.Chapter, len(stored))
			for i, c := range stored {
				out[i] = audioutil.Chapter{ID: i, StartSec: c.StartSec, EndSec: c.EndSec, Title: c.Title}
			}
			return out
		}
	}
	if len(files) == 0 {
		return nil
	}
	tracks := make([]audioutil.TrackInfo, len(files))
	for i, f := range files {
		tracks[i] = audioutil.TrackInfo{
			Title:       f.File.Title,
			Filename:    filepath.Base(f.File.FilePath),
			DurationSec: f.DurationSec,
		}
	}
	return audioutil.SynthesizeChapters(tracks)
}

// resolveNarrators picks the AUTHORITATIVE narrator source.
//
// internal/database holds narrators three ways (spec §12 leaves the choice to this
// phase). The decision, and the reason:
//
//	1st  the BookNarrator junction (via GetNarratorsByBookIDs) — the normalized
//	     relational source, the only one that can express multiple narrators
//	     cleanly, and the only one with a batched getter, so a page of 50 books
//	     costs one query instead of fifty.
//	2nd  Book.NarratorsJSON — a JSON array written by the importer; used only when
//	     the junction is empty, for rows written before the junction existed.
//	3rd  Book.Narrator — the single-string legacy column, last resort.
//
// The fallbacks are read-only: nothing here writes back, so this function cannot
// change stored data. If the junction is ever backfilled from the other two, the
// fallbacks become dead code and can be deleted without touching callers.
func resolveNarrators(book *database.Book, junction []database.Narrator) []string {
	if len(junction) > 0 {
		out := make([]string, 0, len(junction))
		for _, n := range junction {
			if name := strings.TrimSpace(n.Name); name != "" {
				out = append(out, name)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if book.NarratorsJSON != nil {
		if names := parseNarratorsJSON(*book.NarratorsJSON); len(names) > 0 {
			return names
		}
	}
	if book.Narrator != nil {
		if name := strings.TrimSpace(*book.Narrator); name != "" {
			return []string{name}
		}
	}
	return nil
}

// parseNarratorsJSON tolerates both shapes the column has held: a JSON array of
// strings, and a bare comma-separated string. A parse failure yields no narrators
// rather than an error — a malformed legacy value must not fail a whole page.
func parseNarratorsJSON(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		out := make([]string, 0, len(arr))
		for _, n := range arr {
			if n = strings.TrimSpace(n); n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	var out []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// ── rendering ───────────────────────────────────────────────────────────────

// metadata renders media.metadata, emitting BOTH forms of every relation.
func (h *Handler) metadata(v *itemView) bookMetadataDTO {
	b := v.Book

	authorNames := make([]string, 0, len(v.Authors))
	authorObjs := make([]idNameDTO, 0, len(v.Authors))
	for _, a := range v.Authors {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		authorNames = append(authorNames, name)
		authorObjs = append(authorObjs, idNameDTO{ID: strconv.Itoa(a.ID), Name: name})
	}

	seriesRefs := []seriesRefDTO{}
	seriesName := ""
	if v.Series != nil {
		var seq *string
		if b.SeriesSequence != nil {
			// A STRING, never a number: §1.7.2 / §1.8.5 item 12.
			s := strconv.Itoa(*b.SeriesSequence)
			seq = &s
		}
		seriesRefs = append(seriesRefs, seriesRefDTO{
			ID: strconv.Itoa(v.Series.ID), Name: v.Series.Name, Sequence: seq,
		})
		seriesName = v.Series.Name
		if seq != nil {
			seriesName = v.Series.Name + " #" + *seq
		}
	}

	narrators := v.Narrators
	if narrators == nil {
		narrators = []string{}
	}

	title := strings.TrimSpace(b.Title)
	if title == "" {
		// §1.8.5 item 4: Book.swift:196 decodes title NON-optionally, so one null
		// title blanks the entire page. Fall back to the filename, which is what the
		// spec prescribes and abs-shim gets wrong.
		title = fallbackTitle(b, v.Files)
	}

	return bookMetadataDTO{
		Abridged:         false,
		ASIN:             b.ASIN,
		AuthorName:       strings.Join(authorNames, ", "),
		AuthorNameLF:     lastFirstJoin(authorNames),
		Authors:          authorObjs,
		Description:      b.Description,
		DescriptionPlain: b.Description,
		Explicit:         false,
		Genres:           splitList(b.Genre),
		ISBN:             firstNonEmpty(b.ISBN13, b.ISBN10),
		Language:         b.Language,
		NarratorName:     strings.Join(narrators, ", "),
		Narrators:        narrators,
		PublishedDate:    nil,
		PublishedYear:    yearString(b),
		Publisher:        b.Publisher,
		Series:           seriesRefs,
		SeriesName:       seriesName,
		// Book has no Subtitle column (spec §1). The key still has to EXIST —
		// requirement 14 — so it is emitted as an explicit null rather than dropped.
		Subtitle:          nil,
		Title:             title,
		TitleIgnorePrefix: ignorePrefix(title),
	}
}

// fallbackTitle derives a human title from the first file's name, then the book's
// own path, so `title` is never empty.
func fallbackTitle(b *database.Book, files []fileView) string {
	if len(files) > 0 && files[0].File.FilePath != "" {
		base := filepath.Base(files[0].File.FilePath)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	if b.FilePath != "" {
		base := filepath.Base(b.FilePath)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return "Untitled"
}

// yearString renders publishedYear as a STRING or null — never a number.
// §1.7.2: in Dart `as String?` on a number THROWS inside a widget build(), so a
// numeric year red-screens the book detail sheet outright.
func yearString(b *database.Book) *string {
	year := b.AudiobookReleaseYear
	if year == nil {
		year = b.PrintYear
	}
	if year == nil || *year == 0 {
		return nil
	}
	s := strconv.Itoa(*year)
	return &s
}

// lastFirstJoin renders the "Last, First" author form ABS calls authorNameLF. The
// oracle turns "transl. Samuel Butler Homer" into "Homer, transl. Samuel Butler",
// i.e. the final whitespace-separated token moves to the front.
func lastFirstJoin(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, lastFirst(n))
	}
	return strings.Join(out, ", ")
}

func lastFirst(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	last := parts[len(parts)-1]
	return last + ", " + strings.Join(parts[:len(parts)-1], " ")
}

// ignorePrefixes are the leading articles ABS moves to the end for sorting. Kept in
// sync with serverSettings.sortingPrefixes, which advertises the same list.
var ignorePrefixes = []string{"the ", "a ", "an "}

// ignorePrefix renders titleIgnorePrefix: "The Odyssey" -> "Odyssey, The".
func ignorePrefix(title string) string {
	lower := strings.ToLower(title)
	for _, p := range ignorePrefixes {
		if strings.HasPrefix(lower, p) {
			return strings.TrimSpace(title[len(p):]) + ", " + strings.TrimSpace(title[:len(p)])
		}
	}
	return title
}

func splitList(raw *string) []string {
	out := []string{}
	if raw == nil {
		return out
	}
	for _, part := range strings.Split(*raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstNonEmpty(vals ...*string) *string {
	for _, v := range vals {
		if v != nil && strings.TrimSpace(*v) != "" {
			return v
		}
	}
	return nil
}

// coverPath returns the media.coverPath value: the on-disk cover if one exists, else
// null. Book.CoverURL is an API path, not a disk path, so it is deliberately not used
// here.
func (h *Handler) coverPath(bookID string) *string {
	p := h.coverFile(bookID)
	if p == "" {
		return nil
	}
	return &p
}

// minifiedMedia renders the list-response media block.
func (h *Handler) minifiedMedia(v *itemView) bookMediaDTO {
	return bookMediaDTO{
		CoverPath: h.coverPath(v.Book.ID),
		Duration:  v.DurationSec,
		// media.id is the same 36-char sync id as the item. Keeping them equal means
		// the `mediaItemId` a client stores against progress inherits the same
		// merge-following durability as libraryItemId, instead of being a second,
		// unmanaged identity that dedup would orphan.
		ID:            v.SyncID,
		Metadata:      h.metadata(v),
		NumAudioFiles: len(v.Files),
		NumChapters:   len(v.Chapters),
		NumTracks:     len(v.Files),
		Size:          v.SizeBytes,
		Tags:          []string{},
	}
}

// expandedMedia renders the ?expanded=1 media block, including the playback timeline.
func (h *Handler) expandedMedia(v *itemView) bookMediaExpandedDTO {
	return bookMediaExpandedDTO{
		bookMediaDTO:  h.minifiedMedia(v),
		AudioFiles:    h.audioFiles(v),
		Chapters:      chapterDTOs(v.Chapters),
		EbookFile:     nil,
		LibraryItemID: v.SyncID,
		Tracks:        h.audioTracks(v),
	}
}

func chapterDTOs(chs []audioutil.Chapter) []chapterDTO {
	out := make([]chapterDTO, 0, len(chs))
	for i, c := range chs {
		// The id is re-derived from the position rather than copied, so it is always
		// the contiguous array index the client expects (§1.8.5 item 7).
		out = append(out, chapterDTO{ID: i, Start: c.StartSec, End: c.EndSec, Title: c.Title})
	}
	return out
}

func (h *Handler) audioFiles(v *itemView) []audioFileDTO {
	out := make([]audioFileDTO, 0, len(v.Files))
	for i := range v.Files {
		out = append(out, h.audioFile(v, i))
	}
	return out
}

// audioTracks renders media.tracks / the play session's audioTracks.
//
// Returns NIL, never an empty slice, when there are no files. Callers rely on that:
// an explicit "audioTracks": [] defeats AudioBooth's local-track fallback and kills
// playback of an already-downloaded book (§1.8.5 item 3).
func (h *Handler) audioTracks(v *itemView) []audioTrackDTO {
	if len(v.Files) == 0 {
		return nil
	}
	out := make([]audioTrackDTO, 0, len(v.Files))
	for i := range v.Files {
		f := &v.Files[i]
		out = append(out, audioTrackDTO{
			audioFileDTO: h.audioFile(v, i),
			// EXACTLY /api/items/{itemId}/file/{ino} with {ino} LAST. Absorb enforces
			// the segment count and a mismatch fails the ENTIRE download
			// (download_service.dart:1629-1660). Never session-scoped, never a
			// transcode URL.
			ContentURL:  fmt.Sprintf("/api/items/%s/file/%s", v.SyncID, f.SyncFileID),
			StartOffset: f.StartOffsetSec,
			Title:       trackTitle(f),
		})
	}
	return out
}

func trackTitle(f *fileView) string {
	if t := strings.TrimSpace(f.File.Title); t != "" {
		return t
	}
	return filepath.Base(f.File.FilePath)
}

func (h *Handler) audioFile(v *itemView, i int) audioFileDTO {
	f := &v.Files[i]
	disc := discPtr(f.File.DiscNumber)
	return audioFileDTO{
		AddedAt:       msEpoch(f.File.CreatedAt),
		BitRate:       f.File.BitrateKbps * 1000,
		ChannelLayout: channelLayout(f.File.Channels),
		Channels:      f.File.Channels,
		// A file's `chapters` are the chapters embedded in THAT file, which is a
		// narrower thing than the book's timeline. We only persist chapters per BOOK,
		// so the only case where we can honestly fill this in is a single-file book:
		// there, the book's chapters ARE that file's chapters. For a multi-file book we
		// cannot attribute a book-level chapter to a particular track, so it stays
		// empty — which is exactly what real ABS returns for the oracle's 6-mp3 set.
		Chapters:            h.audioFileChapters(v),
		Codec:               f.File.Codec,
		DiscNumFromFilename: disc,
		DiscNumFromMeta:     disc,
		Duration:            f.DurationSec,
		EmbeddedCoverArt:    nil,
		Error:               nil,
		Exclude:             f.File.SkipScan,
		Format:              f.File.Format,
		// ABS indexes tracks from 1, not 0. AudioBooth uses this index as the track
		// segment of /public/session/:id/track/:index, so an off-by-one here streams
		// the wrong file.
		Index:                i + 1,
		Ino:                  f.SyncFileID,
		Language:             fileLanguage(v),
		ManuallyVerified:     false,
		MetaTags:             h.metaTags(v, f),
		Metadata:             h.fileMetadata(f),
		MimeType:             mimeTypeForPath(f.File.FilePath),
		TimeBase:             "1/1000",
		TrackNumFromFilename: trackNumPtr(f.File.TrackNumber),
		TrackNumFromMeta:     trackNumFromTags(f.File.RawTags),
		UpdatedAt:            msEpoch(f.File.UpdatedAt),
	}
}

// audioFileChapters returns the per-file chapter list. See the note at its call site:
// only a single-file book can be attributed honestly.
func (h *Handler) audioFileChapters(v *itemView) []chapterDTO {
	if len(v.Files) != 1 {
		return []chapterDTO{}
	}
	return chapterDTOs(v.Chapters)
}

// fileLanguage reports the per-file STREAM language.
//
// ⚠️ FLAGGED DEVIATION, and a real data gap rather than a mapping choice. Real ABS
// fills this from ffprobe's stream language ("und" on the oracle's m4b, absent on its
// mp3s), but our scan pipeline runs ffprobe for duration only and persists no
// per-stream language, and BookFile has no column for one. So this is always null.
//
// Deliberately NOT backfilled from Book.Language: that is the CURATED metadata
// language shown in the UI (media.metadata.language), a different fact from what a
// container's audio stream declares — the oracle proves they differ, reporting
// metadata.language null and the stream "und" for the same file. Copying one into the
// other would make a curated value look like it came off the file.
//
// Zero client impact: neither AudioBooth nor Absorb reads audioFiles[].language. The
// fix belongs in the scanner (capture the ffprobe stream language), not here.
func fileLanguage(v *itemView) *string { return nil }

// trackNumPtr returns nil for an unset track number rather than a misleading 0.
func trackNumPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

// trackNumFromTags reads the track number out of the LOSSLESS embedded-tag capture,
// handling the "3/24" form. It returns nil when the file carries no track tag — which
// is a real and common state, and is different from "track 0".
func trackNumFromTags(tags map[string]string) *int {
	raw, ok := tags["track"]
	if !ok {
		return nil
	}
	if slash := strings.Index(raw, "/"); slash >= 0 {
		raw = raw[:slash]
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

// discPtr returns nil for an unset disc number so the field marshals as null,
// matching real ABS, rather than as a misleading 0.
func discPtr(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func channelLayout(channels int) string {
	switch channels {
	case 0:
		return ""
	case 1:
		return "mono"
	case 2:
		return "stereo"
	default:
		return strconv.Itoa(channels) + " channels"
	}
}

// metaTags renders the embedded-tag block.
//
// RawTags is the LOSSLESS capture of what the file actually carried at import, so it
// WINS wherever it has a value; the book-level values are only a fallback for rows
// imported before lossless capture existed. Doing it the other way round would report
// our curated metadata as though it were embedded in the file, which is precisely the
// confusion the metadata-editor screens exist to resolve.
func (h *Handler) metaTags(v *itemView, f *fileView) metaTagsDTO {
	tags := metaTagsDTO{
		TagAlbum:  v.Book.Title,
		TagArtist: strings.Join(authorNamesOf(v), ", "),
		TagGenre:  strings.Join(splitList(v.Book.Genre), ", "),
		TagTitle:  trackTitle(f),
	}
	if v.Book.Description != nil {
		tags.TagComment = *v.Book.Description
	}
	if f.File.TrackCount > 0 && f.File.TrackNumber > 0 {
		tags.TagTrack = fmt.Sprintf("%d/%d", f.File.TrackNumber, f.File.TrackCount)
	} else if f.File.TrackNumber > 0 {
		tags.TagTrack = strconv.Itoa(f.File.TrackNumber)
	}
	for key, dst := range map[string]*string{
		"album":   &tags.TagAlbum,
		"artist":  &tags.TagArtist,
		"comment": &tags.TagComment,
		"date":    &tags.TagDate,
		"encoder": &tags.TagEncoder,
		"genre":   &tags.TagGenre,
		"title":   &tags.TagTitle,
		"track":   &tags.TagTrack,
	} {
		if val, ok := f.File.RawTags[key]; ok && strings.TrimSpace(val) != "" {
			*dst = val
		}
	}
	return tags
}

func authorNamesOf(v *itemView) []string {
	out := make([]string, 0, len(v.Authors))
	for _, a := range v.Authors {
		if n := strings.TrimSpace(a.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (h *Handler) fileMetadata(f *fileView) fileMetadataDTO {
	path := f.File.FilePath
	return fileMetadataDTO{
		BirthtimeMs: f.BirthtimeMs,
		CtimeMs:     f.CtimeMs,
		Ext:         strings.ToLower(filepath.Ext(path)),
		Filename:    filepath.Base(path),
		MtimeMs:     f.MtimeMs,
		Path:        path,
		RelPath:     filepath.Base(path),
		Size:        f.SizeBytes,
	}
}

// mimeTypeForPath maps an audiobook extension to the MIME type clients expect.
// Deliberately not mime.TypeByExtension: on some platforms that consults OS
// registries that map .m4b to a generic or wrong type, and iOS players care about
// getting exactly audio/mp4 for an MPEG-4 container.
func mimeTypeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4b", ".m4a", ".mp4":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".opus", ".ogg":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".aax", ".aaxc":
		// DRM-protected; still reported honestly so a client can show something
		// rather than silently treating the track as absent.
		return "audio/vnd.audible.aax"
	default:
		return "application/octet-stream"
	}
}

// ── item envelopes ──────────────────────────────────────────────────────────

// minifiedItem renders the list-response library item.
func (h *Handler) minifiedItem(v *itemView) libraryItemDTO {
	dir := itemDir(v)
	return libraryItemDTO{
		AddedAt:     msEpochPtr(v.Book.CreatedAt),
		BirthtimeMs: msEpochPtr(v.Book.CreatedAt),
		CtimeMs:     msEpochPtr(v.Book.UpdatedAt),
		FolderID:    h.folderID(),
		ID:          v.SyncID,
		// The item-level `ino` is the same durable sync id, not a filesystem inode
		// (spec §4.2b). Clients treat it as opaque.
		Ino: v.SyncID,
		// Always false: this app stores every book as a directory of one or more
		// files, and `path` above is that directory. ABS only sets isFile for a
		// loose file sitting directly in a library folder.
		IsFile:           false,
		IsInvalid:        false,
		IsMissing:        itemMissing(v),
		LibraryID:        h.libraryID(),
		Media:            h.minifiedMedia(v),
		MediaType:        "book",
		MtimeMs:          msEpochPtr(v.Book.UpdatedAt),
		NumFiles:         len(v.Files),
		OldLibraryItemID: nil,
		Path:             dir,
		RelPath:          h.relPath(dir),
		Size:             v.SizeBytes,
		UpdatedAt:        msEpochPtr(v.Book.UpdatedAt),
	}
}

// expandedItem renders the ?expanded=1 library item, including libraryFiles.
func (h *Handler) expandedItem(v *itemView) libraryItemExpandedDTO {
	base := h.minifiedItem(v)
	base.Media = h.expandedMedia(v)
	return libraryItemExpandedDTO{
		libraryItemDTO: base,
		LastScan:       msEpochPtr(v.Book.UpdatedAt),
		LibraryFiles:   h.libraryFiles(v),
		ScanVersion:    h.cfg.ServerVersion,
	}
}

func (h *Handler) libraryFiles(v *itemView) []libraryFileDTO {
	out := make([]libraryFileDTO, 0, len(v.Files))
	for i := range v.Files {
		f := &v.Files[i]
		out = append(out, libraryFileDTO{
			AddedAt:  msEpoch(f.File.CreatedAt),
			FileType: "audio",
			Ino:      f.SyncFileID,
			// null, matching real ABS: the flag is tri-state there (an ebook or a
			// cover can be marked supplementary), and every file we list is a primary
			// audio track, so "not applicable" is the honest value rather than false.
			IsSupplementary: nil,
			Metadata:        h.fileMetadata(f),
			UpdatedAt:       msEpoch(f.File.UpdatedAt),
		})
	}
	return out
}

// itemMissing reports whether NONE of the book's files resolve on disk. A partially
// missing book is still playable, so it is not reported missing — that flag greys the
// item out in both clients.
func itemMissing(v *itemView) bool {
	if len(v.Files) == 0 {
		return false
	}
	for i := range v.Files {
		if v.Files[i].Exists {
			return false
		}
	}
	return true
}

// itemDir is the directory that holds the book, which is what ABS reports as the
// item path.
func itemDir(v *itemView) string {
	if len(v.Files) > 0 && v.Files[0].File.FilePath != "" {
		return filepath.Dir(v.Files[0].File.FilePath)
	}
	if v.Book.FilePath != "" {
		return filepath.Dir(v.Book.FilePath)
	}
	return ""
}

// relPath renders the item path relative to the configured library root, falling
// back to the base name when the path lies outside it.
func (h *Handler) relPath(dir string) string {
	if h.coverRoot != "" {
		if rel, err := filepath.Rel(h.coverRoot, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(dir)
}

// msEpochPtr is msEpoch for the *time.Time columns the Book model uses. A nil
// column becomes 0 rather than a negative epoch, matching msEpoch's contract.
func msEpochPtr(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return msEpoch(*t)
}
