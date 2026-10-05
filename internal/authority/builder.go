// file: internal/authority/builder.go
// version: 1.0.0
// guid: 69528b02-de24-4411-8a5e-ac3831986a80
// last-edited: 2026-10-04

package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
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

// RoleStat is the evidence for one role: observations per tier, their sum,
// and the best tier seen.
type RoleStat struct {
	Count  int          `json:"count"`
	Tier   Tier         `json:"tier"`
	ByTier map[Tier]int `json:"by_tier"`
}

// Person is a ref_person: row.
type Person struct {
	Fold     string   `json:"fold"`
	Display  string   `json:"display"`
	Variants []string `json:"variants,omitempty"`
	ASINs    []string `json:"asins,omitempty"`
	// HomonymASINs is set when the fold carries more than one contributor
	// ASIN: two people may share the spelling. Reported, never resolved.
	HomonymASINs  bool              `json:"homonym_asins,omitempty"`
	Roles         map[Role]RoleStat `json:"roles"`
	Sources       []string          `json:"sources"`
	Tier          Tier              `json:"tier"`
	ProductSample []string          `json:"product_sample,omitempty"`
	FirstSeen     time.Time         `json:"first_seen"`
	LastSeen      time.Time         `json:"last_seen"`
}

// Publisher is a ref_pub: row.
type Publisher struct {
	Fold     string   `json:"fold"`
	Display  string   `json:"display"`
	Variants []string `json:"variants,omitempty"`
	// Count is the number of products seen with this publisher;
	// ManualOnlyCount how many of them were manual-only (Doctor Who / Big
	// Finish / Torchwood). The seed contributes no products.
	Count           int       `json:"count"`
	ManualOnlyCount int       `json:"manual_only_count"`
	Sources         []string  `json:"sources"`
	Tier            Tier      `json:"tier"`
	ProductSample   []string  `json:"product_sample,omitempty"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

// ASINRef is a ref_asin:person: row: every fold a contributor ASIN was
// credited under. More than one fold is a spelling variant ("Jonathan Brazee"
// / "Jonathan P. Brazee"); all are kept, none is picked.
type ASINRef struct {
	ASIN  string   `json:"asin"`
	Folds []string `json:"folds"`
}

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
	Fold    string   `json:"fold"`
	Display string   `json:"display"`
	ASINs   []string `json:"asins,omitempty"`
	Folds   []string `json:"folds,omitempty"`
	Tier    Tier     `json:"tier,omitempty"`
}

// Report is what a build found.
type Report struct {
	Sources          map[string]*SourceStats `json:"sources"`
	Persons          int                     `json:"persons"`
	Publishers       int                     `json:"publishers"`
	ASINs            int                     `json:"asins"`
	LedgerRows       int                     `json:"ledger_rows"`
	PersonsByTier    map[Tier]int            `json:"persons_by_tier"`
	PublishersByTier map[Tier]int            `json:"publishers_by_tier"`
	// PersonsByRole counts persons holding each role (a person may hold
	// several).
	PersonsByRole map[Role]int `json:"persons_by_role"`
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
	Persons    map[string]*Person
	Publishers map[string]*Publisher
	ASINs      map[string]*ASINRef
	// Ledger maps a ref_src: key to the digest of what that item contributed.
	Ledger map[string]string
	Report Report
}

type personAcc struct {
	displays map[string]int
	asins    map[string]bool
	roles    map[Role]map[Tier]int
	sources  map[string]bool
	products []string
}

type pubAcc struct {
	displays   map[string]int
	count      int
	manualOnly int
	tier       Tier
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
}

// NewBuilder returns an empty Builder.
func NewBuilder() *Builder {
	return &Builder{
		persons: map[string]*personAcc{},
		pubs:    map[string]*pubAcc{},
		ledger:  map[string]string{},
		sources: map[string]*SourceStats{},
	}
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
		p = &personAcc{displays: map[string]int{}, asins: map[string]bool{}, roles: map[Role]map[Tier]int{}, sources: map[string]bool{}}
		b.persons[fold] = p
	}
	return p
}

func (b *Builder) addPerson(source, name, asin, product string, role Role, tier Tier) {
	name = strings.Join(strings.Fields(name), " ")
	f := Fold(name)
	if f == "" || authorcredit.IsCollectiveCredit(name) {
		return
	}
	p := b.person(f)
	p.displays[name]++
	if a := normalizeASIN(asin); a != "" {
		p.asins[a] = true
	}
	if p.roles[role] == nil {
		p.roles[role] = map[Tier]int{}
	}
	p.roles[role][tier]++
	p.sources[source] = true
	if product != "" {
		p.products = addSample(p.products, product)
	}
}

func (b *Builder) addPublisher(source, name, product string, tier Tier, manualOnly bool, isProduct bool) {
	name = strings.Join(strings.Fields(name), " ")
	f := Fold(name)
	if f == "" {
		return
	}
	p := b.pubs[f]
	if p == nil {
		p = &pubAcc{displays: map[string]int{}, sources: map[string]bool{}}
		b.pubs[f] = p
	}
	p.displays[name]++
	p.tier = better(p.tier, tier)
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
	st := b.stats(SourceSeed)
	for _, e := range s.Entries {
		f := Fold(e.Name)
		id := e.Kind + ":" + f
		key := sourceKey(SourceSeed, id)
		if _, dup := b.ledger[key]; dup {
			st.Duplicates++
			continue
		}
		b.ledger[key] = digestOf(e)
		st.Items++
		switch e.Kind {
		case SeedKindPublisher:
			b.addPublisher(SourceSeed, e.Name, "", s.Tier, false, false)
			continue
		case SeedKindAuthor:
			b.addSeedRoles(e, RoleAuthor, e.AlsoNarrator, RoleNarrator, s.Tier)
		case SeedKindNarrator:
			b.addSeedRoles(e, RoleNarrator, e.AlsoAuthor, RoleAuthor, s.Tier)
		}
	}
}

func (b *Builder) addSeedRoles(e SeedEntry, primary Role, alsoOther bool, other Role, tier Tier) {
	b.addPerson(SourceSeed, e.Name, "", "", primary, tier)
	acc := b.person(Fold(e.Name))
	for _, a := range e.ASINs {
		if n := normalizeASIN(a); n != "" {
			acc.asins[n] = true
		}
	}
	if alsoOther {
		b.addPerson(SourceSeed, e.Name, "", "", other, tier)
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
func productTier(source, asin string) Tier {
	if source == SourceLibraryExport {
		return TierO
	}
	if normalizeASIN(asin) != "" {
		return TierA
	}
	return TierB
}

// productDigest is what one product contributed, for the ledger.
type productDigest struct {
	Cast      bool        `json:"cast"`
	Authors   [][2]string `json:"authors"`
	Narrators [][2]string `json:"narrators"`
	Publisher string      `json:"publisher"`
}

// AddProduct ingests one decoded product from source (SourceCatalog or
// SourceLibraryExport). Only contributor names, contributor ASINs and the
// publisher are kept; the title, subtitle and series are read in memory for
// the cast decision and never stored. A product ASIN already ingested from
// the same source in this build is counted as a duplicate and skipped.
func (b *Builder) AddProduct(source string, p metadata.CatalogProduct) {
	id := normalizeASIN(p.ASIN)
	cast := IsCastContext(p)
	manual := catalog.IsManualOnly(p)
	d := productDigest{Cast: cast, Publisher: strings.TrimSpace(p.Publisher)}
	for _, a := range p.Authors {
		d.Authors = append(d.Authors, [2]string{strings.TrimSpace(a.Name), normalizeASIN(a.ASIN)})
	}
	for _, n := range p.Narrators {
		d.Narrators = append(d.Narrators, [2]string{strings.TrimSpace(n.Name), normalizeASIN(n.ASIN)})
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.stats(source)
	if id == "" || (len(d.Authors) == 0 && len(d.Narrators) == 0 && d.Publisher == "") {
		st.Skipped++
		return
	}
	key := sourceKey(source, id)
	if _, dup := b.ledger[key]; dup {
		st.Duplicates++
		return
	}
	b.ledger[key] = digestOf(d)
	st.Items++
	if cast {
		st.CastContext++
	}
	if manual {
		st.ManualOnly++
	}
	authorRole := RoleAuthor
	if cast {
		authorRole = RoleCastAuthor
	}
	for _, a := range d.Authors {
		b.addPerson(source, a[0], a[1], id, authorRole, productTier(source, a[1]))
	}
	for _, n := range d.Narrators {
		b.addPerson(source, n[0], n[1], id, RoleNarrator, productTier(source, n[1]))
	}
	if d.Publisher != "" {
		pubTier := TierB
		if source == SourceLibraryExport {
			pubTier = TierO
		}
		b.addPublisher(source, d.Publisher, id, pubTier, manual, true)
	}
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

// AuthorEvidenceRule reports whether a person's author role is author
// evidence (design: roles.author >= 1 at tier O or A, or >= 2 counting tier
// B). Tier C alone never is, and cast_author never counts.
func AuthorEvidenceRule(st RoleStat) bool {
	strong := st.ByTier[TierO] + st.ByTier[TierA]
	return strong >= 1 || strong+st.ByTier[TierB] >= 2
}

// narratorEvidenceRule: any structured observation (O, A or B), or two of
// any tier.
func narratorEvidenceRule(st RoleStat) bool {
	return st.ByTier[TierO]+st.ByTier[TierA]+st.ByTier[TierB] >= 1 || st.Count >= 2
}

// Finish builds the Result. The Builder must not be used afterwards.
func (b *Builder) Finish() *Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := &Result{
		Persons:    make(map[string]*Person, len(b.persons)),
		Publishers: make(map[string]*Publisher, len(b.pubs)),
		ASINs:      map[string]*ASINRef{},
		Ledger:     b.ledger,
	}
	rep := Report{
		Sources:          b.sources,
		PersonsByTier:    map[Tier]int{},
		PublishersByTier: map[Tier]int{},
		PersonsByRole:    map[Role]int{},
	}
	asinFolds := map[string]map[string]bool{}

	for _, f := range sortedKeys(b.persons) {
		acc := b.persons[f]
		display, variants := pickDisplay(acc.displays)
		p := &Person{
			Fold: f, Display: display, Variants: variants,
			ASINs:         sortedKeys(acc.asins),
			Roles:         map[Role]RoleStat{},
			Sources:       sortedKeys(acc.sources),
			ProductSample: acc.products,
		}
		for role, byTier := range acc.roles {
			st := RoleStat{ByTier: map[Tier]int{}}
			for t, n := range byTier {
				st.ByTier[t] = n
				st.Count += n
				st.Tier = better(st.Tier, t)
			}
			p.Roles[role] = st
			p.Tier = better(p.Tier, st.Tier)
			rep.PersonsByRole[role]++
		}
		p.HomonymASINs = len(p.ASINs) > 1
		res.Persons[f] = p
		rep.PersonsByTier[p.Tier]++
		sample := NameSample{Fold: f, Display: display, ASINs: p.ASINs, Tier: p.Tier}
		if st, ok := p.Roles[RoleAuthor]; ok && AuthorEvidenceRule(st) {
			rep.AuthorEvidence++
		}
		if _, cast := p.Roles[RoleCastAuthor]; cast && len(p.Roles) == 1 {
			rep.CastOnly++
			rep.CastOnlySample = appendSample(rep.CastOnlySample, sample)
		}
		if p.HomonymASINs {
			rep.Homonyms++
			rep.HomonymSample = appendSample(rep.HomonymSample, sample)
		}
		if isSingleWord(display) {
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
		ref := &ASINRef{ASIN: a, Folds: sortedKeys(asinFolds[a])}
		res.ASINs[a] = ref
		if len(ref.Folds) > 1 {
			rep.SpellingASINs++
			rep.SpellingSample = appendSample(rep.SpellingSample, NameSample{Fold: ref.Folds[0], ASINs: []string{a}, Folds: ref.Folds})
		}
	}
	for _, f := range sortedKeys(b.pubs) {
		acc := b.pubs[f]
		display, variants := pickDisplay(acc.displays)
		p := &Publisher{
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
