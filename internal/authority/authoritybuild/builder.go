// file: internal/authority/authoritybuild/builder.go
// version: 1.2.0
// guid: 69528b02-de24-4411-8a5e-ac3831986a80
// last-edited: 2026-10-05

package authoritybuild

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/catalog"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// MaxPlainAuthors is the most authors a product may list before its author
// array is read as a cast list.
const MaxPlainAuthors = 3

const (
	maxVariants      = 10
	maxProductSample = 3
	reportSampleSize = 25
)

// SourceStats counts one source's ingest.
type SourceStats struct {
	// Items ingested (seed entries or products).
	Items int `json:"items"`
	// Duplicates: an item whose ledger id this run already ingested (the
	// same product ASIN twice in one source).
	Duplicates int `json:"duplicates"`
	// Skipped: an item with no usable id or no contributors.
	Skipped int `json:"skipped"`
	// Undecodable: a payload the decoder refused.
	Undecodable int `json:"undecodable"`
	// CastContext: products whose author array was read as a cast list.
	CastContext int `json:"cast_context"`
	// ManualOnly: products matching the owner's manual-only patterns.
	ManualOnly int `json:"manual_only"`
}

// NameSample is one reported name.
type NameSample struct {
	Fold    string         `json:"fold"`
	Display string         `json:"display"`
	ASINs   []string       `json:"asins,omitempty"`
	Folds   []string       `json:"folds,omitempty"`
	Tier    authority.Tier `json:"tier,omitempty"`
}

// Report is what a build found.
type Report struct {
	Sources          map[string]*SourceStats `json:"sources"`
	Persons          int                     `json:"persons"`
	Publishers       int                     `json:"publishers"`
	ASINs            int                     `json:"asins"`
	LedgerRows       int                     `json:"ledger_rows"`
	PersonsByTier    map[authority.Tier]int  `json:"persons_by_tier"`
	PublishersByTier map[authority.Tier]int  `json:"publishers_by_tier"`
	// PersonsByRole counts persons holding each role (a person may hold
	// several).
	PersonsByRole map[authority.Role]int `json:"persons_by_role"`
	// AuthorEvidence counts persons whose author role qualifies as evidence
	// (AuthorEvidenceRule).
	AuthorEvidence int `json:"author_evidence"`
	// CastOnly: persons whose only role is cast_author.
	CastOnly       int          `json:"cast_only"`
	CastOnlySample []NameSample `json:"cast_only_sample,omitempty"`
	// Homonyms: one fold, several contributor ASINs.
	Homonyms       int          `json:"homonyms"`
	HomonymSample  []NameSample `json:"homonym_sample,omitempty"`
	SpellingASINs  int          `json:"spelling_asins"`
	SpellingSample []NameSample `json:"spelling_sample,omitempty"`
	// SingleWord: persons whose display name is one word.
	SingleWord       int          `json:"single_word"`
	SingleWordSample []NameSample `json:"single_word_sample,omitempty"`
	PersonSample     []NameSample `json:"person_sample,omitempty"`
	PublisherSample  []NameSample `json:"publisher_sample,omitempty"`
}

// Result is a finished build: the rows an apply writes, plus the report.
type Result struct {
	Persons    map[string]*authority.Person
	Publishers map[string]*authority.Publisher
	ASINs      map[string]*authority.ASINRef
	// Ledger maps a ref_src: key to the digest of what that item contributed.
	Ledger map[string]string
	Report Report
}

type personAcc struct {
	displays map[string]int
	asins    map[string]bool
	roles    map[authority.Role]map[authority.Tier]int
	sources  map[string]bool
	products []string
}

type pubAcc struct {
	displays   map[string]int
	count      int
	manualOnly int
	tier       authority.Tier
	sources    map[string]bool
	products   []string
}

