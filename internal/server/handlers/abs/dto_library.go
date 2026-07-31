// file: internal/server/handlers/abs/dto_library.go
// version: 1.0.0
// guid: c471e9a0-5b83-4d16-92fe-08a7c35d1b6e
// last-edited: 2026-07-30

package abs

// The DTOs here continue dto.go's type contract for the browse + playback surface.
// Read dto.go's header first; the same rules bind, and these are the shapes where
// they bite hardest:
//
//  1. publishedYear / publishedDate / series[].sequence are STRINGS or null, never
//     numbers (§1.7.2). In Dart `as String?` on a number THROWS, inside a widget
//     build() — a numeric year blanks the whole book page.
//  2. Every count (total, page, numBooks, numAudioFiles, numChapters, numTracks) is
//     a Go int, so it can never serialize with a decimal point (§1.7.3 item 5).
//  3. Every Date-ish field is an int64 millisecond epoch (§1.8.5 item 1).
//  4. `AudioTracks` is a NIL-able slice with omitempty. An explicit "audioTracks":[]
//     is WORSE than omitting the key: AudioBooth's `?? orderedTracks` local-track
//     fallback only fires on nil, so [] kills playback of a downloaded book
//     (§1.8.5 item 3). Never assign an empty non-nil slice to it.
//  5. Chapter.ID is an int while every other id in the protocol is a string
//     (§1.8.5 item 7).
//  6. Every relation is emitted in BOTH forms — object array AND flattened string
//     (§1.7.3 item 8). Items are read via the flat strings; the arrays matter in
//     filterdata. Note filterdata.narrators is plain NAME STRINGS while
//     filterdata.authors is objects.

// idNameDTO is the {id, name} pair ABS uses for author references.
type idNameDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// seriesRefDTO is one entry of media.metadata.series. Sequence is a *string: it may
// be absent (null) but must never be a number.
type seriesRefDTO struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// ── library ─────────────────────────────────────────────────────────────────

type libraryFolderDTO struct {
	AddedAt   int64  `json:"addedAt"`
	FullPath  string `json:"fullPath"`
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
}

// librarySettingsDTO mirrors ABS 2.36.0's library settings verbatim. Emitted whole
// rather than as a subset for the same reason as serverSettings: the decode is
// all-or-nothing, so a field we omit that a client decodes non-optionally is an
// outage, and there is no cost to completeness.
type librarySettingsDTO struct {
	AudiobooksOnly                     bool     `json:"audiobooksOnly"`
	AutoScanCronExpression             *string  `json:"autoScanCronExpression"`
	CoverAspectRatio                   int      `json:"coverAspectRatio"`
	DisableWatcher                     bool     `json:"disableWatcher"`
	EpubsAllowScriptedContent          bool     `json:"epubsAllowScriptedContent"`
	HideSingleBookSeries               bool     `json:"hideSingleBookSeries"`
	MarkAsFinishedPercentComplete      *int     `json:"markAsFinishedPercentComplete"`
	MarkAsFinishedTimeRemaining        int      `json:"markAsFinishedTimeRemaining"`
	MetadataPrecedence                 []string `json:"metadataPrecedence"`
	OnlyShowLaterBooksInContinueSeries bool     `json:"onlyShowLaterBooksInContinueSeries"`
	SkipMatchingMediaWithAsin          bool     `json:"skipMatchingMediaWithAsin"`
	SkipMatchingMediaWithIsbn          bool     `json:"skipMatchingMediaWithIsbn"`
}

// libraryDTO is one entry of GET /api/libraries.
//
// MediaType must be exactly "book" or "podcast" — AudioBooth decodes it as a
// NON-TOLERANT enum (§1.8.5 item 9), so "audiobook" or "books" throws. ID must be a
// JSON string or Absorb's library selection throws and no library is ever selected,
// which leaves the app dead (§1.7.1).
type libraryDTO struct {
	CreatedAt       int64              `json:"createdAt"`
	DisplayOrder    int                `json:"displayOrder"`
	Folders         []libraryFolderDTO `json:"folders"`
	Icon            string             `json:"icon"`
	ID              string             `json:"id"`
	LastScan        int64              `json:"lastScan"`
	LastScanVersion string             `json:"lastScanVersion"`
	LastUpdate      int64              `json:"lastUpdate"`
	MediaType       string             `json:"mediaType"`
	Name            string             `json:"name"`
	Provider        string             `json:"provider"`
	Settings        librarySettingsDTO `json:"settings"`
}

