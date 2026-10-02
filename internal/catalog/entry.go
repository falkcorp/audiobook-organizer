// file: internal/catalog/entry.go
// version: 1.1.0
// guid: 4e7a2c91-6b5d-4f08-a3e1-9c2d8f6b1a74
// last-edited: 2026-10-01

package catalog

import (
	"regexp"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

var (
	dramatizedRe = regexp.MustCompile(`(?i)\bdramati[sz](?:ed|ation)\b|\bradio\s+(?:drama|play)\b|\baudio\s+drama\b`)
	fullCastRe   = regexp.MustCompile(`(?i)\bfull[\s-]+cast\b`)
)

// EditionKind classifies a product (owner answer Q1, 2026-10-01). Title and
// subtitle markers OVERRIDE format_type: Audible files most dramatizations
// and full-cast productions as "original_recording" or even "unabridged"
// (verified 2026-10-01: Gaiman's The Sandman is original_recording), and only
// an unabridged entry may later fill a series gap, so a dramatization must
// never be read as unabridged.
//
// "original_recording" with no marker is unknown, not unabridged: Audible
// Originals range from single-narrator novels to full-cast productions, and
// unknown is the side that does not wrongly fill a gap.
func EditionKind(formatType, title, subtitle string) string {
	text := title + " " + subtitle
	switch {
	case dramatizedRe.MatchString(text):
		return database.EditionDramatized
	case fullCastRe.MatchString(text):
		return database.EditionFullCast
	}
	switch strings.ToLower(strings.TrimSpace(formatType)) {
	case "unabridged":
		return database.EditionUnabridged
	case "abridged":
		return database.EditionAbridged
	}
	return database.EditionUnknown
}

// IsManualOnly reports whether a product belongs to the owner's manual-only
// libraries (Doctor Who / Big Finish / Torchwood, R13), using the same
// pattern set every bulk-apply path uses.
func IsManualOnly(p metadata.CatalogProduct) bool {
	if applygate.IsOwnerManualOnly(p.Title+" "+p.Subtitle, "") || applygate.IsOwnerManualOnly(p.Publisher, "") {
		return true
	}
	for _, s := range p.Series {
		if applygate.IsOwnerManualOnly("", s.Title) {
			return true
		}
	}
	return false
}

// BuildEntry converts a provider product into a catalog entry (no id, no
// group id: the store assigns both). marketplace and provider come from the
// harvest's configuration.
func BuildEntry(p metadata.CatalogProduct, provider, marketplace string) database.CatalogEntry {
	e := database.CatalogEntry{
		Provider:    provider,
		ProviderID:  p.ASIN,
		Marketplace: marketplace,
		Language:    p.Language,
		Title:       p.Title,
		Subtitle:    p.Subtitle,
		Publisher:   p.Publisher,
		RuntimeMin:  p.RuntimeMin,
		ReleaseDate: p.ReleaseDate,
		Format:      p.ContentDeliveryType,
		FormatType:  p.FormatType,
		EditionKind: EditionKind(p.FormatType, p.Title, p.Subtitle),
		CoverURL:    p.CoverURL,
		ManualOnly:  IsManualOnly(p),
	}
	for _, a := range p.Authors {
		e.Authors = append(e.Authors, database.CatalogEntryAuthor{Name: a.Name, ProviderAuthorID: a.ASIN})
		for _, k := range []string{AuthorASINKey(a.ASIN), AuthorNameKey(a.Name)} {
			if k != "" && !slices.Contains(e.AuthorKeys, k) {
				e.AuthorKeys = append(e.AuthorKeys, k)
			}
		}
	}
	for _, n := range p.Narrators {
		if n.Name != "" {
			e.Narrators = append(e.Narrators, n.Name)
		}
	}
	for _, s := range p.Series {
		ser := database.CatalogEntrySeries{Name: s.Title, ProviderSeriesID: s.ASIN, Sequence: s.Sequence}
		if lo, hi, ok := ParseSequence(s.Sequence); ok {
			ser.SeqLo, ser.SeqHi = &lo, &hi
		}
		e.Series = append(e.Series, ser)
		if k := SeriesKey(s.Title); k != "" && !slices.Contains(e.SeriesKeys, k) {
			e.SeriesKeys = append(e.SeriesKeys, k)
		}
	}
	// The group key comes from the PRODUCT's first credited author, never the
	// author being harvested: a co-authored title reached through either
	// author must land in one group.
	if len(p.Authors) > 0 {
		e.EditionGroupKey = EditionGroupKey(p.Authors[0].Name, p.Title)
	}
	return e
}