// Builder accumulates sources into a Result. It is safe for concurrent use:
// decode a payload outside it, then hand the product to AddProduct from any
// worker.
type Builder struct {
	mu      sync.Mutex
	persons map[string]*personAcc
	pubs    map[string]*pubAcc
	ledger  map[string]string
	sources map[string]*SourceStats
	// asinSources: which sources credited each contributor ASIN.
	asinSources map[string]map[string]bool
	// pending holds one product per ledger key until Finish. Products are
	// accumulated in Finish in key order, so the result never depends on
	// worker order, and of two payloads with the same product ASIN in one
	// source (two marketplaces) the one with the smaller tiebreak (its
	// cat_raw key, or its export position) is kept.
	pending map[string]*pendingProduct
}

type pendingProduct struct {
	source, id, tiebreak string
	d                    productDigest
	manual               bool
}

// NewBuilder returns an empty Builder.
func NewBuilder() *Builder {
	return &Builder{
		persons: map[string]*personAcc{},
		pubs:    map[string]*pubAcc{},
		ledger:  map[string]string{},
		sources: map[string]*SourceStats{},

		asinSources: map[string]map[string]bool{},
		pending:     map[string]*pendingProduct{},
	}
}

func (b *Builder) noteASIN(asin, source string) {
	if b.asinSources[asin] == nil {
		b.asinSources[asin] = map[string]bool{}
	}
	b.asinSources[asin][source] = true
}

func (b *Builder) stats(source string) *SourceStats {
	s := b.sources[source]
	if s == nil {
		s = &SourceStats{}
		b.sources[source] = s
	}
	return s
}

// NoteSource records that source ran, so a source that produced nothing (an
// empty catalog) still appears in the report with zero counts instead of
// being indistinguishable from a source that was skipped.
func (b *Builder) NoteSource(source string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats(source)
}

// NoteUndecodable counts a payload of source the decoder refused.
func (b *Builder) NoteUndecodable(source string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats(source).Undecodable++
}

func (b *Builder) person(fold string) *personAcc {
	p := b.persons[fold]
	if p == nil {
		p = &personAcc{displays: map[string]int{}, asins: map[string]bool{}, roles: map[authority.Role]map[authority.Tier]int{}, sources: map[string]bool{}}
		b.persons[fold] = p
	}
	return p
}

func (b *Builder) addPerson(source, name, asin, product string, role authority.Role, tier authority.Tier) {
	name = strings.Join(strings.Fields(name), " ")
	f := authority.Fold(name)
	if f == "" || authorcredit.IsCollectiveCredit(name) {
		return
	}
	p := b.person(f)
	p.displays[name]++
	if a := authority.NormalizeASIN(asin); a != "" {
		p.asins[a] = true
		b.noteASIN(a, source)
	}
	if p.roles[role] == nil {
		p.roles[role] = map[authority.Tier]int{}
	}
	p.roles[role][tier]++
	p.sources[source] = true
	if product != "" {
		p.products = addSample(p.products, product)
	}
}

func (b *Builder) addPublisher(source, name, product string, tier authority.Tier, manualOnly bool, isProduct bool) {
	name = strings.Join(strings.Fields(name), " ")
	f := authority.Fold(name)
	if f == "" {
		return
	}
	p := b.pubs[f]
	if p == nil {
		p = &pubAcc{displays: map[string]int{}, sources: map[string]bool{}}
		b.pubs[f] = p
	}
	p.displays[name]++
	p.tier = authority.Better(p.tier, tier)
	p.sources[source] = true
	if isProduct {
		p.count++
		if manualOnly {
			p.manualOnly++
		}
	}
	if product != "" {
		p.products = addSample(p.products, product)
	}
}

// addSample keeps the smallest maxProductSample product ids, so the sample
// does not depend on worker order.
func addSample(s []string, id string) []string {
	i := sort.SearchStrings(s, id)
	if i < len(s) && s[i] == id {
		return s
	}
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = id
	if len(s) > maxProductSample {
		s = s[:maxProductSample]
	}
	return s
}