type librariesResponse struct {
	Libraries []libraryDTO `json:"libraries"`
}

// ── metadata ────────────────────────────────────────────────────────────────

// bookMetadataDTO is media.metadata. We always emit the EXPANDED superset, even on
// minified list responses: the extra keys are harmless (real ABS omits some of them
// on the minified path, and no client rejects unknown fields), while emitting both
// the object arrays and the flat strings everywhere satisfies §1.7.3 item 8 in one
// place instead of in two divergent shapes.
type bookMetadataDTO struct {
	Abridged          bool           `json:"abridged"`
	ASIN              *string        `json:"asin"`
	AuthorName        string         `json:"authorName"`
	AuthorNameLF      string         `json:"authorNameLF"`
	Authors           []idNameDTO    `json:"authors"`
	Description       *string        `json:"description"`
	DescriptionPlain  *string        `json:"descriptionPlain"`
	Explicit          bool           `json:"explicit"`
	Genres            []string       `json:"genres"`
	ISBN              *string        `json:"isbn"`
	Language          *string        `json:"language"`
	NarratorName      string         `json:"narratorName"`
	Narrators         []string       `json:"narrators"`
	PublishedDate     *string        `json:"publishedDate"`
	PublishedYear     *string        `json:"publishedYear"`
	Publisher         *string        `json:"publisher"`
	Series            []seriesRefDTO `json:"series"`
	SeriesName        string         `json:"seriesName"`
	Subtitle          *string        `json:"subtitle"`
	Title             string         `json:"title"`
	TitleIgnorePrefix string         `json:"titleIgnorePrefix"`
}

// ── files, tracks, chapters ─────────────────────────────────────────────────

// fileMetadataDTO is the per-file metadata block. Every timestamp is an int64 ms
// epoch; `size` is an int64 byte count.
type fileMetadataDTO struct {
	BirthtimeMs int64  `json:"birthtimeMs"`
	CtimeMs     int64  `json:"ctimeMs"`
	Ext         string `json:"ext"`
	Filename    string `json:"filename"`
	MtimeMs     int64  `json:"mtimeMs"`
	Path        string `json:"path"`
	RelPath     string `json:"relPath"`
	Size        int64  `json:"size"`
}

// metaTagsDTO is the embedded-tag block real ABS reports per audio file.
//
// Every field is `omitempty` because real ABS reports the tags a file ACTUALLY
// carries and nothing else — the oracle's m4b has a tagDate and no tagTrack, its mp3s
// the reverse. Emitting `""` for a tag the file does not have would be a small lie
// with a real cost: the metadata-editor screens in both clients show these verbatim,
// so a fabricated empty tag reads as "this file has a blank title" rather than "this
// file has no title tag".
type metaTagsDTO struct {
	TagAlbum   string `json:"tagAlbum,omitempty"`
	TagArtist  string `json:"tagArtist,omitempty"`
	TagComment string `json:"tagComment,omitempty"`
	TagDate    string `json:"tagDate,omitempty"`
	TagEncoder string `json:"tagEncoder,omitempty"`
	TagGenre   string `json:"tagGenre,omitempty"`
	TagTitle   string `json:"tagTitle,omitempty"`
	TagTrack   string `json:"tagTrack,omitempty"`
}

// chapterDTO is one navigable chapter. All four fields are required (§1.8.5 item 7)
// and ID is an INT (the array index), unlike every other id in the protocol.
type chapterDTO struct {
	End   float64 `json:"end"`
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	Title string  `json:"title"`
}

