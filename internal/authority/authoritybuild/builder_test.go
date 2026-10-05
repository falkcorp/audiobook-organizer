// file: internal/authority/authoritybuild/builder_test.go
// version: 1.1.0
// guid: 4b1eb286-eaec-4b35-9e85-3ce90684f4d1
// last-edited: 2026-10-05

package authoritybuild

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func c(name, asin string) metadata.CatalogContributor {
	return metadata.CatalogContributor{Name: name, ASIN: asin}
}

func prod(asin string, authors, narrators []metadata.CatalogContributor) metadata.CatalogProduct {
	return metadata.CatalogProduct{ASIN: asin, Title: "Some Novel", FormatType: "unabridged", Authors: authors, Narrators: narrators, Publisher: "Podium Audio"}
}

func TestFold_IgnoresPunctuationAndCase(t *testing.T) {
	require.Equal(t, authority.Fold("J.N. Chaney"), authority.Fold("j n chaney"))
	require.Equal(t, "", authority.Fold(" ... "))
	require.Equal(t, authority.PersonPrefix+"jnchaney", authority.PersonKey("J. N. Chaney"))
}

func TestBuilder_HomonymIsReportedNeverPicked(t *testing.T) {
	b := NewBuilder()
	b.AddProduct(authority.SourceCatalog, "", prod("P1", []metadata.CatalogContributor{c("John Smith", "B000000001")}, nil))
	b.AddProduct(authority.SourceCatalog, "", prod("P2", []metadata.CatalogContributor{c("John Smith", "B000000002")}, nil))
	res := b.Finish()
	p := res.Persons["johnsmith"]
	require.NotNil(t, p)
	require.True(t, p.HomonymASINs)
	require.Equal(t, []string{"B000000001", "B000000002"}, p.ASINs)
	require.Equal(t, 1, res.Report.Homonyms)
	require.Equal(t, []string{"johnsmith"}, res.ASINs["B000000001"].Folds)
	require.Equal(t, []string{"johnsmith"}, res.ASINs["B000000002"].Folds)
}

func TestBuilder_SpellingVariantsShareAnASIN(t *testing.T) {
	b := NewBuilder()
	b.AddProduct(authority.SourceCatalog, "", prod("P1", []metadata.CatalogContributor{c("Jonathan Brazee", "B00AAAAAAA")}, nil))
	b.AddProduct(authority.SourceCatalog, "", prod("P2", []metadata.CatalogContributor{c("Jonathan P. Brazee", "B00AAAAAAA")}, nil))
	res := b.Finish()
	require.Equal(t, []string{"jonathanbrazee", "jonathanpbrazee"}, res.ASINs["B00AAAAAAA"].Folds)
	require.Equal(t, 1, res.Report.SpellingASINs)
	require.False(t, res.Persons["jonathanbrazee"].HomonymASINs)
}

func TestBuilder_TierPrecedenceAndAuthorEvidence(t *testing.T) {
	b := NewBuilder()
	b.AddProduct(authority.SourceCatalog, "", prod("P1", []metadata.CatalogContributor{c("Ann Leckie", "B001JP7W9E"), c("No Asin", "")}, []metadata.CatalogContributor{c("Adjoa Andoh", "")}))
	res := b.Finish()
	require.Equal(t, authority.TierA, res.Persons["annleckie"].Tier)
	require.Equal(t, authority.TierB, res.Persons["noasin"].Tier)
	require.True(t, authority.AuthorEvidenceRule(res.Persons["annleckie"].Roles[authority.RoleAuthor]), "one tier-A credit is evidence")
	require.False(t, authority.AuthorEvidenceRule(res.Persons["noasin"].Roles[authority.RoleAuthor]), "one tier-B credit is not")
	require.True(t, authority.NarratorEvidenceRule(res.Persons["adjoaandoh"].Roles[authority.RoleNarrator]))

	// A second tier-B credit makes it evidence; an owner-library credit
	// raises the tier to O.
	b = NewBuilder()
	b.AddProduct(authority.SourceCatalog, "", prod("P1", []metadata.CatalogContributor{c("No Asin", "")}, nil))
	b.AddProduct(authority.SourceCatalog, "", prod("P2", []metadata.CatalogContributor{c("No Asin", "")}, nil))
	b.AddProduct(authority.SourceLibraryExport, "", prod("P3", []metadata.CatalogContributor{c("No Asin", "")}, nil))
	res = b.Finish()
	st := res.Persons["noasin"].Roles[authority.RoleAuthor]
	require.Equal(t, authority.TierO, st.Tier)
	require.Equal(t, 3, st.Count)
	require.Equal(t, map[authority.Tier]int{authority.TierB: 2, authority.TierO: 1}, st.ByTier)
	require.Equal(t, authority.TierO, res.Persons["noasin"].Tier)
	require.Equal(t, []string{authority.SourceCatalog, authority.SourceLibraryExport}, res.Persons["noasin"].Sources)
	require.Equal(t, authority.TierO, res.Publishers["podiumaudio"].Tier)
	require.Equal(t, 3, res.Publishers["podiumaudio"].Count)
	require.Equal(t, 1, res.Report.AuthorEvidence)
}

