// file: internal/metadata/catalog_match.go
// version: 1.0.0
// guid: 7d1f4a52-3c8e-4b9a-a6d0-5e2b8c9f1a37
// last-edited: 2026-10-12

package metadata

// CatalogSourceName is the Source of a candidate read from the local author
// catalog by the B1/B2 blocking stage (internal/catalog.MatchBook). It is a
// REVIEW-ONLY source (metafetch.IsReviewOnlyCandidateSource): such a
// candidate is never applied unattended; only an owner review applies it
// (owner decision D31, catalog candidates start review-only).
//
// It lives here, not in internal/catalog, because the metadata search
// (internal/metafetch, a layer below the catalog) must recognise the name and
// cannot import the catalog.
const CatalogSourceName = "Audible Catalog (local)"

// Stages of origin of a candidate (spec 02 section 3.3, "B - Blocking").
const (
	CatalogStageIdentifier = "B1" // the book's own ASIN, a point read
	CatalogStageAuthor     = "B2" // an exact author block, title-scored
)

// CatalogQuestion is the book as the local catalog stage sees it. Credits is
// the book's ORDERED live credit list (database.LiveBookAuthorNames); it is
// never sorted, joined or de-duplicated by the stage.
type CatalogQuestion struct {
	// ASIN is the book's own ASIN ("" when unknown): the B1 point read.
	ASIN string
	// Provider and Marketplace name the catalog the B1 read goes to (the
	// harvest's own settings); empty disables B1.
	Provider    string
	Marketplace string
	// Titles are every title the book can be known by: the stored title, the
	// parsed one, the folder-parse stand-in. A catalog title matches when it
	// matches any of them.
	Titles []string
	// Credits are the author credits in order. AuthorASINs, when set, is
	// parallel to Credits: the author ASIN of Credits[i] if known ("" if not).
	Credits     []string
	AuthorASINs []string
	Series      string
	Position    string
}

// CatalogOrigin records where one catalog candidate came from.
type CatalogOrigin struct {
	// Stage is CatalogStageIdentifier or CatalogStageAuthor.
	Stage string
	// CreditPosition is the index in Credits of the credit whose block
	// produced a B2 candidate (0 for B1).
	CreditPosition int
	// ManualOnly marks an entry in the owner's manual-only libraries (Doctor
	// Who / Big Finish / Torchwood): still a candidate, but the apply gate
	// treats it as it treats any manual-only record.
	ManualOnly bool
}

// CatalogMatch is the local stage's answer. Candidates and Origins are
// parallel. Stage is "B1" when the identifier hit, "B2" when only author
// blocks produced candidates, "" when nothing did.
type CatalogMatch struct {
	Candidates []BookMetadata
	Origins    []CatalogOrigin
	Stage      string
	// OversizeBlocks counts credit blocks skipped for holding more entries
	// than the cap (a junk or franchise row); HomonymSkipped counts blocks
	// skipped because the folded name belongs to more than one provider
	// author and nothing local chose between them.
	OversizeBlocks int
	HomonymSkipped int
}