// AddSeed ingests the seed. Every entry is tier O (the seed's own tier).
func (b *Builder) AddSeed(s *Seed) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.stats(authority.SourceSeed)
	for _, e := range s.Entries {
		f := authority.Fold(e.Name)
		id := e.Kind + ":" + f
		key := authority.SourceKey(authority.SourceSeed, id)
		if _, dup := b.ledger[key]; dup {
			st.Duplicates++
			continue
		}
		b.ledger[key] = digestOf(e)
		st.Items++
		switch e.Kind {
		case SeedKindPublisher:
			b.addPublisher(authority.SourceSeed, e.Name, "", s.Tier, false, false)
			continue
		case SeedKindAuthor:
			b.addSeedRoles(e, authority.RoleAuthor, e.AlsoNarrator, authority.RoleNarrator, s.Tier)
		case SeedKindNarrator:
			b.addSeedRoles(e, authority.RoleNarrator, false, "", s.Tier)
		}
	}
}

func (b *Builder) addSeedRoles(e SeedEntry, primary authority.Role, alsoOther bool, other authority.Role, tier authority.Tier) {
	b.addPerson(authority.SourceSeed, e.Name, "", "", primary, tier)
	acc := b.person(authority.Fold(e.Name))
	for _, a := range e.ASINs {
		if n := authority.NormalizeASIN(a); n != "" {
			acc.asins[n] = true
			b.noteASIN(n, authority.SourceSeed)
		}
	}
	if alsoOther {
		b.addPerson(authority.SourceSeed, e.Name, "", "", other, tier)
	}
}

// IsCastContext reports whether a product's author array is a cast list: a
// manual-only product (Doctor Who / Big Finish / Torchwood, the same pattern
// set every bulk-apply path uses), a dramatized or full-cast edition, or more
// than MaxPlainAuthors authors. It fails toward cast: a cast member read as an
// author would become author evidence, an author read as a cast member only
// loses evidence. scripts/gen_authority_seed.py ports this rule.
func IsCastContext(p metadata.CatalogProduct) bool {
	if len(p.Authors) > MaxPlainAuthors || catalog.IsManualOnly(p) {
		return true
	}
	switch catalog.EditionKind(p.FormatType, p.Title, p.Subtitle) {
	case database.EditionDramatized, database.EditionFullCast:
		return true
	}
	return false
}

// productTier is the tier of one contributor credit from source: the
// owner's library is O; a structured list entry is A with a contributor ASIN
// and B without one.
func productTier(source, asin string) authority.Tier {
	if source == authority.SourceLibraryExport {
		return authority.TierO
	}
	if authority.NormalizeASIN(asin) != "" {
		return authority.TierA
	}
	return authority.TierB
}

// credit is one classified contributor credit.
type credit struct {
	Name string         `json:"name"`
	ASIN string         `json:"asin,omitempty"`
	Role authority.Role `json:"role"`
}

// productDigest is what one product contributed, for the ledger.
type productDigest struct {
	Cast      bool     `json:"cast"`
	Credits   []credit `json:"credits"`
	Publisher string   `json:"publisher"`
}

// classifyCredit strips a role marker from a credit
// (metadata.ClassifyContributor: "Jane Doe - translator", "Read by Jane
// Doe") and returns the bare name with the role it is recorded under. A
// marked translator / illustrator / editor / introduction credit is
// RoleOther wherever it appears, never author evidence. In the author array
// an unmarked credit is an author (cast_author in a cast-context product); in
// the narrator array it is a narrator.
func classifyCredit(raw string, inAuthors, cast bool) (string, authority.Role) {
	name, role := metadata.ClassifyContributor(raw)
	switch role {
	case metadata.RoleOther:
		return name, authority.RoleOther
	case metadata.RoleNarrator:
		return name, authority.RoleNarrator
	}
	switch {
	case !inAuthors:
		return name, authority.RoleNarrator
	case cast:
		return name, authority.RoleCastAuthor
	}
	return name, authority.RoleAuthor
}