func TestBuilder_CastContextIsCastAuthorOnly(t *testing.T) {
	four := []metadata.CatalogContributor{c("Aa Bb", ""), c("Cc Dd", ""), c("Ee Ff", ""), c("Gg Hh", "")}
	cases := map[string]metadata.CatalogProduct{
		"more than 3 authors": prod("P1", four, nil),
		"big finish publisher": func() metadata.CatalogProduct {
			p := prod("P2", []metadata.CatalogContributor{c("Aa Bb", "")}, nil)
			p.Publisher = "Big Finish Productions"
			return p
		}(),
		"dramatized title": func() metadata.CatalogProduct {
			p := prod("P3", []metadata.CatalogContributor{c("Aa Bb", "")}, nil)
			p.Subtitle = "A Full-Cast Dramatization"
			return p
		}(),
		"doctor who series": func() metadata.CatalogProduct {
			p := prod("P4", []metadata.CatalogContributor{c("Aa Bb", "")}, nil)
			p.Series = []metadata.CatalogProductSeries{{Title: "Doctor Who: The Monthly Adventures"}}
			return p
		}(),
	}
	for name, p := range cases {
		require.True(t, IsCastContext(p), name)
		b := NewBuilder()
		b.AddProduct(authority.SourceCatalog, "", p)
		res := b.Finish()
		pe := res.Persons["aabb"]
		require.NotNil(t, pe, name)
		_, author := pe.Roles[authority.RoleAuthor]
		require.False(t, author, "%s: a cast member must never be author evidence", name)
		require.Equal(t, 1, pe.Roles[authority.RoleCastAuthor].Count, name)
		require.Positive(t, res.Report.CastOnly, name)
		require.Equal(t, 1, res.Report.Sources[authority.SourceCatalog].CastContext, name)
	}
	require.False(t, IsCastContext(prod("P5", four[:3], nil)))
}

func TestBuilder_SkipsCollectiveDuplicatesAndEmpty(t *testing.T) {
	b := NewBuilder()
	b.AddProduct(authority.SourceCatalog, "", prod("P1", []metadata.CatalogContributor{c("Full Cast", "")}, []metadata.CatalogContributor{c("Various Narrators", "")}))
	b.AddProduct(authority.SourceCatalog, "", prod("p1", []metadata.CatalogContributor{c("Ann Leckie", "")}, nil)) // same ASIN, other case
	b.AddProduct(authority.SourceCatalog, "", prod("", []metadata.CatalogContributor{c("Ann Leckie", "")}, nil))
	res := b.Finish()
	require.Empty(t, res.Persons, "collective credits are never persons; the duplicate and id-less products add nothing")
	st := res.Report.Sources[authority.SourceCatalog]
	require.Equal(t, 1, st.Items)
	require.Equal(t, 1, st.Duplicates)
	require.Equal(t, 1, st.Skipped)
}

func TestBuilder_SingleWordReportedAndSeedIngested(t *testing.T) {
	b := NewBuilder()
	b.AddSeed(&Seed{Source: authority.SourceSeed, Tier: authority.TierO, Entries: []SeedEntry{
		{Kind: SeedKindAuthor, Name: "Actus", ASINs: []string{"B09GS7Y8DF"}, AlsoNarrator: true},
		{Kind: SeedKindNarrator, Name: "Ray Porter"},
		{Kind: SeedKindPublisher, Name: "Podium Audio"},
	}})
	res := b.Finish()
	require.Equal(t, 1, res.Report.SingleWord)
	a := res.Persons["actus"]
	require.Equal(t, authority.TierO, a.Roles[authority.RoleAuthor].Tier)
	require.Equal(t, authority.TierO, a.Roles[authority.RoleNarrator].Tier)
	require.Equal(t, []string{"B09GS7Y8DF"}, a.ASINs)
	_, author := res.Persons["rayporter"].Roles[authority.RoleAuthor]
	require.False(t, author)
	require.Equal(t, 0, res.Publishers["podiumaudio"].Count, "the seed contributes no products")
	require.Len(t, res.Ledger, 3)
}

