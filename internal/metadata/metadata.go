// file: internal/metadata/metadata.go
// version: 1.18.0
// guid: 9d0e1f2a-3b4c-5d6e-7f8a-9b0c1d2e3f4a

package metadata

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dhowden/tag"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var yearPattern = regexp.MustCompile(`(\d{4})`)

// MetadataExtractor defines the interface for extracting metadata from files.
// Tests can swap a mock implementation via SetMetadataExtractor.
type MetadataExtractor interface {
	ExtractMetadata(filePath string) (Metadata, error)
}

// activeExtractor overrides the default implementation. Set by tests via SetMetadataExtractor.
var activeExtractor MetadataExtractor

// SetMetadataExtractor overrides the default metadata extractor for testing.
func SetMetadataExtractor(e MetadataExtractor) {
	activeExtractor = e
}

// Metadata holds audio file metadata
type Metadata struct {
	Title        string
	Artist       string
	AuthorSource string
	Album        string
	Genre        string
	Series       string
	SeriesIndex  int
	Comments     string
	Year         int
	// Track / disc position within a multi-file album. Captured so the scanner can
	// order chapters and group by album instead of by folder (the shattering bug).
	TrackNumber int
	TrackTotal  int
	DiscNumber  int
	DiscTotal   int
	// Grouping is the iTunes "Grouping" field (ID3 TIT1/GRP1, MP4 ©grp). It is the
	// authoritative box-set/anthology divider: a single Album that contains multiple
	// books carries a distinct Grouping per book. Kept distinct from Series.
	Grouping string
	// Extended fields (best-effort; may be empty)
	Narrator  string
	Language  string
	Publisher string
	ISBN10    string
	ISBN13    string
	// Custom organizer tags (read from embedded AUDIOBOOK_ORGANIZER_* tags)
	BookOrganizerID     string
	OrganizerTagVersion string
	ASIN                string
	OpenLibraryID       string
	HardcoverID         string
	GoogleBooksID       string
	Edition             string
	PrintYear           string
	// UsedFilenameFallback indicates filename parsing filled metadata gaps.
	UsedFilenameFallback bool
}

type fieldCandidate struct {
	value  string
	source string
}

func pickFirstNonEmpty(candidates ...fieldCandidate) (string, string) {
	for _, candidate := range candidates {
		if trimmed := cleanTagValue(candidate.value); trimmed != "" {
			return trimmed, candidate.source
		}
	}
	return "", ""
}

func setFieldSource(sources map[string]string, field, source string) {
	if sources == nil || source == "" {
		return
	}
	sources[field] = source
}

func sourceOrUnknown(sources map[string]string, field string) string {
	if sources == nil {
		return "unset"
	}
	if value, ok := sources[field]; ok && value != "" {
		return value
	}
	return "unset"
}

// ExtractMetadata reads metadata from audio files.
// If metaLog is nil, a default logger is used.
func ExtractMetadata(filePath string, metaLog logger.Logger) (Metadata, error) {
	if activeExtractor != nil {
		return activeExtractor.ExtractMetadata(filePath)
	}
	if metaLog == nil {
		metaLog = logger.New("metadata")
	}
	var metadata Metadata
	metaLog.Debug("extracting tags for %s", filePath)

	// Check if path is a directory — tag extraction only works on files
	info, err := os.Stat(filePath)
	if err != nil {
		return metadata, fmt.Errorf("error stat'ing path: %w", err)
	}
	if info.IsDir() {
		metaLog.Debug("%s is a directory, falling back to filename parsing", filePath)
		metadata = extractFromFilename(filePath)
		metadata.UsedFilenameFallback = true
		if metadata.SeriesIndex == 0 {
			metadata.SeriesIndex = DetectVolumeNumber(metadata.Title)
		}
		return metadata, nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return metadata, fmt.Errorf("error opening file: %w", err)
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		// dhowden/tag choked on this file. Before giving up and falling
		// back to filename parsing, try TagLib — it understands more
		// variants (M4B with unusual atoms, certain DRM-scrubbed files,
		// Vorbis-with-non-standard-comment layouts) and we already link
		// its static libs for tag writing. TagLib's output is folded
		// into a Metadata via BuildMetadataFromTaglibMap so everything
		// downstream sees the same shape.
		metaLog.Warn("dhowden/tag failed for %s: %v; trying taglib fallback", filePath, err)
		if tagMap, tlErr := readTagsWithTaglib(filePath); tlErr == nil && len(tagMap) > 0 {
			result := BuildMetadataFromTaglibMap(tagMap, filePath, metaLog)
			metaLog.Info("taglib fallback succeeded for %s (%d keys)", filePath, len(tagMap))
			return result, nil
		} else if tlErr != nil {
			metaLog.Warn("taglib fallback also failed for %s: %v", filePath, tlErr)
		}
		metaLog.Warn("both parsers failed for %s; falling back to filename parsing", filePath)
		metadata = extractFromFilename(filePath)
		metadata.UsedFilenameFallback = true
		if metadata.SeriesIndex == 0 {
			metadata.SeriesIndex = DetectVolumeNumber(metadata.Title)
		}
		return metadata, nil
	}

	result := BuildMetadataFromTag(m, filePath, metaLog)
	return result, nil
}