// audioFileDTO is media.audioFiles[] — the physical file, with no playback timeline.
type audioFileDTO struct {
	AddedAt             int64        `json:"addedAt"`
	BitRate             int          `json:"bitRate"`
	ChannelLayout       string       `json:"channelLayout"`
	Channels            int          `json:"channels"`
	Chapters            []chapterDTO `json:"chapters"`
	Codec               string       `json:"codec"`
	DiscNumFromFilename *int         `json:"discNumFromFilename"`
	DiscNumFromMeta     *int         `json:"discNumFromMeta"`
	Duration            float64      `json:"duration"`
	EmbeddedCoverArt    *string      `json:"embeddedCoverArt"`
	Error               *string      `json:"error"`
	Exclude             bool         `json:"exclude"`
	Format              string       `json:"format"`
	Index               int          `json:"index"`
	Ino                 string       `json:"ino"`
	// Language is the stream language the container declares ("und" when the file
	// says "undetermined"), or null when we have none. A *string rather than a plain
	// string so "we do not know" stays distinguishable from "the file says nothing".
	Language         *string         `json:"language"`
	ManuallyVerified bool            `json:"manuallyVerified"`
	MetaTags         metaTagsDTO     `json:"metaTags"`
	Metadata         fileMetadataDTO `json:"metadata"`
	MimeType         string          `json:"mimeType"`
	TimeBase         string          `json:"timeBase"`
	// TrackNumFromFilename and TrackNumFromMeta are separate on purpose and are NOT
	// interchangeable: ABS reports where each number came from, and the two disagree
	// in practice (the oracle's 6th mp3 has a filename-derived 6 and NO track tag at
	// all, and its m4b has neither). Both are null when that source has no number —
	// fabricating an index here would tell a client the file is tagged when it is not.
	TrackNumFromFilename *int  `json:"trackNumFromFilename"`
	TrackNumFromMeta     *int  `json:"trackNumFromMeta"`
	UpdatedAt            int64 `json:"updatedAt"`
}

// audioTrackDTO is an audioFile placed on the whole-book timeline.
//
// Index, StartOffset and Duration are non-optional in AudioBooth's Codable
// (Models/AudioTrack.swift:4-6) and StartOffset is CUMULATIVE float seconds across
// tracks — real ABS emits 0, 1386.057143, 2788.702041, 4309.211429. They are plain
// value types here, never pointers, so a nil can never reach the wire, and nothing
// rounds them.
//
// ContentUrl is exactly /api/items/{itemId}/file/{ino} with {ino} LAST: Absorb
// validates the segment count and a mismatch fails the ENTIRE download
// (download_service.dart:1629-1660).
type audioTrackDTO struct {
	audioFileDTO
	ContentURL  string  `json:"contentUrl"`
	StartOffset float64 `json:"startOffset"`
	Title       string  `json:"title"`
}

// libraryFileDTO is one entry of libraryItem.libraryFiles. It is the strictest
// object in AudioBooth's repo (§1.8.5 item 8): ino, metadata, addedAt, updatedAt
// plus metadata.{filename, ext, path, relPath, size, birthtimeMs} are all required.
// We emit it complete rather than omitting it, because real ABS does and the
// download UI reads it.
type libraryFileDTO struct {
	AddedAt         int64           `json:"addedAt"`
	FileType        string          `json:"fileType"`
	Ino             string          `json:"ino"`
	IsSupplementary *bool           `json:"isSupplementary"`
	Metadata        fileMetadataDTO `json:"metadata"`
	UpdatedAt       int64           `json:"updatedAt"`
}

// ── media ───────────────────────────────────────────────────────────────────

// bookMediaDTO is the MINIFIED media block used in list responses.
//
// Duration must be non-zero for an audiobook: if duration <= 0 and
// audioFiles/tracks/numAudioFiles are empty-or-zero, Absorb classifies the item as
// ebook-only and THE PLAY BUTTON DISAPPEARS (player_settings.dart:895-909).
type bookMediaDTO struct {
	CoverPath     *string         `json:"coverPath"`
	Duration      float64         `json:"duration"`
	ID            string          `json:"id"`
	Metadata      bookMetadataDTO `json:"metadata"`
	NumAudioFiles int             `json:"numAudioFiles"`
	NumChapters   int             `json:"numChapters"`
	NumTracks     int             `json:"numTracks"`
	Size          int64           `json:"size"`
	Tags          []string        `json:"tags"`
}

// bookMediaExpandedDTO is the media block for ?expanded=1 and for the libraryItem
// embedded in a play session. The embedded bookMediaDTO is inlined by
// encoding/json, so this is the minified shape plus the timeline.
type bookMediaExpandedDTO struct {
	bookMediaDTO
	AudioFiles    []audioFileDTO  `json:"audioFiles"`
	Chapters      []chapterDTO    `json:"chapters"`
	EbookFile     *string         `json:"ebookFile"`
	LibraryItemID string          `json:"libraryItemId"`
	Tracks        []audioTrackDTO `json:"tracks"`
}