func TestReadLibraryExport_ListObjectAndNulls(t *testing.T) {
	item := `{"asin":"P1","title":"T","authors":[{"name":"Ann Leckie","asin":null}],"narrators":null,"series":null,"publisher_name":"Orbit","rating":null}`
	for _, doc := range []string{"[" + item + "]", `{"items":[` + item + `]}`} {
		items, err := ReadLibraryExport(stringsReader(doc), MaxLibraryExportBytes)
		require.NoError(t, err)
		require.Len(t, items, 1)
		b := NewBuilder()
		require.NoError(t, b.AddRawProduct(authority.SourceLibraryExport, "", items[0]))
		res := b.Finish()
		require.Equal(t, authority.TierO, res.Persons["annleckie"].Tier)
		require.Empty(t, res.Persons["annleckie"].ASINs)
	}
	_, err := ReadLibraryExport(stringsReader(`{"nope":[]}`), MaxLibraryExportBytes)
	require.Error(t, err)
	_, err = ReadLibraryExport(stringsReader("["+item+"]"), 10)
	require.Error(t, err, "an oversized export is refused, never truncated")
	b := NewBuilder()
	require.Error(t, b.AddRawProduct(authority.SourceCatalog, "", []byte(`{"authors":"not a list"}`)))
	require.Equal(t, 1, b.Finish().Report.Sources[authority.SourceCatalog].Undecodable)
}

func TestBuilder_RoleSuffixedCreditsAreNeverAuthors(t *testing.T) {
	b := NewBuilder()
	b.AddProduct(authority.SourceCatalog, "", prod("P1",
		[]metadata.CatalogContributor{c("Cixin Liu", "B00AAAAAA1"), c("Ken Liu - translator", "B00AAAAAA2"), c("Ricardo Cortes - illustrator", "")},
		[]metadata.CatalogContributor{c("Read by Luke Daniels", ""), c("Robert J. Sawyer - introduction", "")}))
	res := b.Finish()
	require.Nil(t, res.Persons["kenliutranslator"], "the role suffix is stripped from the name")
	for _, f := range []string{"kenliu", "ricardocortes", "robertjsawyer"} {
		p := res.Persons[f]
		require.NotNil(t, p, f)
		require.Equal(t, []authority.Role{authority.RoleOther}, rolesOf(p), f)
	}
	require.Equal(t, []authority.Role{authority.RoleNarrator}, rolesOf(res.Persons["lukedaniels"]))
	require.Equal(t, []authority.Role{authority.RoleAuthor}, rolesOf(res.Persons["cixinliu"]))
	require.Equal(t, 1, res.Report.AuthorEvidence, "only the unmarked author is author evidence")
}

func rolesOf(p *authority.Person) []authority.Role {
	var out []authority.Role
	for r := range p.Roles {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// TestBuilder_DuplicateProductIsDeterministic: two payloads with one product
// ASIN (two marketplaces) keep the smaller tiebreak whatever the order the
// workers hand them in.
func TestBuilder_DuplicateProductIsDeterministic(t *testing.T) {
	us := prod("P1", []metadata.CatalogContributor{c("Ann Leckie", "B001JP7W9E")}, nil)
	uk := prod("P1", []metadata.CatalogContributor{c("Ann  Leckie", "B001JP7W9E")}, []metadata.CatalogContributor{c("Adjoa Andoh", "")})
	run := func(first, second func(*Builder)) *Result {
		b := NewBuilder()
		first(b)
		second(b)
		return b.Finish()
	}
	addUS := func(b *Builder) { b.AddProduct(authority.SourceCatalog, "cat_raw:A", us) }
	addUK := func(b *Builder) { b.AddProduct(authority.SourceCatalog, "cat_raw:B", uk) }
	r1, r2 := run(addUS, addUK), run(addUK, addUS)
	require.Equal(t, r1.Ledger, r2.Ledger)
	require.Nil(t, r1.Persons["adjoaandoh"], "cat_raw:A (smaller key) wins")
	require.Nil(t, r2.Persons["adjoaandoh"])
	require.Equal(t, 1, r1.Report.Sources[authority.SourceCatalog].Duplicates)
	require.Equal(t, 1, r1.Report.Sources[authority.SourceCatalog].Items)
}