// BuildMetadataFromTag extracts a Metadata struct from an already-parsed tag.Metadata.
// filePath is used only for filename-fallback logic and logging; the file is not re-opened.
// This is the shared builder used by both ExtractMetadata and ProcessFile (single-pass).
// If metaLog is nil, a default logger is used.
func BuildMetadataFromTag(m tag.Metadata, filePath string, metaLog logger.Logger) Metadata {
	if metaLog == nil {
		metaLog = logger.New("metadata")
	}
	var metadata Metadata
	fieldSources := map[string]string{}
	seriesIndexSource := ""
	fallbackUsed := false
	authorFromArtist := false

	raw := m.Raw()
	if len(raw) > 0 {
		rawKeys := make([]string, 0, len(raw))
		for key := range raw {
			rawKeys = append(rawKeys, key)
		}
		sort.Strings(rawKeys)
		metaLog.Trace("raw tag keys for %s: %v", filePath, rawKeys)
	}

	albumValue, albumSource := pickFirstNonEmpty(
		fieldCandidate{value: m.Album(), source: "tag.Album"},
		fieldCandidate{value: getRawString(raw, "TALB", "\xa9alb", "©alb", "album"), source: "raw.album"},
	)
	metadata.Album = cleanTagValue(albumValue)
	if metadata.Album != "" {
		setFieldSource(fieldSources, "album", albumSource)
	}

	titleValue, titleSource := pickFirstNonEmpty(
		fieldCandidate{value: m.Title(), source: "tag.Title"},
		fieldCandidate{value: getRawString(raw, "\xa9nam", "©nam", "title", "TIT2"), source: "raw.title"},
		fieldCandidate{value: metadata.Album, source: "album"},
	)
	metadata.Title = cleanTagValue(titleValue)
	if metadata.Title != "" {
		setFieldSource(fieldSources, "title", titleSource)
	} else {
		metadata.Title = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
		setFieldSource(fieldSources, "title", "filename default (basename)")
	}

	composerValue, composerSource := pickFirstNonEmpty(
		fieldCandidate{value: m.Composer(), source: "tag.Composer"},
		fieldCandidate{value: getRawString(raw, "TCOM", "\xa9wrt", "composer"), source: "raw.composer"},
	)
	albumArtistValue, albumArtistSource := pickFirstNonEmpty(
		fieldCandidate{value: m.AlbumArtist(), source: "tag.AlbumArtist"},
		fieldCandidate{value: getRawString(raw, "TPE2", "ALBUMARTIST", "AlbumArtist", "album_artist", "aART"), source: "raw.album_artist"},
	)
	artistValue, artistSource := pickFirstNonEmpty(
		fieldCandidate{value: m.Artist(), source: "tag.Artist"},
		fieldCandidate{value: getRawString(raw, "TPE1", "artist", "\xa9ART", "©ART"), source: "raw.artist"},
	)
	// Priority: album_artist > artist > composer.
	// Composer is used as fallback only — in audiobooks, composer typically
	// contains the narrator, not the author.
	if albumArtistValue != "" {
		metadata.Artist = cleanTagValue(albumArtistValue)
		if metadata.Artist != "" {
			setFieldSource(fieldSources, "author", albumArtistSource+" (album_artist)")
		}
	} else if artistValue != "" {
		metadata.Artist = cleanTagValue(artistValue)
		if metadata.Artist != "" {
			setFieldSource(fieldSources, "author", artistSource)
			authorFromArtist = true
		}
	} else if composerValue != "" {
		// Composer as last resort — in audiobooks this is often the narrator
		metadata.Artist = cleanTagValue(composerValue)
		setFieldSource(fieldSources, "author", composerSource+" (composer fallback)")
	}

	genreValue, genreSource := pickFirstNonEmpty(
		fieldCandidate{value: m.Genre(), source: "tag.Genre"},
		fieldCandidate{value: getRawString(raw, "TCON", "genre", "\xa9gen", "©gen"), source: "raw.genre"},
	)
	metadata.Genre = cleanTagValue(genreValue)
	if metadata.Genre != "" {
		setFieldSource(fieldSources, "genre", genreSource)
	}

	narratorValue, narratorSource := pickFirstNonEmpty(
		fieldCandidate{value: getRawString(raw, "PERFORMER", "Performer", "TXXX:NARRATOR", "TXXX:Narrator", "NARRATOR", "Narrator", "©nrt", "\xa9nrt"), source: "raw.narrator"},
		fieldCandidate{value: getRawString(raw, "TXXX:Reader", "READER"), source: "raw.reader"},
	)
	metadata.Narrator = cleanTagValue(narratorValue)
	if metadata.Narrator != "" {
		setFieldSource(fieldSources, "narrator", narratorSource)
	} else if !authorFromArtist {
		artistFallback := cleanTagValue(artistValue)
		if artistFallback != "" && artistFallback != metadata.Artist {
			metadata.Narrator = artistFallback
			setFieldSource(fieldSources, "narrator", artistSource)
		}
	}

	languageValue, languageSource := pickFirstNonEmpty(
		fieldCandidate{value: getRawString(raw, "TLAN", "LANGUAGE"), source: "raw.language"},
	)
	metadata.Language = cleanTagValue(languageValue)
	if metadata.Language != "" {
		setFieldSource(fieldSources, "language", languageSource)
	}

	publisherValue, publisherSource := pickFirstNonEmpty(
		fieldCandidate{value: getRawString(raw, "TPUB", "publisher", "PUBLISHER", "LABEL", "©pub", "\xa9pub"), source: "raw.publisher"},
	)
	metadata.Publisher = cleanTagValue(publisherValue)
	if metadata.Publisher != "" {
		setFieldSource(fieldSources, "publisher", publisherSource)
	}

	commentValue, commentSource := pickFirstNonEmpty(
		fieldCandidate{value: getRawString(raw, "DESCRIPTION", "description"), source: "raw.description"},
		fieldCandidate{value: m.Comment(), source: "tag.Comment"},
		fieldCandidate{value: getRawString(raw, "COMM", "comment", "©cmt", "\xa9cmt"), source: "raw.comment"},
	)
	metadata.Comments = cleanTagValue(commentValue)
	if metadata.Comments != "" {
		setFieldSource(fieldSources, "comment", commentSource)
	}

	metadata.Year = m.Year()
	yearSource := "tag.Year"
	if metadata.Year == 0 {
		yearSource = ""
		if yearStr := getRawString(raw, "TDRC", "TYER", "TDOR", "©day", "\xa9day"); yearStr != "" {
			if parsedYear, convErr := strconv.Atoi(extractYearDigits(yearStr)); convErr == nil {
				metadata.Year = parsedYear
				yearSource = "raw.year"
			}
		}
	}
	if metadata.Year != 0 && yearSource != "" {
		setFieldSource(fieldSources, "year", yearSource)
	}

	// Track / disc position — the dhowden tag interface exposes these directly;
	// fall back to raw frames for parsers that don't. Both "N" and "N/Total"
	// forms are handled by parseSlashPair.
	metadata.TrackNumber, metadata.TrackTotal = m.Track()
	if metadata.TrackNumber == 0 {
		if n, total := parseSlashPair(getRawString(raw, "TRCK", "trkn", "track", "tracknumber", "TRACKNUMBER")); n != 0 {
			metadata.TrackNumber, metadata.TrackTotal = n, total
		}
	}
	metadata.DiscNumber, metadata.DiscTotal = m.Disc()
	if metadata.DiscNumber == 0 {
		if n, total := parseSlashPair(getRawString(raw, "TPOS", "disk", "disc", "discnumber", "DISCNUMBER")); n != 0 {
			metadata.DiscNumber, metadata.DiscTotal = n, total
		}
	}
	if metadata.TrackNumber != 0 {
		setFieldSource(fieldSources, "track", "tag.Track")
	}

	// Grouping (box-set/anthology divider): ID3 TIT1/GRP1, MP4 ©grp.
	metadata.Grouping = cleanTagValue(getRawString(raw, "TIT1", "GRP1", "©grp", "\xa9grp", "grouping", "GROUPING"))

	seriesValue, seriesSource := pickFirstNonEmpty(
		fieldCandidate{value: getRawString(raw, "TXXX:SERIES", "SERIES", "SERIES_NAME", "SERIESNAME", "MVNM", "GRP1", "TGID", "©grp", "\xa9grp"), source: "raw.series"},
	)

	// Read series index from custom tags (SERIES_INDEX, MVIN)
	if seriesIdxStr := getRawString(raw, "SERIES_INDEX", "MVIN", "TXXX:SERIES_INDEX"); seriesIdxStr != "" {
		if idx, err := strconv.Atoi(strings.TrimSpace(seriesIdxStr)); err == nil && idx > 0 {
			metadata.SeriesIndex = idx
			seriesIndexSource = "raw.series_index"
			setFieldSource(fieldSources, "series_index", seriesIndexSource)
		}
	}
	metadata.Series = cleanTagValue(seriesValue)
	if metadata.Series != "" {
		setFieldSource(fieldSources, "series", seriesSource)
	}
	if metadata.Series == "" && strings.Contains(metadata.Album, " - ") {
		parts := strings.Split(metadata.Album, " - ")
		if len(parts) > 1 {
			metadata.Series = strings.TrimSpace(parts[0])
			seriesSource = "album-prefix"
			setFieldSource(fieldSources, "series", seriesSource)
		}
	}
	if metadata.Series == "" && metadata.Comments != "" {
		if extracted := extractSeriesFromComments(metadata.Comments); extracted != "" {
			metadata.Series = extracted
			seriesSource = "comment-extraction"
			setFieldSource(fieldSources, "series", seriesSource)
		}
	}
	if metadata.Series == "" {
		if series, idx := extractSeriesFromVolumeString(metadata.Album); series != "" {
			metadata.Series = series
			seriesSource = "album-volume"
			setFieldSource(fieldSources, "series", seriesSource)
			if metadata.SeriesIndex == 0 && idx > 0 {
				metadata.SeriesIndex = idx
				seriesIndexSource = "album-volume"
			}
		}
	}
	if metadata.Series == "" {
		if series, idx := extractSeriesFromVolumeString(metadata.Title); series != "" {
			metadata.Series = series
			seriesSource = "title-volume"
			setFieldSource(fieldSources, "series", seriesSource)
			if metadata.SeriesIndex == 0 && idx > 0 {
				metadata.SeriesIndex = idx
				seriesIndexSource = "title-volume"
			}
		}
	}
	if metadata.Series == "" {
		if metadata.Album != "" {
			metadata.Series = metadata.Album
			seriesSource = "album fallback"
			setFieldSource(fieldSources, "series", seriesSource)
		} else if metadata.Title != "" {
			metadata.Series = metadata.Title
			seriesSource = "title fallback"
			setFieldSource(fieldSources, "series", seriesSource)
		}
	}

	if metadata.SeriesIndex == 0 {
		metadata.SeriesIndex = DetectVolumeNumber(metadata.Title)
		if metadata.SeriesIndex > 0 {
			seriesIndexSource = "title"
		}
	}
	if metadata.SeriesIndex == 0 {
		if idx := DetectVolumeNumber(metadata.Album); idx > 0 {
			metadata.SeriesIndex = idx
			seriesIndexSource = "album"
		}
	}
	if metadata.SeriesIndex == 0 && metadata.Comments != "" {
		if idx := DetectVolumeNumber(metadata.Comments); idx > 0 {
			metadata.SeriesIndex = idx
			seriesIndexSource = "comment"
		}
	}

	if metadata.Title == "" || metadata.Artist == "" || metadata.Narrator == "" || metadata.Series == "" {
		fallback := extractFromFilename(filePath)
		fallbackUsed = true
		if metadata.Title == "" && fallback.Title != "" {
			metadata.Title = fallback.Title
			setFieldSource(fieldSources, "title", "filename pattern")
		}
		if metadata.Artist == "" && fallback.Artist != "" {
			metadata.Artist = fallback.Artist
			setFieldSource(fieldSources, "author", "filename pattern")
		}
		if metadata.Narrator == "" && fallback.Narrator != "" {
			metadata.Narrator = fallback.Narrator
			setFieldSource(fieldSources, "narrator", "filename pattern")
		}
		if metadata.Series == "" && fallback.Series != "" {
			metadata.Series = fallback.Series
			setFieldSource(fieldSources, "series", "filename pattern")
		}
		if metadata.SeriesIndex == 0 && fallback.SeriesIndex > 0 {
			metadata.SeriesIndex = fallback.SeriesIndex
			seriesIndexSource = "filename pattern"
		}
	}

	if metadata.ISBN13 == "" {
		isbn13Value, isbn13Source := pickFirstNonEmpty(
			fieldCandidate{value: getRawString(raw, "TXXX:ISBN13", "TXXX:ISBN", "ISBN13", "ISBN"), source: "raw.isbn13"},
		)
		metadata.ISBN13 = cleanTagValue(isbn13Value)
		if metadata.ISBN13 != "" {
			setFieldSource(fieldSources, "isbn13", isbn13Source)
		}
	}
	if metadata.ISBN10 == "" {
		isbn10Value, isbn10Source := pickFirstNonEmpty(
			fieldCandidate{value: getRawString(raw, "TXXX:ISBN10", "ISBN10"), source: "raw.isbn10"},
		)
		metadata.ISBN10 = cleanTagValue(isbn10Value)
		if metadata.ISBN10 != "" {
			setFieldSource(fieldSources, "isbn10", isbn10Source)
		}
	}

	// Read custom AUDIOBOOK_ORGANIZER_* tags (TXXX frames for MP3, plain keys for FLAC/M4B)
	metadata.BookOrganizerID = cleanTagValue(getRawString(raw, "TXXX:"+TagBookID, TagBookID))
	metadata.OrganizerTagVersion = cleanTagValue(getRawString(raw, "TXXX:"+TagVersion, TagVersion))
	metadata.ASIN = cleanTagValue(getRawString(raw, "TXXX:"+TagASIN, TagASIN))
	metadata.OpenLibraryID = cleanTagValue(getRawString(raw, "TXXX:"+TagOpenLibrary, TagOpenLibrary))
	metadata.HardcoverID = cleanTagValue(getRawString(raw, "TXXX:"+TagHardcover, TagHardcover))
	metadata.GoogleBooksID = cleanTagValue(getRawString(raw, "TXXX:"+TagGoogleBooks, TagGoogleBooks))
	metadata.Edition = cleanTagValue(getRawString(raw, "TXXX:"+TagEdition, TagEdition))
	metadata.PrintYear = cleanTagValue(getRawString(raw, "TXXX:"+TagPrintYear, TagPrintYear))
	metadata.AuthorSource = sourceOrUnknown(fieldSources, "author")

	if seriesIndexSource == "" && metadata.SeriesIndex > 0 {
		seriesIndexSource = "detected"
	}
	if fallbackUsed {
		metaLog.Trace("filename fallback filled gaps for %s", filePath)
	}
	metadata.UsedFilenameFallback = fallbackUsed
	metaLog.Trace(
		"sources for %s | title=%s | author=%s | series=%s | series_index=%s | narrator=%s | album=%s | publisher=%s | language=%s",
		filePath,
		sourceOrUnknown(fieldSources, "title"),
		sourceOrUnknown(fieldSources, "author"),
		sourceOrUnknown(fieldSources, "series"),
		seriesIndexSource,
		sourceOrUnknown(fieldSources, "narrator"),
		sourceOrUnknown(fieldSources, "album"),
		sourceOrUnknown(fieldSources, "publisher"),
		sourceOrUnknown(fieldSources, "language"),
	)

	metaLog.Debug("extracted for %s (title=%q author=%q series=%q position=%d)", filePath, metadata.Title, metadata.Artist, metadata.Series, metadata.SeriesIndex)
	return metadata
}