// ── library item ────────────────────────────────────────────────────────────

// libraryItemDTO is the MINIFIED library item.
//
// ID is the durable 36-char sync_item UUID, never the Book ULID (§1.7.1). Media is
// `any` so the same envelope carries either the minified or the expanded media block
// without two near-identical item structs drifting apart.
type libraryItemDTO struct {
	AddedAt          int64   `json:"addedAt"`
	BirthtimeMs      int64   `json:"birthtimeMs"`
	CtimeMs          int64   `json:"ctimeMs"`
	FolderID         string  `json:"folderId"`
	ID               string  `json:"id"`
	Ino              string  `json:"ino"`
	IsFile           bool    `json:"isFile"`
	IsInvalid        bool    `json:"isInvalid"`
	IsMissing        bool    `json:"isMissing"`
	LibraryID        string  `json:"libraryId"`
	Media            any     `json:"media"`
	MediaType        string  `json:"mediaType"`
	MtimeMs          int64   `json:"mtimeMs"`
	NumFiles         int     `json:"numFiles"`
	OldLibraryItemID *string `json:"oldLibraryItemId"`
	Path             string  `json:"path"`
	RelPath          string  `json:"relPath"`
	Size             int64   `json:"size"`
	UpdatedAt        int64   `json:"updatedAt"`
}

// libraryItemExpandedDTO adds the fields real ABS only returns on ?expanded=1.
type libraryItemExpandedDTO struct {
	libraryItemDTO
	LastScan     int64            `json:"lastScan"`
	LibraryFiles []libraryFileDTO `json:"libraryFiles"`
	ScanVersion  string           `json:"scanVersion"`
}

// itemWithProgressDTO is GET /api/items/:id with ?include=progress. The progress
// object is emitted whenever we have one, regardless of the include flag, because
// some clients ignore the gate (§1.6 item 3) and an absent-but-known progress is
// indistinguishable from "never started" to a client.
type itemWithProgressDTO struct {
	libraryItemExpandedDTO
	UserMediaProgress *mediaProgressDTO `json:"userMediaProgress,omitempty"`
}

// mediaProgressDTO is one (user, item) progress record.
//
// LastUpdate is the single highest-value field in the protocol (§1.7.3 item 1): omit
// it and the server permanently loses every conflict, because clients compare it
// against their own wall clock and ties go to local. Duration is always sent
// alongside IsFinished — `isFinished:true` with a null duration sets the client's
// currentTime to 0 (§1.8.7). Progress is a 0.0–1.0 FRACTION, not a percentage
// (§1.8.5 item 11).
type mediaProgressDTO struct {
	CurrentTime               float64 `json:"currentTime"`
	Duration                  float64 `json:"duration"`
	EbookLocation             *string `json:"ebookLocation"`
	EbookProgress             float64 `json:"ebookProgress"`
	EpisodeID                 *string `json:"episodeId"`
	FinishedAt                *int64  `json:"finishedAt"`
	HideFromContinueListening bool    `json:"hideFromContinueListening"`
	ID                        string  `json:"id"`
	IsFinished                bool    `json:"isFinished"`
	LastUpdate                int64   `json:"lastUpdate"`
	LibraryItemID             string  `json:"libraryItemId"`
	MediaItemID               string  `json:"mediaItemId"`
	MediaItemType             string  `json:"mediaItemType"`
	Progress                  float64 `json:"progress"`
	StartedAt                 int64   `json:"startedAt"`
	UserID                    string  `json:"userId"`
}

// ── list envelopes ──────────────────────────────────────────────────────────

// itemsPageResponse is GET /api/libraries/:id/items. Total AND page are both
// required by Page<T> (§1.8.5 item 5) and both are ints (§1.7.3 item 5).
type itemsPageResponse struct {
	CollapseSeries bool   `json:"collapseseries"`
	Include        string `json:"include"`
	Limit          int    `json:"limit"`
	MediaType      string `json:"mediaType"`
	Minified       bool   `json:"minified"`
	Offset         int    `json:"offset"`
	Page           int    `json:"page"`
	Results        []any  `json:"results"`
	SortDesc       bool   `json:"sortDesc"`
	Total          int    `json:"total"`
}