// AddProduct ingests one decoded product from source (SourceCatalog or
// SourceLibraryExport). tiebreak orders duplicates: of two payloads with the
// same product ASIN in one source, the smaller tiebreak wins, and an equal
// tiebreak means the same payload again (pass a unique tiebreak per payload:
// the cat_raw key, or the export item's zero-padded position). Only contributor
// names, contributor ASINs and the publisher are kept; the title, subtitle
// and series are read in memory for the cast decision and never stored.
// Nothing is accumulated until Finish, so the result is independent of the
// order workers call this in.
func (b *Builder) AddProduct(source, tiebreak string, p metadata.CatalogProduct) {
	id := authority.NormalizeASIN(p.ASIN)
	cast := IsCastContext(p)
	pp := &pendingProduct{source: source, id: id, tiebreak: tiebreak, manual: catalog.IsManualOnly(p),
		d: productDigest{Cast: cast, Publisher: strings.TrimSpace(p.Publisher)}}
	for _, a := range p.Authors {
		name, role := classifyCredit(a.Name, true, cast)
		pp.d.Credits = append(pp.d.Credits, credit{Name: name, ASIN: authority.NormalizeASIN(a.ASIN), Role: role})
	}
	for _, n := range p.Narrators {
		name, role := classifyCredit(n.Name, false, cast)
		pp.d.Credits = append(pp.d.Credits, credit{Name: name, ASIN: authority.NormalizeASIN(n.ASIN), Role: role})
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.stats(source)
	if id == "" || (len(pp.d.Credits) == 0 && pp.d.Publisher == "") {
		st.Skipped++
		return
	}
	key := authority.SourceKey(source, id)
	if old, dup := b.pending[key]; dup {
		// Duplicates counts payloads DROPPED: exactly one per collision of two
		// different payloads (the larger tiebreak). The same payload handed in
		// twice (same tiebreak) is not a second product and drops nothing.
		if tiebreak == old.tiebreak {
			return
		}
		st.Duplicates++
		if tiebreak > old.tiebreak {
			return
		}
	}
	b.pending[key] = pp
}

// accumulatePending folds the pending products into the accumulators, in
// ledger-key order. Called by Finish with b.mu held.
func (b *Builder) accumulatePending() {
	for _, key := range sortedKeys(b.pending) {
		pp := b.pending[key]
		st := b.stats(pp.source)
		b.ledger[key] = digestOf(pp.d)
		st.Items++
		if pp.d.Cast {
			st.CastContext++
		}
		if pp.manual {
			st.ManualOnly++
		}
		for _, c := range pp.d.Credits {
			b.addPerson(pp.source, c.Name, c.ASIN, pp.id, c.Role, productTier(pp.source, c.ASIN))
		}
		if pp.d.Publisher != "" {
			pubTier := authority.TierB
			if pp.source == authority.SourceLibraryExport {
				pubTier = authority.TierO
			}
			b.addPublisher(pp.source, pp.d.Publisher, pp.id, pubTier, pp.manual, true)
		}
	}
	b.pending = map[string]*pendingProduct{}
}

func digestOf(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		// Only plain strings, bools and slices of them reach here.
		raw = fmt.Appendf(nil, "%#v", v)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// pickDisplay is the most frequent spelling, then the lexically smallest, so
// a rebuild picks the same one.
func pickDisplay(displays map[string]int) (string, []string) {
	names := make([]string, 0, len(displays))
	for n := range displays {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if displays[names[i]] != displays[names[j]] {
			return displays[names[i]] > displays[names[j]]
		}
		return names[i] < names[j]
	})
	variants := append([]string(nil), names[1:]...)
	sort.Strings(variants)
	if len(variants) > maxVariants {
		variants = variants[:maxVariants]
	}
	return names[0], variants
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Finish builds the Result. The Builder must not be used afterwards.
func (b *Builder) Finish() *Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.accumulatePending()
	res := &Result{
		Persons:    make(map[string]*authority.Person, len(b.persons)),
		Publishers: make(map[string]*authority.Publisher, len(b.pubs)),
		ASINs:      map[string]*authority.ASINRef{},
		Ledger:     b.ledger,
	}
	rep := Report{
		Sources:          b.sources,
		PersonsByTier:    map[authority.Tier]int{},
		PublishersByTier: map[authority.Tier]int{},
		PersonsByRole:    map[authority.Role]int{},
	}
	asinFolds := map[string]map[string]bool{}

	for _, f := range sortedKeys(b.persons) {
		acc := b.persons[f]
		display, variants := pickDisplay(acc.displays)
		p := &authority.Person{
			Fold: f, Display: display, Variants: variants,
			ASINs:         sortedKeys(acc.asins),
			Roles:         map[authority.Role]authority.RoleStat{},
			Sources:       sortedKeys(acc.sources),
			ProductSample: acc.products,
		}
		for role, byTier := range acc.roles {
			st := authority.RoleStat{ByTier: map[authority.Tier]int{}}
			for t, n := range byTier {
				st.ByTier[t] = n
				st.Count += n
				st.Tier = authority.Better(st.Tier, t)
			}
			p.Roles[role] = st
			p.Tier = authority.Better(p.Tier, st.Tier)
			rep.PersonsByRole[role]++
		}
		p.HomonymASINs = len(p.ASINs) > 1
		res.Persons[f] = p
		rep.PersonsByTier[p.Tier]++
		sample := NameSample{Fold: f, Display: display, ASINs: p.ASINs, Tier: p.Tier}
		if st, ok := p.Roles[authority.RoleAuthor]; ok && authority.AuthorEvidenceRule(st) {
			rep.AuthorEvidence++
		}
		if _, cast := p.Roles[authority.RoleCastAuthor]; cast && len(p.Roles) == 1 {
			rep.CastOnly++
			rep.CastOnlySample = appendSample(rep.CastOnlySample, sample)
		}
		if p.HomonymASINs {
			rep.Homonyms++
			rep.HomonymSample = appendSample(rep.HomonymSample, sample)
		}
		if authority.IsSingleWord(display) {
			rep.SingleWord++
			rep.SingleWordSample = appendSample(rep.SingleWordSample, sample)
		}
		rep.PersonSample = appendSample(rep.PersonSample, sample)
		for _, a := range p.ASINs {
			if asinFolds[a] == nil {
				asinFolds[a] = map[string]bool{}
			}
			asinFolds[a][f] = true
		}
	}
	for _, a := range sortedKeys(asinFolds) {
		ref := &authority.ASINRef{ASIN: a, Folds: sortedKeys(asinFolds[a]), Sources: sortedKeys(b.asinSources[a])}
		res.ASINs[a] = ref
		if len(ref.Folds) > 1 {
			rep.SpellingASINs++
			rep.SpellingSample = appendSample(rep.SpellingSample, NameSample{Fold: ref.Folds[0], ASINs: []string{a}, Folds: ref.Folds})
		}
	}
	for _, f := range sortedKeys(b.pubs) {
		acc := b.pubs[f]
		display, variants := pickDisplay(acc.displays)
		p := &authority.Publisher{
			Fold: f, Display: display, Variants: variants,
			Count: acc.count, ManualOnlyCount: acc.manualOnly,
			Sources: sortedKeys(acc.sources), Tier: acc.tier, ProductSample: acc.products,
		}
		res.Publishers[f] = p
		rep.PublishersByTier[p.Tier]++
		rep.PublisherSample = appendSample(rep.PublisherSample, NameSample{Fold: f, Display: display, Tier: p.Tier})
	}
	rep.Persons = len(res.Persons)
	rep.Publishers = len(res.Publishers)
	rep.ASINs = len(res.ASINs)
	rep.LedgerRows = len(res.Ledger)
	res.Report = rep
	return res
}

func appendSample(s []NameSample, n NameSample) []NameSample {
	if len(s) >= reportSampleSize {
		return s
	}
	return append(s, n)
}