func cleanTagValue(value string) string {
	// Strip null bytes and other control characters that appear in some M4B tags
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, value)
	return strings.TrimSpace(cleaned)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// parseSlashPair parses an iTunes/ID3 track-or-disc value of the form "N" or
// "N/Total" (e.g. "15", "15/47") into (number, total). Returns (0, 0) when the
// leading component is not a positive integer.
func parseSlashPair(s string) (number, total int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0
	}
	parts := strings.SplitN(s, "/", 2)
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return 0, 0
	}
	if len(parts) == 2 {
		if t, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && t > 0 {
			total = t
		}
	}
	return n, total
}

func getRawString(raw map[string]interface{}, keys ...string) string {
	if raw == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if normalized := normalizeRawTagValue(value); normalized != "" {
				return normalized
			}
		}
		for actualKey, actualValue := range raw {
			if strings.EqualFold(actualKey, key) {
				if normalized := normalizeRawTagValue(actualValue); normalized != "" {
					return normalized
				}
			}
			if comm, ok := actualValue.(*tag.Comm); ok {
				if strings.EqualFold(comm.Description, key) {
					if normalized := strings.TrimSpace(comm.Text); normalized != "" {
						return normalized
					}
				}
			}
			if comm, ok := actualValue.(tag.Comm); ok {
				if strings.EqualFold(comm.Description, key) {
					if normalized := strings.TrimSpace(comm.Text); normalized != "" {
						return normalized
					}
				}
			}
		}
	}
	return ""
}

func normalizeRawTagValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []string:
		for _, s := range typed {
			if trimmed := strings.TrimSpace(s); trimmed != "" && !looksLikeReleaseGroupTag(trimmed) {
				return trimmed
			}
		}
		for _, s := range typed {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				return trimmed
			}
		}
	case []byte:
		if s := strings.TrimSpace(string(typed)); s != "" {
			return s
		}
	case *tag.Comm:
		if typed != nil {
			if s := strings.TrimSpace(typed.Text); s != "" {
				return s
			}
		}
	case tag.Comm:
		if s := strings.TrimSpace(typed.Text); s != "" {
			return s
		}
	default:
		if s := strings.TrimSpace(fmt.Sprint(typed)); s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}

func extractSeriesFromComments(comment string) string {
	seriesMatches := []string{"Series:", "Series :", "Part of:"}
	for _, marker := range seriesMatches {
		if strings.Contains(comment, marker) {
			parts := strings.Split(comment, marker)
			if len(parts) > 1 {
				section := strings.TrimSpace(parts[1])
				end := strings.IndexAny(section, "\n\r.,;")
				if end > 0 {
					return section[:end]
				}
				return section
			}
		}
	}
	return ""
}

func looksLikeReleaseGroupTag(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && len(trimmed) > 2 {
		return true
	}
	return false
}