// pageResponse is the generic {results,total,page} envelope used by /series (and by
// the /collections and /playlists stubs). §1.8.6: `{}` throws because Page<T> needs
// total and page even when results is empty.
type pageResponse struct {
	Include  string `json:"include"`
	Limit    int    `json:"limit"`
	Minified bool   `json:"minified"`
	Page     int    `json:"page"`
	Results  []any  `json:"results"`
	SortDesc bool   `json:"sortDesc"`
	Total    int    `json:"total"`
}

// authorDTO is one entry of /api/libraries/:id/authors. NumBooks is an int: it is
// cast during widget build in Absorb (library_grid_tiles.dart:441), so a float
// red-screens the author tiles.
type authorDTO struct {
	AddedAt     int64   `json:"addedAt"`
	ASIN        *string `json:"asin"`
	Description *string `json:"description"`
	ID          string  `json:"id"`
	ImagePath   *string `json:"imagePath"`
	LastFirst   string  `json:"lastFirst"`
	LibraryID   string  `json:"libraryId"`
	Name        string  `json:"name"`
	NumBooks    int     `json:"numBooks"`
	UpdatedAt   int64   `json:"updatedAt"`
}

// authorsResponse is the wrapper real ABS returns. abs-shim's bare {authors:[…]}
// with no total/page is noted in §1.8.5 item 5 as a shape that would throw for a
// Page<T> decode; ABS 2.36.0 itself returns exactly this wrapper, and the fixtures
// confirm it, so we match the oracle rather than the shim.
type authorsResponse struct {
	Authors []authorDTO `json:"authors"`
}

// narratorsResponse is /api/libraries/:id/narrators. The wrapper key is required
// (§1.8.6) and entries are objects with a name and a book count.
type narratorsResponse struct {
	Narrators []narratorDTO `json:"narrators"`
}

type narratorDTO struct {
	Name     string `json:"name"`
	NumBooks int    `json:"numBooks"`
}

// episodesResponse is /api/libraries/:id/recent-episodes — the podcast stub. The
// wrapper key is required (§1.8.6); an empty array is the correct answer for a
// library that holds no podcasts.
type episodesResponse struct {
	Episodes []any `json:"episodes"`
}

// filterDataResponse is ?include=filterdata. ALL EIGHT of authors, genres, tags,
// series, narrators, languages, publishers and publishedDecades are decoded
// non-optionally, so every key must be present (§1.8.6).
//
// Note the asymmetry in §1.7.3 item 8, which is easy to get backwards: Authors is
// OBJECTS while Narrators is PLAIN NAME STRINGS.
type filterDataResponse struct {
	AuthorCount      int         `json:"authorCount"`
	Authors          []idNameDTO `json:"authors"`
	BookCount        int         `json:"bookCount"`
	Genres           []string    `json:"genres"`
	Languages        []string    `json:"languages"`
	LoadedAt         int64       `json:"loadedAt"`
	Narrators        []string    `json:"narrators"`
	NumIssues        int         `json:"numIssues"`
	PodcastCount     int         `json:"podcastCount"`
	PublishedDecades []string    `json:"publishedDecades"`
	Publishers       []string    `json:"publishers"`
	Series           []idNameDTO `json:"series"`
	SeriesCount      int         `json:"seriesCount"`
	Tags             []string    `json:"tags"`
}

// searchResponse is /api/libraries/:id/search. Every key is a non-nil array: a null
// there fails the decode, and the six keys are what ABS returns.
type searchResponse struct {
	Authors   []any              `json:"authors"`
	Book      []searchBookHitDTO `json:"book"`
	Genres    []any              `json:"genres"`
	Narrators []any              `json:"narrators"`
	Series    []any              `json:"series"`
	Tags      []any              `json:"tags"`
}

type searchBookHitDTO struct {
	LibraryItem any `json:"libraryItem"`
}

// shelfDTO is one shelf of /api/libraries/:id/personalized. That endpoint's body is
// a BARE ARRAY of these — an object there throws (§1.8.6).
type shelfDTO struct {
	Entities       []any  `json:"entities"`
	ID             string `json:"id"`
	Label          string `json:"label"`
	LabelStringKey string `json:"labelStringKey"`
	Total          int    `json:"total"`
	Type           string `json:"type"`
}