func extractYearDigits(value string) string {
	if match := yearPattern.FindStringSubmatch(value); len(match) > 1 {
		return match[1]
	}
	return value
}

// extractFromFilename tries to extract metadata from filename when tags are unavailable
func extractFromFilename(filePath string) Metadata {
	var metadata Metadata

	filename := filepath.Base(filePath)
	// Remove extension
	filename = strings.TrimSuffix(filename, filepath.Ext(filename))

	// Remove leading track/chapter/disc prefixes
	// "Track 01 - Title", "Track01 - Title"
	trackWordRe := regexp.MustCompile(`(?i)^[Tt]rack\s*\d+\s*[-–._]\s*`)
	filename = trackWordRe.ReplaceAllString(filename, "")
	// "Disc 1 - Title", "Disc01 - Title"
	discWordRe := regexp.MustCompile(`(?i)^[Dd]is[ck]\s*\d+\s*[-–._]\s*`)
	filename = discWordRe.ReplaceAllString(filename, "")
	// "01 - Title", "01. Title", "001-Title"
	numDashRe := regexp.MustCompile(`^\d{1,3}\s*[-–._]\s*`)
	filename = numDashRe.ReplaceAllString(filename, "")
	// "01 Title" (bare number prefix)
	parts := strings.Split(filename, " ")
	if len(parts) > 1 {
		if _, err := strconv.Atoi(parts[0]); err == nil {
			filename = strings.Join(parts[1:], " ")
		}
	}
	filename = strings.TrimSpace(filename)

	// Remove chapter info from end (e.g., "Title-10 Chapter 10" -> "Title")
	re := regexp.MustCompile(`(?i)[-_]\d+\s+Chapter\s+\d+$`)
	filename = re.ReplaceAllString(filename, "")

	// Try underscore separator first (for Author_Title patterns)
	if strings.Contains(filename, "_") && !strings.Contains(filename, " - ") {
		parts := strings.SplitN(filename, "_", 2)
		if len(parts) == 2 {
			left := strings.TrimSpace(parts[0])
			right := strings.TrimSpace(parts[1])
			if looksLikePersonName(right) && !looksLikePersonName(left) {
				metadata.Title = left
				metadata.Artist = right
				return metadata
			} else if looksLikePersonName(left) && !looksLikePersonName(right) {
				metadata.Title = right
				metadata.Artist = left
				return metadata
			}
		}
	}

	// Try to parse "Title - Author" or "Author - Title" patterns
	if strings.Contains(filename, " - ") {
		title, author := parseFilenameForAuthor(filename)
		if author != "" {
			metadata.Title = title
			metadata.Artist = author
		} else {
			// Fallback to old behavior if we can't determine author
			parts := strings.Split(filename, " - ")
			if len(parts) >= 2 {
				metadata.Series = parts[0]
				metadata.Title = parts[len(parts)-1]
			} else {
				metadata.Title = filename
			}
		}
	} else {
		metadata.Title = filename
	}

	// If we still don't have an artist, try to get from parent directory
	if metadata.Artist == "" {
		metadata.Artist = extractAuthorFromDirectory(filePath)
	}

	if metadata.SeriesIndex == 0 {
		metadata.SeriesIndex = DetectVolumeNumber(metadata.Title)
	}

	return metadata
}

// extractAuthorFromDirectory extracts author from directory name with validation
func extractAuthorFromDirectory(filePath string) string {
	dir := filepath.Dir(filePath)
	dirName := filepath.Base(dir)

	// Skip common non-author directory names
	skipDirs := map[string]bool{
		"books": true, "audiobooks": true, "newbooks": true, "downloads": true,
		"media": true, "audio": true, "library": true, "collection": true,
		"bt": true, "incomplete": true, "data": true,
	}

	if skipDirs[strings.ToLower(dirName)] {
		return ""
	}

	// Handle complex directory patterns like "Author, Co-Author - translator - Title"
	if strings.Contains(dirName, " - translator - ") || strings.Contains(dirName, " - narrated by - ") {
		re := regexp.MustCompile(`^([^-]+)\s*-\s*(?:translator|narrated by)\s*-`)
		matches := re.FindStringSubmatch(dirName)
		if len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
	}

	// Extract author from "Author - Title" directory pattern
	if strings.Contains(dirName, " - ") {
		parts := strings.SplitN(dirName, " - ", 2)
		if len(parts) > 0 {
			author := strings.TrimSpace(parts[0])
			if looksLikePersonName(author) {
				return author
			}
		}
	}

	// Use directory name if it's valid
	if looksLikePersonName(dirName) {
		return dirName
	}

	return ""
}

// isValidAuthor checks if extracted author string is valid
func isValidAuthor(author string) bool {
	if author == "" {
		return false
	}

	author = strings.ToLower(author)

	// Skip invalid patterns
	if strings.HasPrefix(author, "book") || strings.HasPrefix(author, "chapter") ||
		strings.HasPrefix(author, "part") || strings.HasPrefix(author, "vol") ||
		strings.HasPrefix(author, "volume") || strings.HasPrefix(author, "disc") {
		return false
	}

	// Skip purely numeric (like "01", "02")
	if _, err := strconv.Atoi(author); err == nil {
		return false
	}

	// Skip chapter patterns
	if strings.HasPrefix(author, "chapter ") {
		return false
	}

	return true
} // parseFilenameForAuthor attempts to intelligently parse title and author from filename
// Handles patterns like "Title - Author" or "Author - Title"
// Returns (title, author) where author is empty string if pattern not detected
func parseFilenameForAuthor(filename string) (string, string) {
	parts := strings.Split(filename, " - ")
	if len(parts) != 2 {
		return "", "" // Not a simple two-part pattern
	}

	left := strings.TrimSpace(parts[0])
	right := strings.TrimSpace(parts[1])

	// Heuristic: check if right side looks like an author name
	rightIsName := looksLikePersonName(right)
	leftIsName := looksLikePersonName(left)

	if rightIsName && !leftIsName {
		// Pattern: "Title - Author"
		return left, right
	} else if leftIsName && !rightIsName {
		// Pattern: "Author - Title"
		return right, left
	} else if leftIsName && rightIsName {
		leftHasInitials := strings.Contains(left, ".")
		rightHasInitials := strings.Contains(right, ".")
		if leftHasInitials && !rightHasInitials {
			return right, left
		}
		if rightHasInitials && !leftHasInitials {
			return left, right
		}
		// Both could be names, prefer "Title - Author" pattern
		return left, right
	} else if isTitleCaseCandidate(left) && isTitleCaseCandidate(right) {
		// Both are capitalized words; assume "Title - Author"
		return left, right
	}

	// Couldn't determine, return empty author
	return "", ""
}

// looksLikePersonName checks if a string looks like a person's name
// Looks for patterns like "John Smith", "J. Smith", "J. K. Rowling"
func looksLikePersonName(s string) bool {
	if !isValidAuthor(s) {
		return false
	}

	// Check for initials like "J. K. Rowling" or "J.K. Rowling"
	if strings.Contains(s, ".") {
		// Count uppercase letters and periods
		uppers := 0
		for _, r := range s {
			if r >= 'A' && r <= 'Z' {
				uppers++
			}
		}
		if uppers >= 2 {
			return true
		}
	}

	// Check for multi-word names with proper capitalization
	words := strings.Fields(s)
	if len(words) < 2 || len(words) > 4 {
		return false
	}
	for _, word := range words {
		if len(word) == 0 || (word[0] < 'A' || word[0] > 'Z') {
			return false
		}
	}
	return true
}

// isTitleCaseCandidate reports whether a string starts with an uppercase letter.
func isTitleCaseCandidate(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	return trimmed[0] >= 'A' && trimmed[0] <= 'Z'
}

// ExtractCoverArt extracts embedded cover art from an audio file and saves it
// to the .covers directory. Returns the path to the saved file, or empty string
// if no cover art is found.
func ExtractCoverArt(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("error opening file: %w", err)
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return "", fmt.Errorf("error reading tags: %w", err)
	}

	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return "", nil
	}

	// Determine extension from MIME type
	ext := ".jpg"
	switch pic.MIMEType {
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}

	// Hash the image data for dedup
	hash := fmt.Sprintf("%x", sha256.Sum256(pic.Data))

	coverDir := filepath.Join(config.AppConfig.RootDir, ".covers")
	if err := os.MkdirAll(coverDir, 0775); err != nil {
		return "", fmt.Errorf("failed to create covers directory: %w", err)
	}

	coverPath := filepath.Join(coverDir, hash+ext)

	// Skip if already cached
	if _, err := os.Stat(coverPath); err == nil {
		return coverPath, nil
	}

	if err := os.WriteFile(coverPath, pic.Data, 0664); err != nil {
		return "", fmt.Errorf("failed to write cover: %w", err)
	}

	// Cover art extraction log — use a default logger since this function
	// doesn't accept a logger parameter (callers are few and non-critical).
	logger.New("metadata").Debug("extracted cover art from %s → %s", filePath, coverPath)
	return coverPath, nil
}

// ExtractCoverArtBytes extracts embedded cover art from an audio file and returns
// the raw image bytes and MIME type. Returns nil data if no cover art is found.
func ExtractCoverArtBytes(filePath string) (data []byte, mimeType string, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, "", fmt.Errorf("error opening file: %w", err)
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil, "", fmt.Errorf("error reading tags: %w", err)
	}

	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, "", nil
	}

	mime := pic.MIMEType
	if mime == "" {
		mime = "image/jpeg"
	}
	return pic.Data, mime, nil
}
